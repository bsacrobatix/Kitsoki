// writeback.go implements slice 6 of the node-artifact-materialization plan
// (POG .context/node-artifact-materialization-plan.md): persisting a
// materialize job's results back into the catalog YAML — the node's
// `materialization:` block and `evidence:` entries — so the portal's
// post-`done` reload renders them straight from the catalog (the plan's "the
// catalog is the durable truth" principle), not from transient job state.
//
// # Mechanism: system-authored changeset (POG use-case-loop-plan.md §3.3/§3.4)
//
// This file used to splice the catalog YAML directly (see git history for
// the original comment-preserving-splice implementation). That decision
// predated internal/graph's D9 auto-authorize allowlist
// (autoAuthorizeFieldRoots = {"materialization", "evidence"}), which exists
// *specifically* so a system-authored write like this one can go through
// the same graph.propose/authorize/apply machinery every other catalog edit
// uses — full audit trail (a changeset node records what happened and why),
// pre-apply staleness/lint validation, and zero human-review latency (the
// allowlisted fields self-authorize) — instead of a bespoke, unaudited
// second write path. Now: Propose (with Provenance{job_id, story}, which
// self-authorizes because materialization/evidence are both D9-allowlisted)
// then, since Propose only appends the changeset node and never applies it,
// an immediate Apply of that changeset. Both share the target catalog's
// underlying yaml.Node-tree surgical edit (graph/apply.go's setNodeField),
// which only replaces the touched field's subtree and re-serializes
// everything else from the same in-memory Node objects — this is the exact
// mechanism that already proved safe against pog/catalog.yaml's own block
// scalars when A1 (graph.propose/authorize) landed and re-canonicalized the
// file once (see wi-3-5's summary in pog/catalog.yaml).
package materialize

import (
	"fmt"

	"kitsoki/internal/clock"
	"kitsoki/internal/graph"
)

// EvidenceEntry is one `evidence:` list entry appended to a node — the same
// Legacy entries carry Path. Typed application entries carry only an opaque
// Handle plus phase/output attribution.
type EvidenceEntry struct {
	Kind    string
	Title   string
	Path    string
	Handle  string
	PhaseID string
	Output  string
}

// MaterializationRecord is the node-level `materialization:` block written
// on job completion — the plan's instance-node shape (job_id, status, story,
// stages, artifacts).
type MaterializationRecord struct {
	JobID string
	// SessionID identifies the nested story session that produced this attempt.
	SessionID string
	Status    string
	Story     string
	// ApplicationID identifies a typed registered application producer.
	// It is mutually exclusive with Story.
	ApplicationID string
	Stages        []Stage
	Artifacts     []MaterializationArtifact
	// Checks are the recorded gate-check verdicts (script hash + reproduce
	// command included) — the durable receipt that the node's gate was (or
	// was not) machine-verified, and how to re-run the judgment.
	Checks []CheckResult
	// ContextDigest is the materialization input digest used by readiness to
	// distinguish fresh results from changed declared context.
	ContextDigest string
	// ReceiptIDs are canonical application receipt IDs, one per completed
	// typed phase.
	ReceiptIDs []string
}

// MaterializationArtifact is one `materialization.artifacts[]` entry —
// distinct from EvidenceEntry only in carrying `produced_at`, matching the
// plan's contract example.
type MaterializationArtifact struct {
	Kind       string
	Title      string
	Path       string
	Handle     string
	PhaseID    string
	Output     string
	ProducedAt string
}

// provenance builds the {"job_id", "story"} marker that makes a proposed
// changeset system-authored — combined with a D9-allowlisted field path,
// this is what makes Propose auto-authorize instead of queuing for human
// review (internal/graph/propose.go: allOpsAutoAuthorizable).
func provenance(jobID, story string) map[string]any {
	return map[string]any{"job_id": jobID, "story": story}
}

// proposeAndApply proposes a single-node "modified" changeset touching
// exactly one D9-allowlisted `fields.<key>` path and, if it self-authorized
// (it always should for materialization/evidence — that's the allowlist's
// whole point), applies it immediately so the write actually lands. A
// changeset that comes back "proposed" (not auto-authorized — should not
// happen for these two fields, but checked defensively) or that Apply
// rejects is reported as an error rather than silently left half-done in
// the review queue, since callers here are best-effort (`_ = ...`) anyway.
func proposeAndApply(catalogPath, nodeID, title, fieldKey string, after any, jobID, story string) error {
	res, err := graph.Propose(catalogPath, graph.ProposeInput{
		Title: title,
		Operations: []map[string]any{
			{
				"kind": "modified",
				"node": nodeID,
				"changes": []any{
					map[string]any{"path": []any{"fields", fieldKey}, "after": after},
				},
			},
		},
		Provenance: provenance(jobID, story),
	}, "materialize", clock.Real())
	if err != nil {
		return fmt.Errorf("materialize: writeback: propose %s.%s: %w", nodeID, fieldKey, err)
	}
	if len(res.RejectReasons) > 0 {
		return fmt.Errorf("materialize: writeback: propose %s.%s rejected: %v", nodeID, fieldKey, res.RejectReasons)
	}
	if res.Status != "authorized" {
		return fmt.Errorf("materialize: writeback: propose %s.%s did not auto-authorize (status %q) — D9 allowlist gap?", nodeID, fieldKey, res.Status)
	}
	applyRes, err := graph.Apply(catalogPath, res.ChangesetID, false, "materialize", clock.Real())
	if err != nil {
		return fmt.Errorf("materialize: writeback: apply %s.%s changeset %s: %w", nodeID, fieldKey, res.ChangesetID, err)
	}
	if applyRes.Rejected() {
		return fmt.Errorf("materialize: writeback: apply %s.%s changeset %s rejected: %v %v", nodeID, fieldKey, res.ChangesetID, applyRes.RejectReasons, applyRes.LintIssues)
	}
	return nil
}

func renderMaterializationValue(rec MaterializationRecord) map[string]any {
	stages := make([]any, len(rec.Stages))
	for i, st := range rec.Stages {
		stages[i] = map[string]any{"id": st.ID, "status": st.Status}
	}
	artifacts := make([]any, len(rec.Artifacts))
	for i, a := range rec.Artifacts {
		entry := map[string]any{"kind": a.Kind, "title": a.Title, "produced_at": a.ProducedAt}
		if a.Path != "" {
			entry["path"] = a.Path
		}
		if a.Handle != "" {
			entry["handle"] = a.Handle
			entry["phase"] = a.PhaseID
			entry["output"] = a.Output
		}
		artifacts[i] = entry
	}
	out := map[string]any{
		"job_id":    rec.JobID,
		"status":    rec.Status,
		"stages":    stages,
		"artifacts": artifacts,
	}
	if rec.Story != "" {
		out["story"] = rec.Story
	}
	if rec.ApplicationID != "" {
		out["application_id"] = rec.ApplicationID
	}
	if len(rec.ReceiptIDs) > 0 {
		out["receipt_ids"] = append([]string(nil), rec.ReceiptIDs...)
	}
	if rec.ContextDigest != "" {
		out["context_digest"] = rec.ContextDigest
	}
	if rec.SessionID != "" {
		out["session_id"] = rec.SessionID
	}
	if len(rec.Checks) > 0 {
		checks := make([]any, len(rec.Checks))
		for i, c := range rec.Checks {
			entry := map[string]any{"id": c.ID, "script": c.Script, "ok": c.OK}
			if c.ScriptSHA256 != "" {
				entry["script_sha256"] = c.ScriptSHA256
			}
			if len(c.Reasons) > 0 {
				reasons := make([]any, len(c.Reasons))
				for j, r := range c.Reasons {
					reasons[j] = r
				}
				entry["reasons"] = reasons
			}
			if c.Error != "" {
				entry["error"] = c.Error
			}
			if c.Reproduce != "" {
				entry["reproduce"] = c.Reproduce
			}
			checks[i] = entry
		}
		out["checks"] = checks
	}
	return out
}

// WriteMaterialization upserts nodeID's `materialization:` block in
// catalogPath — called once a materialize job reaches a terminal status
// (complete/failed/cancelled), reflecting the plan's instance-node
// `materialization:` shape. Safe to call more than once for the same job
// (e.g. status transitions mid-run to running then done): each call fully
// replaces the previous block, and each call is its own system-authored
// changeset (§3.3/§3.4).
func WriteMaterialization(catalogPath, nodeID string, rec MaterializationRecord) error {
	cat, err := graph.LoadCatalog(catalogPath)
	if err != nil {
		return fmt.Errorf("materialize: writeback: load catalog: %w", err)
	}
	node := cat.Nodes[graph.NodeID(nodeID)]
	if node == nil {
		return fmt.Errorf("materialize: writeback: node %q not found", nodeID)
	}
	after := renderMaterializationValue(rec)
	// Keep the current convenient summary while retaining every terminal run.
	// Old records that predate attempts[] are promoted once on the next write.
	attempts := materializationAttempts(node.Fields["materialization"])
	if !containsAttempt(attempts, rec.JobID) {
		attempts = append(attempts, attemptValue(after))
	}
	after["attempts"] = attempts
	title := fmt.Sprintf("materialize %s: write back status %q", nodeID, rec.Status)
	producer := rec.Story
	if producer == "" {
		producer = "application:" + rec.ApplicationID
	}
	return proposeAndApply(catalogPath, nodeID, title, "materialization", after, rec.JobID, producer)
}

func materializationAttempts(raw any) []any {
	current, _ := raw.(map[string]any)
	if current == nil {
		return nil
	}
	if attempts, ok := current["attempts"].([]any); ok {
		return append([]any(nil), attempts...)
	}
	if current["job_id"] == nil {
		return nil
	}
	return []any{attemptValue(current)}
}

func attemptValue(record map[string]any) map[string]any {
	attempt := map[string]any{}
	for _, key := range []string{"job_id", "session_id", "status", "story", "application_id", "stages", "artifacts", "checks", "context_digest", "receipt_ids"} {
		if value, ok := record[key]; ok {
			attempt[key] = value
		}
	}
	return attempt
}

func containsAttempt(attempts []any, jobID string) bool {
	for _, raw := range attempts {
		attempt, _ := raw.(map[string]any)
		if attempt["job_id"] == jobID {
			return true
		}
	}
	return false
}

// AppendEvidence appends one evidence entry to nodeID's `evidence:` list in
// catalogPath, unless an entry for the same path or handle is already present. Called
// as soon as a materialize job's world exposes a produced artifact path —
// deliberately not deferred to job completion, so a multi-artifact story's
// evidence lands "as they appear" per the plan, and so a story that fails
// partway through still leaves whatever it already produced visible on the
// node. jobID/story identify the changeset's provenance the same way
// WriteMaterialization's does.
func AppendEvidence(catalogPath, nodeID string, entry EvidenceEntry, jobID, story string) error {
	cat, err := graph.LoadCatalog(catalogPath)
	if err != nil {
		return fmt.Errorf("materialize: writeback: load %s: %w", catalogPath, err)
	}
	node, ok := cat.Nodes[graph.NodeID(nodeID)]
	if !ok {
		return fmt.Errorf("materialize: writeback: node %q not found in %s", nodeID, catalogPath)
	}
	existing, _ := node.Fields["evidence"].([]any)
	next := make([]any, 0, len(existing)+1)
	for _, e := range existing {
		next = append(next, e)
		if em, ok := e.(map[string]any); ok {
			if entry.Path != "" {
				if path, _ := em["path"].(string); path == entry.Path {
					return nil
				}
			}
			if entry.Handle != "" {
				if handle, _ := em["handle"].(string); handle == entry.Handle {
					return nil
				}
			}
		}
	}
	rendered := map[string]any{"kind": entry.Kind, "title": entry.Title}
	if entry.Path != "" {
		rendered["path"] = entry.Path
	}
	if entry.Handle != "" {
		rendered["handle"] = entry.Handle
		rendered["phase"] = entry.PhaseID
		rendered["output"] = entry.Output
	}
	next = append(next, rendered)

	identity := entry.Path
	if identity == "" {
		identity = entry.Handle
	}
	title := fmt.Sprintf("materialize %s: append evidence %s", nodeID, identity)
	return proposeAndApply(catalogPath, nodeID, title, "evidence", next, jobID, story)
}
