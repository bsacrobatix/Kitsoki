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
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"kitsoki/internal/app"
	"kitsoki/internal/graph"
	"kitsoki/internal/jobs"
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
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Path  string `json:"path"`
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
	mu            sync.Mutex
	nodeID        string
	stages        []materialize.Stage
	gates         []string
	artifactKind  string
	artifactTitle string
	status        string // running | awaiting_input | done | failed | cancelled
	artifacts     []materializeArtifact
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

func (st *materializeJobState) artifactFor(path string) materializeArtifact {
	st.mu.Lock()
	defer st.mu.Unlock()
	return materializeArtifact{Kind: st.artifactKind, Title: st.artifactTitle, Path: path}
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
	default:
		return nil, nil, false
	}
}

// materializeStart implements graph.materialize.start {catalog, node_id,
// params} → {job_id, stages: [{id, title}]}. Gates are validated
// server-side by internal/materialize.Start; an unmet gate rejects with the
// unmet field list both in the error message and (machine-readable) in the
// rpcError's Data as a JSON array.
func (s *Server) materializeStart(ctx context.Context, params map[string]any) (any, *rpcError) {
	catalogPath, _ := params["catalog"].(string)
	if catalogPath == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.start: missing 'catalog'"}
	}
	nodeID, _ := params["node_id"].(string)
	if nodeID == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.start: missing 'node_id'"}
	}
	paramArgs, _ := params["params"].(map[string]any)

	repoRoot := s.materializeRoot
	if repoRoot == "" {
		repoRoot = "."
	}

	cat, err := graph.LoadCatalog(catalogPath)
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.start: " + err.Error()}
	}
	node, ok := cat.Nodes[graph.NodeID(nodeID)]
	if !ok {
		return nil, &rpcError{Code: codeServerError, Message: fmt.Sprintf("graph.materialize.start: node %q not found in catalog", nodeID)}
	}
	binding, err := materialize.ResolveBinding(cat, node)
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.materialize.start: " + err.Error()}
	}
	eff, _ := cat.Registry.Effective(node.TypeID)

	jobID, stages, err := materialize.Start(ctx, s.materializeSched, materialize.Request{
		CatalogPath: catalogPath,
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

	artifactKind := "doc"
	if eff.Artifact != nil && eff.Artifact.Presentation != "" {
		artifactKind = eff.Artifact.Presentation
	}

	state := &materializeJobState{
		nodeID:        nodeID,
		stages:        append([]materialize.Stage(nil), stages...),
		gates:         binding.Gates,
		artifactKind:  artifactKind,
		artifactTitle: fmt.Sprintf("%s artifact", eff.ID),
		status:        string(jobs.JobRunning),
	}
	s.materializeMu.Lock()
	s.materializeJobs[jobID] = state
	s.materializeMu.Unlock()

	// Subscribe synchronously, before returning, so a .status poll can never
	// see less progress than a live SSE subscriber that connected late would
	// have (see internal/materialize's doc comment on this exact race).
	s.trackMaterializeJob(jobID, state)

	// Best-effort room titles: the story's own app.yaml state descriptions
	// (app.State.Description), falling back to the room id. A load failure
	// here (unlikely — materialize.Start just loaded the same file
	// successfully) degrades to id-only titles rather than failing the RPC.
	def, _ := app.Load(filepath.Join(repoRoot, binding.Story, "app.yaml"))
	stagesOut := make([]materializeStageWire, len(stages))
	for i, st := range stages {
		title := st.ID
		if def != nil {
			if roomState, ok := def.States[st.ID]; ok && roomState.Description != "" {
				title = roomState.Description
			}
		}
		stagesOut[i] = materializeStageWire{ID: st.ID, Title: title}
	}

	return map[string]any{
		"job_id": jobID,
		"stages": stagesOut,
	}, nil
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
			switch ev.Status {
			case jobs.JobRunning:
				state.setStatus(string(jobs.JobRunning))
			case jobs.JobAwaitingInput:
				state.setStatus(string(jobs.JobAwaitingInput))
			case jobs.JobDone:
				if a, ok := materializeArtifactFromResult(state, ev); ok {
					state.addArtifact(a)
				}
				state.setStatus(string(jobs.JobDone))
			case jobs.JobFailed:
				state.setStatus(string(jobs.JobFailed))
			case jobs.JobCancelled:
				state.setStatus(string(jobs.JobCancelled))
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
		return nil, &rpcError{Code: codeNotFound, Message: "graph.materialize.status: unknown job_id: " + jobID}
	}

	stages, status, artifacts := state.snapshot()
	if artifacts == nil {
		artifacts = []materializeArtifact{}
	}
	return map[string]any{
		"status":    status,
		"stages":    stages,
		"artifacts": artifacts,
	}, nil
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
