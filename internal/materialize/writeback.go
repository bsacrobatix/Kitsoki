// writeback.go implements slice 6 of the node-artifact-materialization plan
// (POG .context/node-artifact-materialization-plan.md): persisting a
// materialize job's results back into the catalog YAML — the node's
// `materialization:` block and `evidence:` entries — so the portal's
// post-`done` reload renders them straight from the catalog (the plan's "the
// catalog is the durable truth" principle), not from transient job state.
//
// # Mechanism decision (plan's decision-queue item 3)
//
// The plan leaves the write-back mechanism open: `internal/graph`'s Apply
// changeset path, or a comment-preserving splice like the POG portal dev
// server's write routes (portal/vite.config.ts's handleSetStatus/
// handleSetField/handleAddEdge). This file chooses the splice approach,
// reimplemented natively in Go against the same file both write:
//
//   - graph.Apply (internal/graph/apply.go) operates on a `changeset` node's
//     authored `operations:` list and REQUIRES that changeset to already
//     carry status `authorized` (or be run dry-run only) — it exists to let
//     a human/LLM propose and review a batch of graph edits before they
//     land, with pre-apply ValidateChangeset staleness checks and post-apply
//     lint. A materialize job's write-back is a single, autonomous,
//     server-authored field update with no proposal/review step and no
//     natural "changeset node" to author — forcing one through Apply would
//     mean synthesizing a throwaway changeset node per job for a workflow
//     Apply was never shaped for, and its all-or-nothing re-serialization
//     risk (see apply.go's own comments on rewriting the whole catalog) is
//     exactly what the portal's splice routes were built to avoid.
//   - The portal's splice routes already solve this exact problem
//     (single-node, single-field edits with comments and hand-formatting
//     preserved) for the POG catalog specifically. This file is a Go-native
//     port of that same technique (nodeBlockRange / yq / insert-before-edges
//     conventions all mirror portal/vite.config.ts line for line) so the
//     kitsoki-side job handler can make the same kind of surgical edit
//     without shelling out to the portal's Vite dev server or requiring one
//     to be running — materialize jobs run just as well against a bare
//     `kitsoki graph materialize` CLI invocation with no portal in the loop.
//
// Both write paths (this file's node-block splice, and the portal's own
// splice routes) target the same file with the same indentation convention,
// so a node written to by one remains readable/editable by the other.
package materialize

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// EvidenceEntry is one `evidence:` list entry appended to a node — the same
// {kind, title, path} shape POG's catalog already hand-authors (see
// pog/catalog.yaml's work-item entries) and portal/catalog-model.ts's
// nodeEvidence() reads.
type EvidenceEntry struct {
	Kind  string
	Title string
	Path  string
}

// MaterializationRecord is the node-level `materialization:` block written
// on job completion — the plan's instance-node shape (job_id, status, story,
// stages, artifacts).
type MaterializationRecord struct {
	JobID     string
	Status    string
	Story     string
	Stages    []Stage
	Artifacts []MaterializationArtifact
}

// MaterializationArtifact is one `materialization.artifacts[]` entry —
// distinct from EvidenceEntry only in carrying `produced_at`, matching the
// plan's contract example.
type MaterializationArtifact struct {
	Kind       string
	Title      string
	Path       string
	ProducedAt string
}

// continuationRE matches a line that is a deeper-indented continuation of a
// block-scalar field (5+ leading spaces, non-blank) — the same test
// portal/vite.config.ts's handleSetField uses to find where a field's old
// value ends.
var continuationRE = regexp.MustCompile(`^\s{5,}\S`)

// yq renders value as a YAML double-quoted scalar, matching
// portal/vite.config.ts's yq() convention for values spliced into the file
// by hand (guarantees valid YAML regardless of colons/quotes/unicode in the
// source value, at the cost of never round-tripping as an unquoted scalar).
func yq(value string) string {
	b, _ := json.Marshal(value)
	return string(b)
}

// nodeBlockRange locates the [start, end) line range of a node's block in
// catalog source lines: from its `  - schema:` list-entry line to the line
// before the next entry (or EOF). Node ids live at exactly 4-space indent
// (`    id: <id>`), the same convention portal/vite.config.ts's
// nodeBlockRange relies on to stay distinct from type_registry ids (2-space
// list entries) and source_window input ids (6-space).
func nodeBlockRange(lines []string, nodeID string) (start, end int, ok bool) {
	want := "    id: " + nodeID
	idLine := -1
	for i, l := range lines {
		if l == want {
			idLine = i
			break
		}
	}
	if idLine == -1 {
		return 0, 0, false
	}
	start = idLine
	for start > 0 && !strings.HasPrefix(lines[start], "  - ") {
		start--
	}
	end = idLine + 1
	for end < len(lines) && !strings.HasPrefix(lines[end], "  - ") {
		end++
	}
	return start, end, true
}

// blockInsertionPoint returns where a new top-level (4-space) block field
// should be inserted within a node's [start, end) range: right before the
// `edges:` block if one exists, otherwise before any trailing blank/comment
// padding at the end of the block — the same convention
// handleSetField/handleAddEdge use in portal/vite.config.ts.
func blockInsertionPoint(lines []string, start, end int) int {
	for i := start; i < end; i++ {
		if strings.HasPrefix(lines[i], "    edges:") {
			return i
		}
	}
	insertAt := end
	for insertAt > start && (strings.TrimSpace(lines[insertAt-1]) == "" || strings.HasPrefix(strings.TrimSpace(lines[insertAt-1]), "#")) {
		insertAt--
	}
	return insertAt
}

// upsertTopLevelBlock replaces (or inserts) the `    <key>:` block within a
// node's [start, end) range with newLines, consuming any existing block's
// continuation lines the same way handleSetField consumes a multi-line
// field's old value.
func upsertTopLevelBlock(lines []string, start, end int, key string, newLines []string) []string {
	prefix := "    " + key + ":"
	idx := -1
	for i := start; i < end; i++ {
		if lines[i] == prefix || strings.HasPrefix(lines[i], prefix+" ") {
			idx = i
			break
		}
	}
	if idx == -1 {
		insertAt := blockInsertionPoint(lines, start, end)
		out := make([]string, 0, len(lines)+len(newLines))
		out = append(out, lines[:insertAt]...)
		out = append(out, newLines...)
		out = append(out, lines[insertAt:]...)
		return out
	}
	last := idx + 1
	for last < end && continuationRE.MatchString(lines[last]) {
		last++
	}
	out := make([]string, 0, len(lines)+len(newLines))
	out = append(out, lines[:idx]...)
	out = append(out, newLines...)
	out = append(out, lines[last:]...)
	return out
}

// renderMaterializationBlock renders a node's `materialization:` block.
func renderMaterializationBlock(rec MaterializationRecord) []string {
	lines := []string{
		"    materialization:",
		"      job_id: " + yq(rec.JobID),
		"      status: " + rec.Status,
		"      story: " + rec.Story,
		"      stages:",
	}
	for _, st := range rec.Stages {
		lines = append(lines, fmt.Sprintf("        - { id: %s, status: %s }", st.ID, st.Status))
	}
	if len(rec.Artifacts) == 0 {
		lines = append(lines, "      artifacts: []")
		return lines
	}
	lines = append(lines, "      artifacts:")
	for _, a := range rec.Artifacts {
		lines = append(lines, fmt.Sprintf(
			"        - { kind: %s, title: %s, path: %s, produced_at: %s }",
			a.Kind, yq(a.Title), a.Path, yq(a.ProducedAt),
		))
	}
	return lines
}

// WriteMaterialization upserts nodeID's `materialization:` block in
// catalogPath — called once a materialize job reaches a terminal status
// (complete/failed/cancelled), reflecting the plan's instance-node
// `materialization:` shape. Safe to call more than once for the same job
// (e.g. status transitions mid-run to running then done): each call fully
// replaces the previous block.
func WriteMaterialization(catalogPath, nodeID string, rec MaterializationRecord) error {
	info, lines, err := readCatalogLines(catalogPath)
	if err != nil {
		return err
	}
	start, end, ok := nodeBlockRange(lines, nodeID)
	if !ok {
		return fmt.Errorf("materialize: writeback: node %q not found in %s", nodeID, catalogPath)
	}
	lines = upsertTopLevelBlock(lines, start, end, "materialization", renderMaterializationBlock(rec))
	return writeCatalogLines(catalogPath, lines, info.Mode())
}

// hasEvidencePath reports whether nodeID's evidence: list in lines[start,end)
// already carries an entry for path — the append-once dedupe guard so a
// re-run (restart intent, or a second materialize pass) does not pile up
// duplicate evidence rows for the same artifact path.
func hasEvidencePath(lines []string, start, end int, path string) bool {
	want := "        path: " + path
	for i := start; i < end; i++ {
		if lines[i] == want {
			return true
		}
	}
	return false
}

// appendEvidenceEntry appends one evidence item to nodeID's `evidence:`
// list within lines[start,end), creating the list if the node has none yet.
// New items always land at the END of the list — existing hand-authored
// evidence rows and their comments are left untouched.
func appendEvidenceEntry(lines []string, start, end int, entry EvidenceEntry) []string {
	itemLines := []string{
		"      - kind: " + entry.Kind,
		"        title: " + yq(entry.Title),
		"        path: " + entry.Path,
	}
	evIdx := -1
	for i := start; i < end; i++ {
		if lines[i] == "    evidence:" {
			evIdx = i
			break
		}
	}
	if evIdx == -1 {
		block := append([]string{"    evidence:"}, itemLines...)
		insertAt := blockInsertionPoint(lines, start, end)
		out := make([]string, 0, len(lines)+len(block))
		out = append(out, lines[:insertAt]...)
		out = append(out, block...)
		out = append(out, lines[insertAt:]...)
		return out
	}
	last := evIdx + 1
	for last < end && continuationRE.MatchString(lines[last]) {
		last++
	}
	out := make([]string, 0, len(lines)+len(itemLines))
	out = append(out, lines[:last]...)
	out = append(out, itemLines...)
	out = append(out, lines[last:]...)
	return out
}

// AppendEvidence appends one evidence entry to nodeID's `evidence:` list in
// catalogPath, unless an entry for the same path is already present. Called
// as soon as a materialize job's world exposes a produced artifact path —
// deliberately not deferred to job completion, so a multi-artifact story's
// evidence lands "as they appear" per the plan, and so a story that fails
// partway through still leaves whatever it already produced visible on the
// node.
func AppendEvidence(catalogPath, nodeID string, entry EvidenceEntry) error {
	info, lines, err := readCatalogLines(catalogPath)
	if err != nil {
		return err
	}
	start, end, ok := nodeBlockRange(lines, nodeID)
	if !ok {
		return fmt.Errorf("materialize: writeback: node %q not found in %s", nodeID, catalogPath)
	}
	if hasEvidencePath(lines, start, end, entry.Path) {
		return nil
	}
	lines = appendEvidenceEntry(lines, start, end, entry)
	return writeCatalogLines(catalogPath, lines, info.Mode())
}

func readCatalogLines(path string) (os.FileInfo, []string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("materialize: writeback: stat %s: %w", path, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("materialize: writeback: read %s: %w", path, err)
	}
	return info, strings.Split(string(raw), "\n"), nil
}

func writeCatalogLines(path string, lines []string, mode os.FileMode) error {
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), mode); err != nil {
		return fmt.Errorf("materialize: writeback: write %s: %w", path, err)
	}
	return nil
}
