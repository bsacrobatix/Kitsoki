// materialize.go — the graph.materialize.* JSON-RPC method family
// (node-artifact-materialization plan slice 4, POG
// .context/node-artifact-materialization-plan.md): starts, polls, cancels,
// and (best-effort) answers a node materialization job. Delegates the actual
// binding resolution / gate validation / headless story drive to
// internal/materialize (slice 3); this file's job is translating that
// package's Start/Scheduler shapes into the plan's RPC wire contract and
// keeping enough server-side bookkeeping that a `.status` poll sees the same
// picture a live SSE subscriber would (see materializeJobState below).
//
// Paired with materialize_stream.go's GET /rpc/materialize-stream, which
// streams the same job's progress live (cloned from turn_stream.go's SSE
// pattern).
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kitsoki/internal/graph"
	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
	"kitsoki/internal/materializationstatus"
	"kitsoki/internal/materialize"
)

// materializeStageWire is the `.start` response's per-stage shape: {id, title}.
// Title falls back to the room id itself when the story's state carries no
// `description:` (app.State.Description is the closest thing kitsoki stories
// have to a stage title).
type materializeStageWire struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// materializeArtifact is the wire shape of one produced-artifact entry, both
// in a `.status` response's `artifacts` list and in a stream `artifact` frame.
type materializeArtifact struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Handle string `json:"handle"`
}

// materializeJobState is the server's own bookkeeping for one materialize
// job, updated by a background goroutine subscribed as early as possible
// (started synchronously, before graph.materialize.start returns) — see
// internal/materialize's package doc on the Subscribe-after-Start race: a
// fast/deterministic job can fan out its early heartbeats before a caller
// gets around to subscribing. The scheduler itself only remembers the LATEST
// heartbeat payload (jobs.Job.Progress); the full per-stage status array and
// the artifacts list a `.status` poll needs are accumulated here instead.
type materializeJobState struct {
	mu     sync.Mutex
	nodeID string
	// sessionID is the live web-registry session the job drives, when the
	// provider could seed one (see materializeSessionDriver) — the id the
	// SPA's /#/s/<id> trace + /#/s/<id>/chat transcript routes take. Empty
	// on the private-rig fallback.
	sessionID     string
	stages        []materialize.Stage
	gates         []string
	artifactKind  string
	artifactTitle string
	applicationID string
	jobID         string
	stageTitles   map[string]string
	status        string // running | awaiting_input | done | failed | cancelled
	artifacts     []materializeArtifact
	receiptIDs    []string
}

func (st *materializeJobState) snapshot() (stages []materialize.Stage, status string, artifacts []materializeArtifact) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]materialize.Stage(nil), st.stages...), st.status, append([]materializeArtifact(nil), st.artifacts...)
}

func (st *materializeJobState) gatesSnapshot() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string(nil), st.gates...)
}

func (st *materializeJobState) applyStageEvent(se materialize.StageEvent) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.stages {
		if st.stages[i].ID == se.Stage {
			st.stages[i].Status = se.Status
			return
		}
	}
}

func (st *materializeJobState) applyStageSnapshot(stages []materialize.Stage) {
	for _, stage := range stages {
		st.applyStageEvent(materialize.StageEvent{Stage: stage.ID, Status: stage.Status})
	}
}

func (st *materializeJobState) setStatus(status string) {
	st.mu.Lock()
	st.status = status
	st.mu.Unlock()
}

func (st *materializeJobState) addArtifact(a materializeArtifact) {
	st.mu.Lock()
	st.artifacts = append(st.artifacts, a)
	st.mu.Unlock()
}

func (st *materializeJobState) setReceiptIDs(ids []string) {
	st.mu.Lock()
	st.receiptIDs = append([]string(nil), ids...)
	st.mu.Unlock()
}

func (st *materializeJobState) receiptIDsSnapshot() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string(nil), st.receiptIDs...)
}

func (st *materializeJobState) artifactFor(path string) materializeArtifact {
	st.mu.Lock()
	defer st.mu.Unlock()
	return materializationArtifactFor(path, st.artifactKind, st.artifactTitle)
}

func (st *materializeJobState) artifactForHandle(handle string) materializeArtifact {
	st.mu.Lock()
	defer st.mu.Unlock()
	return materializeArtifact{Kind: st.artifactKind, Title: st.artifactTitle, Handle: handle}
}

func materializationArtifactFor(path, kind, title string) materializeArtifact {
	sum := sha256.Sum256([]byte(path))
	return materializeArtifact{
		Kind: kind, Title: title,
		Handle: "ma_" + hex.EncodeToString(sum[:16]),
	}
}

func (st *materializeJobState) durableRecord(now time.Time) materializationstatus.Record {
	st.mu.Lock()
	defer st.mu.Unlock()
	stages := make([]materializationstatus.Stage, len(st.stages))
	for i, stage := range st.stages {
		title := st.stageTitles[stage.ID]
		if title == "" {
			title = stage.ID
		}
		stages[i] = materializationstatus.Stage{ID: stage.ID, Title: title, Status: stage.Status}
	}
	artifacts := make([]materializationstatus.Artifact, len(st.artifacts))
	for i, artifact := range st.artifacts {
		artifacts[i] = materializationstatus.Artifact{
			Kind: artifact.Kind, Title: artifact.Title, Handle: artifact.Handle,
		}
	}
	return materializationstatus.Record{
		ApplicationID: st.applicationID, JobID: st.jobID, SessionID: st.sessionID,
		Status: st.status, Stages: stages, Artifacts: artifacts,
		ReceiptIDs: append([]string(nil), st.receiptIDs...), UpdatedAt: now.UTC(),
	}
}

// materializeArtifactFromResult builds the wire artifact entry from a
// terminal JobEvent's Result, if it carries a non-empty artifact_path.
func materializeArtifactFromResult(state *materializeJobState, ev jobs.JobEvent) (materializeArtifact, bool) {
	if ev.Result == nil {
		return materializeArtifact{}, false
	}
	p, ok := ev.Result.Data["artifact_path"].(string)
	if !ok || p == "" {
		return materializeArtifact{}, false
	}
	return state.artifactFor(p), true
}

func typedMaterializeResults(state *materializeJobState, ev jobs.JobEvent) ([]materializeArtifact, []string) {
	if ev.Result == nil {
		return nil, nil
	}
	rawHandles := materializeResultStrings(ev.Result.Data, "artifact_handles")
	rawReceipts := materializeResultStrings(ev.Result.Data, "receipt_ids")
	artifacts := make([]materializeArtifact, len(rawHandles))
	for i, handle := range rawHandles {
		artifacts[i] = state.artifactForHandle(handle)
	}
	return artifacts, rawReceipts
}

func materializeResultStrings(data map[string]any, key string) []string {
	raw, _ := data[key].([]string)
	if raw != nil {
		return append([]string(nil), raw...)
	}
	if values, ok := data[key].([]any); ok {
		for _, value := range values {
			if item, ok := value.(string); ok {
				raw = append(raw, item)
			}
		}
	}
	return raw
}

func materializeStagesFromResult(ev jobs.JobEvent) []materialize.Stage {
	if ev.Result == nil {
		return nil
	}
	value, ok := ev.Result.Data["stages"]
	if !ok {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var stages []materialize.Stage
	if err := json.Unmarshal(raw, &stages); err != nil {
		return nil
	}
	return stages
}

// dispatchMaterialize handles the graph.materialize.* method family. It
// returns (result, nil, true) when it handled the method, or (nil, nil,
// false) when the method is not one of this family's, so the caller can fall
// through to the next dispatcher — same convention as dispatchObjectGraph /
// dispatchEditor.
func (s *Server) dispatchMaterialize(ctx context.Context, method string, params map[string]any) (any, *rpcError, bool) {
	switch method {
	case "graph.materialize.start":
		result, rerr := s.materializeStart(ctx, params)
		return result, rerr, true
	case "graph.materialize.status":
		result, rerr := s.materializeStatus(params)
		return result, rerr, true
	case "graph.materialize.cancel":
		result, rerr := s.materializeCancel(ctx, params)
		return result, rerr, true
	case "graph.materialize.answer":
		result, rerr := s.materializeAnswer(params)
		return result, rerr, true
	case "graph.materialize.checks":
		result, rerr := s.materializeChecks(ctx, params)
		return result, rerr, true
	case "graph.materialize.readiness":
		result, rerr := s.materializeReadiness(params)
		return result, rerr, true
	default:
		return nil, nil, false
	}
}

// resolveMaterializeCatalog resolves the `catalog` param shared by
// graph.materialize.start and graph.materialize.checks. `catalog` is an
// ALIAS (never a raw filesystem path) resolved through s.graphAllowlist() —
// the same allowlist graph.propose/authorize/withdraw/apply/rebase resolve
// their own `catalog` param through (F1, catalog_allowlist.go /
// resolveGraphCatalogParam). Unlike those five verbs, the two
// graph.materialize.* handlers have no `catalog_path` raw-path back-compat
// param: neither ever had a working federated caller before this change, so
// there is nothing depending on raw-path behavior surviving, and accepting
// one here would reopen exactly the gap F1 closed everywhere else.
//
// The returned repoRoot is the resolved catalog's own repository root — two
// filepath.Dir() calls up from its `pog/catalog.yaml` path, mirroring
// buildCatalogAllowlist's absTrack derivation (<home>/<repo>/pog/catalog.yaml)
// exactly, so a "pog" alias still yields s.materializeRoot (zero behavior
// change for the home-catalog case) while a federated member alias yields
// that member's own root. RepoRoot controls BOTH where a node's
// materialize.story path is looked up (internal/materialize.Prepare joins it
// against RepoRoot) and where produced artifacts are written back
// (internal/materialize's write-back path also joins against RepoRoot) — so
// getting this right is what makes materializing a federated member's node
// read and write that member's own tree instead of the home repo's.
func (s *Server) resolveMaterializeCatalog(params map[string]any, rpcMethod string) (catalogPath, repoRoot string, rerr *rpcError) {
	alias := graphStringParam(params, "catalog")
	if alias == "" {
		return "", "", &rpcError{Code: codeServerError, Message: rpcMethod + ": missing 'catalog'"}
	}
	resolved, rerr := resolveGraphCatalogParam(s.graphAllowlist(), alias, "", rpcMethod)
	if rerr != nil {
		return "", "", rerr
	}
	root := filepath.Dir(filepath.Dir(resolved))
	if root == "" {
		root = "."
	}
	return resolved, root, nil
}

// materializeStart implements graph.materialize.start {catalog, node_id,
// params} → {job_id, stages: [{id, title}]}. Gates are validated
// server-side by internal/materialize.Start; an unmet gate rejects with the
// unmet field list both in the error message and (machine-readable) in the
// rpcError's Data as a JSON array. See resolveMaterializeCatalog's doc
// comment for `catalog`'s alias-only semantics and RepoRoot derivation.
func (s *Server) materializeStart(ctx context.Context, params map[string]any) (any, *rpcError) {
	catalogPath, repoRoot, rerr := s.resolveMaterializeCatalog(params, "graph.materialize.start")
	if rerr != nil {
		return nil, rerr
	}
	nodeID, _ := params["node_id"].(string)
	if nodeID == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.start: missing 'node_id'"}
	}
	paramArgs, _ := params["params"].(map[string]any)

	cat, err := graph.LoadCatalog(catalogPath)
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.start: " + err.Error()}
	}
	node, ok := cat.Nodes[graph.NodeID(nodeID)]
	if !ok {
		return nil, &rpcError{Code: codeServerError, Message: fmt.Sprintf("graph.materialize.start: node %q not found in catalog", nodeID)}
	}
	if _, err := materialize.ResolveBinding(cat, node); err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.start: " + err.Error()}
	}
	prep, err := materialize.Prepare(materialize.Request{
		CatalogPath: catalogPath,
		CatalogRef:  graphStringParam(params, "catalog"),
		RepoRoot:    repoRoot,
		NodeID:      graph.NodeID(nodeID),
		Params:      paramArgs,
	})
	if err != nil {
		var gateErr *materialize.GateError
		if errors.As(err, &gateErr) {
			unmetJSON, _ := json.Marshal(gateErr.Unmet)
			return nil, &rpcError{Code: codeServerError, Message: gateErr.Error(), Data: string(unmetJSON)}
		}
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.start: " + err.Error()}
	}

	// Prefer driving the story through a REAL web session when the provider
	// can seed one (`kitsoki web`'s live registry): the drive then has a
	// browsable trace + transcript at /#/s/<session_id> — the portal's "open
	// the conversation in the kitsoki viewer" affordance. Anything short of a
	// fully drivable session (no seeded provider, seeding failed, no write
	// driver, no world reader) falls back to the self-contained private rig,
	// which remains the CLI / `status serve` path.
	var (
		webSessionID        string
		turnDriver          materialize.TurnDriver
		applicationExecutor materialize.ApplicationPhaseExecutor
	)
	if prep.Binding.ApplicationID != "" {
		webSessionID, applicationExecutor, err = s.materializeRegisteredApplication(ctx, prep)
		if err != nil {
			return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.start: " + err.Error()}
		}
	} else {
		webSessionID, turnDriver = s.materializeSessionDriver(ctx, prep)
	}

	// Titles are resolved before submission because a deterministic job may
	// finish before SubmitObserved returns and its terminal observer needs the
	// complete durable stage shape.
	stageTitles := make(map[string]string, len(prep.Stages))
	stagesOut := make([]materializeStageWire, len(prep.Stages))
	for i, stageID := range prep.Stages {
		title := stageID
		if checkID, isCheck := strings.CutPrefix(stageID, materialize.CheckStagePrefix); isCheck {
			title = "Gate check: " + checkID
		} else if prep.Def != nil {
			if roomState, ok := prep.Def.States[stageID]; ok && roomState.Description != "" {
				title = roomState.Description
			}
		}
		stageTitles[stageID] = title
		stagesOut[i] = materializeStageWire{ID: stageID, Title: title}
	}
	artifactKind := prep.Binding.ArtifactKind
	artifactTitle := fmt.Sprintf("%s artifact", prep.Binding.TypeID)
	terminalObserver := materialize.TerminalObserver(nil)
	if s.materializeProjection != nil && s.materializeApplication != "" {
		terminalObserver = func(
			observeCtx context.Context,
			observedJobID jobs.JobID,
			result host.Result,
			runErr error,
		) error {
			status := string(jobs.JobDone)
			if runErr != nil || result.Error != "" {
				status = string(jobs.JobFailed)
			}
			stages := materializeStagesFromResult(jobs.JobEvent{Result: &result})
			if len(stages) == 0 {
				stages = make([]materialize.Stage, len(prep.Stages))
				for i, id := range prep.Stages {
					stages[i] = materialize.Stage{ID: id, Status: "waiting"}
				}
			}
			durableStages := make([]materializationstatus.Stage, len(stages))
			for i, stage := range stages {
				durableStages[i] = materializationstatus.Stage{
					ID: stage.ID, Title: stageTitles[stage.ID], Status: stage.Status,
				}
			}
			var artifacts []materializationstatus.Artifact
			for _, handle := range materializeResultStrings(result.Data, "artifact_handles") {
				artifacts = append(artifacts, materializationstatus.Artifact{
					Kind: artifactKind, Title: artifactTitle, Handle: handle,
				})
			}
			if path, _ := result.Data["artifact_path"].(string); path != "" {
				artifact := materializationArtifactFor(path, artifactKind, artifactTitle)
				artifacts = append(artifacts, materializationstatus.Artifact{
					Kind: artifact.Kind, Title: artifact.Title, Handle: artifact.Handle,
				})
			}
			persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(observeCtx), 5*time.Second)
			defer cancelPersist()
			_, err := s.materializeProjection.Save(persistCtx, materializationstatus.Record{
				ApplicationID: s.materializeApplication,
				JobID:         string(observedJobID),
				SessionID:     webSessionID,
				Status:        status,
				Stages:        durableStages,
				Artifacts:     artifacts,
				ReceiptIDs:    materializeResultStrings(result.Data, "receipt_ids"),
				UpdatedAt:     s.materializationTimestamp(),
			})
			return err
		}
	}

	jobID, stages, err := prep.SubmitObserved(
		ctx, s.materializeSched, turnDriver, webSessionID, terminalObserver, applicationExecutor,
	)
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.start: " + err.Error()}
	}

	state := &materializeJobState{
		nodeID:        nodeID,
		sessionID:     webSessionID,
		stages:        append([]materialize.Stage(nil), stages...),
		gates:         prep.Binding.Gates,
		artifactKind:  artifactKind,
		artifactTitle: artifactTitle,
		applicationID: s.materializeApplication,
		jobID:         string(jobID),
		stageTitles:   stageTitles,
		status:        string(jobs.JobRunning),
	}
	s.materializeMu.Lock()
	s.materializeJobs[jobID] = state
	s.materializeMu.Unlock()

	if s.materializeProjection != nil && s.materializeApplication != "" {
		if _, err := s.materializeProjection.Save(ctx, state.durableRecord(s.materializationTimestamp())); err != nil {
			_ = s.materializeSched.Cancel(ctx, jobID)
			return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.start: persist durable projection: " + err.Error()}
		}
	}

	// Subscribe synchronously, before returning, so a .status poll can never
	// see less progress than a live SSE subscriber that connected late would
	// have (see internal/materialize's doc comment on this exact race).
	s.trackMaterializeJob(jobID, state)

	return map[string]any{
		"job_id":     jobID,
		"stages":     stagesOut,
		"session_id": webSessionID,
	}, nil
}

func (s *Server) materializationTimestamp() time.Time {
	if s.materializeNow != nil {
		return s.materializeNow().UTC()
	}
	return time.Now().UTC()
}

// materializeReadiness exposes gate readiness plus deterministic context
// freshness without starting a job. Historical records lacking context_digest
// intentionally return stale_status=unknown rather than fresh.
func (s *Server) materializeReadiness(params map[string]any) (any, *rpcError) {
	catalogPath, _, rerr := s.resolveMaterializeCatalog(params, "graph.materialize.readiness")
	if rerr != nil {
		return nil, rerr
	}
	nodeID := graphStringParam(params, "node_id")
	if nodeID == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.readiness: missing 'node_id'"}
	}
	paramArgs, _ := params["params"].(map[string]any)
	cat, err := graph.LoadCatalog(catalogPath)
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.readiness: " + err.Error()}
	}
	node := cat.Nodes[graph.NodeID(nodeID)]
	if node == nil {
		return nil, &rpcError{Code: codeServerError, Message: fmt.Sprintf("graph.materialize.readiness: node %q not found in catalog", nodeID)}
	}
	binding, err := materialize.ResolveBinding(cat, node)
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.readiness: " + err.Error()}
	}
	readiness := materialize.MaterializeReadiness(cat, node, binding, paramArgs)
	return map[string]any{"node_id": nodeID, "ready": readiness.Ready, "missing_gates": readiness.MissingGates, "missing_params": readiness.MissingParams, "context_digest": readiness.ContextDigest, "stale": readiness.Stale, "stale_status": readiness.StaleStatus, "stale_reason": readiness.StaleReason}, nil
}

// materializeChecks implements graph.materialize.checks {catalog, node_id} —
// the pre-flight surface behind the portal's "what will actually be
// verified" panel. For each of the node's resolved gate checks it returns
// the Starlark script (path AND source text — the user must be able to read
// the assertion, not trust a label), the concrete inputs the node binds, the
// sandbox capabilities, the current verdict from running the check NOW, and
// the exact `kitsoki starlark run` command that reproduces that verdict.
// Read-only: evaluating a check never mutates the catalog or starts a job.
// See resolveMaterializeCatalog's doc comment for `catalog`'s alias-only
// semantics and RepoRoot derivation.
func (s *Server) materializeChecks(ctx context.Context, params map[string]any) (any, *rpcError) {
	catalogPath, repoRoot, rerr := s.resolveMaterializeCatalog(params, "graph.materialize.checks")
	if rerr != nil {
		return nil, rerr
	}
	nodeID, _ := params["node_id"].(string)
	if nodeID == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.checks: missing 'node_id'"}
	}

	cat, err := graph.LoadCatalog(catalogPath)
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.checks: " + err.Error()}
	}
	node, ok := cat.Nodes[graph.NodeID(nodeID)]
	if !ok {
		return nil, &rpcError{Code: codeServerError, Message: fmt.Sprintf("graph.materialize.checks: node %q not found in catalog", nodeID)}
	}
	binding, err := materialize.ResolveBinding(cat, node)
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.checks: " + err.Error()}
	}

	resolved := materialize.ResolveChecks(node, binding.Checks)
	out := make([]map[string]any, 0, len(resolved))
	for _, rc := range resolved {
		entry := map[string]any{
			"id":         rc.ID,
			"script":     rc.Script,
			"inputs":     rc.Inputs,
			"unresolved": rc.Unresolved,
		}
		if len(rc.Capabilities) > 0 {
			entry["capabilities"] = rc.Capabilities
		}
		if rc.Script != "" {
			absScript := rc.Script
			if !filepath.IsAbs(absScript) {
				absScript = filepath.Join(repoRoot, rc.Script)
			}
			if raw, err := os.ReadFile(absScript); err == nil {
				entry["source"] = string(raw)
			}
		}
		result := materialize.RunCheck(ctx, repoRoot, rc)
		entry["ok"] = result.OK
		entry["reasons"] = result.Reasons
		entry["error"] = result.Error
		entry["script_sha256"] = result.ScriptSHA256
		entry["reproduce"] = result.Reproduce
		out = append(out, entry)
	}
	return map[string]any{"node_id": nodeID, "checks": out}, nil
}

// sessionTurnDriver adapts a live web-registry session's [Driver] +
// [WorldReader] to internal/materialize's TurnDriver, so the materialize job
// drives the same session the browser can watch.
type sessionTurnDriver struct {
	driver Driver
	world  WorldReader
}

func (d sessionTurnDriver) Next(ctx context.Context) error {
	_, err := d.driver.SubmitDirect(ctx, "next", nil)
	return err
}

func (d sessionTurnDriver) World(ctx context.Context) (map[string]any, error) {
	return d.world.CurrentWorld(ctx)
}

// materializeSessionDriver tries to create and seed a live web session for
// prep's story via the provider and wrap it as a TurnDriver. Returns
// ("", nil) — the private-rig fallback — when the provider cannot seed
// sessions, seeding fails, or the session's driver cannot advance turns and
// read world state. Never fails the RPC: the fallback is always valid.
func (s *Server) materializeSessionDriver(ctx context.Context, prep *materialize.Prepared) (string, materialize.TurnDriver) {
	seeded, ok := s.provider.(SeededSessionProvider)
	if !ok {
		return "", nil
	}
	storyAbs, err := filepath.Abs(prep.StoryAppPath)
	if err != nil {
		return "", nil
	}
	sessionID, err := seeded.NewSessionSeeded(ctx, storyAbs, prep.InitialWorld)
	if err != nil || sessionID == "" {
		return "", nil
	}
	entry, ok := s.provider.Get(sessionID)
	if !ok || entry.Driver == nil {
		return "", nil
	}
	worldReader, ok := entry.Driver.(WorldReader)
	if !ok {
		return "", nil
	}
	return sessionID, sessionTurnDriver{driver: entry.Driver, world: worldReader}
}

// trackMaterializeJob subscribes to jobID's events and keeps state in step:
// per-stage status from every StageEvent heartbeat, overall status from
// every JobEvent.Status transition, and the produced artifact once the job
// reaches JobDone. The goroutine exits when the scheduler closes the
// subscription channel (job terminal).
func (s *Server) trackMaterializeJob(jobID jobs.JobID, state *materializeJobState) {
	ch, unsub := s.materializeSched.Subscribe(jobID)
	go func() {
		defer unsub()
		for ev := range ch {
			if se, ok := ev.Progress.(materialize.StageEvent); ok {
				state.applyStageEvent(se)
			}
			if stages := materializeStagesFromResult(ev); len(stages) > 0 {
				state.applyStageSnapshot(stages)
			}
			switch ev.Status {
			case jobs.JobRunning:
				state.setStatus(string(jobs.JobRunning))
			case jobs.JobAwaitingInput:
				state.setStatus(string(jobs.JobAwaitingInput))
			case jobs.JobDone:
				if artifacts, receipts := typedMaterializeResults(state, ev); len(artifacts) > 0 || len(receipts) > 0 {
					for _, artifact := range artifacts {
						state.addArtifact(artifact)
					}
					state.setReceiptIDs(receipts)
				} else if a, ok := materializeArtifactFromResult(state, ev); ok {
					state.addArtifact(a)
				}
				state.setStatus(string(jobs.JobDone))
			case jobs.JobFailed:
				state.setStatus(string(jobs.JobFailed))
			case jobs.JobCancelled:
				state.setStatus(string(jobs.JobCancelled))
			}
			if s.materializeProjection != nil && state.applicationID != "" {
				if _, err := s.materializeProjection.Save(
					context.Background(),
					state.durableRecord(s.materializationTimestamp()),
				); err != nil {
					slog.Error("graph.materialize: persist durable projection", "job_id", jobID, "error", err)
				}
			}
		}
	}()
}

// materializeStatus implements graph.materialize.status {job_id} → {status,
// stages, artifacts} — the poll fallback for a client whose EventSource
// dropped or never connected.
func (s *Server) materializeStatus(params map[string]any) (any, *rpcError) {
	jobID, _ := params["job_id"].(string)
	if jobID == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.status: missing 'job_id'"}
	}
	s.materializeMu.Lock()
	state, ok := s.materializeJobs[jobID]
	s.materializeMu.Unlock()
	if !ok {
		if record, found := s.persistedMaterialization(jobID); found {
			return materializationStatusResponse(record), nil
		}
		return nil, &rpcError{Code: codeNotFound, Message: "graph.materialize.status: unknown job_id: " + jobID}
	}

	if record, found := s.persistedMaterialization(jobID); found {
		return materializationStatusResponse(record), nil
	}

	stages, status, artifacts := state.snapshot()
	if artifacts == nil {
		artifacts = []materializeArtifact{}
	}
	receiptIDs := state.receiptIDsSnapshot()
	if receiptIDs == nil {
		receiptIDs = []string{}
	}
	return map[string]any{
		"status":      status,
		"stages":      stages,
		"artifacts":   artifacts,
		"receipt_ids": receiptIDs,
		"session_id":  state.sessionID,
	}, nil
}

func (s *Server) persistedMaterialization(jobID string) (materializationstatus.Record, bool) {
	if s.materializeProjection == nil || s.materializeApplication == "" {
		return materializationstatus.Record{}, false
	}
	record, found, err := s.materializeProjection.Get(
		context.Background(), s.materializeApplication, jobID,
	)
	return record, found && err == nil
}

func materializationStatusResponse(record materializationstatus.Record) map[string]any {
	stages := make([]any, len(record.Stages))
	for i, stage := range record.Stages {
		stages[i] = map[string]any{"id": stage.ID, "title": stage.Title, "status": stage.Status}
	}
	artifacts := make([]any, len(record.Artifacts))
	for i, artifact := range record.Artifacts {
		artifacts[i] = map[string]any{
			"kind": artifact.Kind, "title": artifact.Title, "handle": artifact.Handle,
		}
	}
	return map[string]any{
		"status": record.Status, "stages": stages, "artifacts": artifacts,
		"receipt_ids": append([]string(nil), record.ReceiptIDs...),
		"session_id":  record.SessionID,
	}
}

// materializeCancel implements graph.materialize.cancel {job_id}.
func (s *Server) materializeCancel(ctx context.Context, params map[string]any) (any, *rpcError) {
	jobID, _ := params["job_id"].(string)
	if jobID == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.cancel: missing 'job_id'"}
	}
	if err := s.materializeSched.Cancel(ctx, jobID); err != nil {
		return nil, &rpcError{Code: codeNotFound, Message: "graph.materialize.cancel: " + err.Error()}
	}
	return map[string]any{"ok": true}, nil
}

// materializeAnswer implements graph.materialize.answer {job_id, answer},
// resuming a job parked in awaiting_input by a mid-run clarification.
//
// Full support (host.RequestClarification's poll loop) requires a SQLite
// jobs.JobStore write-through — see jobs.Scheduler.Awaiting's doc comment:
// "the DB row must already have been flipped to awaiting_input ... before
// calling this". graph.materialize.start runs jobs on an in-memory-only
// scheduler (materialize jobs are short-lived deterministic story drives —
// see WithMaterializeRoot's doc comment), so no clarification storage layer
// is wired. The deterministic pilot story (stories/materialize-work-item)
// never requests one. This method therefore validates the RPC shape and the
// job's status faithfully, but reports a clear server error instead of
// silently no-op'ing when a job genuinely is awaiting_input — a future slice
// that binds an LLM-backed (or otherwise clarification-issuing) story to
// materialize: should wire a JobStore-backed scheduler here instead.
func (s *Server) materializeAnswer(params map[string]any) (any, *rpcError) {
	jobID, _ := params["job_id"].(string)
	if jobID == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.answer: missing 'job_id'"}
	}
	if _, present := params["answer"]; !present {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.answer: missing 'answer'"}
	}
	job, ok := s.materializeSched.Get(jobID)
	if !ok {
		return nil, &rpcError{Code: codeNotFound, Message: "graph.materialize.answer: unknown job_id: " + jobID}
	}
	if job.Status != jobs.JobAwaitingInput {
		return nil, &rpcError{Code: codeServerError, Message: fmt.Sprintf("graph.materialize.answer: job %s is not awaiting input (status=%s)", jobID, job.Status)}
	}
	return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.answer: this server has no clarification storage wired for materialize jobs (in-memory scheduler only)"}
}
