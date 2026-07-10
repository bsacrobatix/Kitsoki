package server

// materialize_stream.go — the GET /rpc/materialize-stream SSE endpoint
// (node-artifact-materialization plan slice 4). Cloned from turn_stream.go's
// pattern: subscribe to a job's events and translate them into
// text/event-stream frames in real time, so the portal sees stage pills
// advance live instead of polling graph.materialize.status.
//
// Protocol:
//
//	GET /rpc/materialize-stream?job=<job_id>
//	  response: text/event-stream
//
//	Events:
//	  data: {"type":"gate","gate_id":"…","passed":true}
//	  data: {"type":"stage","stage_id":"…","status":"in-progress"|"complete"|"failed"}
//	  data: {"type":"artifact","kind":"…","title":"…","path":"…"}
//	  data: {"type":"status","status":"in-progress"|"awaiting-input"|"complete"|"failed"|"cancelled"}
//	  data: {"type":"error","message":"…"}
//	  data: {"type":"done"}
//
// gate frames fire once, immediately on connect: every gate named by the
// type's materialize.gates already passed validation before
// graph.materialize.start allowed the job to be submitted (see
// materialize.Start's gate-check-before-submit contract), so there is
// nothing to wait on — a late-connecting subscriber still gets the full
// picture up front instead of missing a frame that only ever fires once, at
// a moment before it connected.
//
// A subscriber that connects to an already-terminal job (fast/deterministic
// pilot stories routinely finish before a browser's EventSource opens — see
// internal/materialize's package doc on the Subscribe-after-Start race)
// receives Subscribe's single replayed terminal JobEvent, so it still gets a
// status/artifact/done sequence, but not the intermediate per-stage frames
// that preceded it. graph.materialize.status is the intended fallback for
// exactly that case (per the plan), not something this endpoint works around.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"kitsoki/internal/jobs"
	"kitsoki/internal/materialize"
)

// materializeStreamFrame is one SSE data payload for /rpc/materialize-stream.
type materializeStreamFrame struct {
	Type string `json:"type"` // "gate" | "stage" | "artifact" | "status" | "error" | "done" | "ping"

	// gate
	GateID string `json:"gate_id,omitempty"`
	Passed bool   `json:"passed,omitempty"`

	// stage
	StageID string `json:"stage_id,omitempty"`

	// artifact
	Kind  string `json:"kind,omitempty"`
	Title string `json:"title,omitempty"`
	Path  string `json:"path,omitempty"`

	// status (also reused as the stage frame's status field)
	Status string `json:"status,omitempty"`

	// error
	Message string `json:"message,omitempty"`
}

// jobStatusToWire maps a jobs.JobStatus to the plan's node-pill vocabulary:
// running -> in-progress, awaiting_input -> awaiting-input, done -> complete,
// failed/cancelled pass through unchanged.
func jobStatusToWire(status jobs.JobStatus) string {
	switch status {
	case jobs.JobRunning:
		return "in-progress"
	case jobs.JobAwaitingInput:
		return "awaiting-input"
	case jobs.JobDone:
		return "complete"
	default:
		return string(status) // "failed" | "cancelled"
	}
}

func (s *Server) handleMaterializeStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jobID := r.URL.Query().Get("job")
	if jobID == "" {
		http.Error(w, `materialize-stream: missing "job" query parameter`, http.StatusBadRequest)
		return
	}

	s.materializeMu.Lock()
	state, ok := s.materializeJobs[jobs.JobID(jobID)]
	s.materializeMu.Unlock()
	if !ok {
		http.Error(w, "materialize-stream: unknown job: "+jobID, http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	emit := func(f materializeStreamFrame) {
		b, err := json.Marshal(f)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	for _, gateID := range state.gatesSnapshot() {
		emit(materializeStreamFrame{Type: "gate", GateID: gateID, Passed: true})
	}

	ch, unsub := s.materializeSched.Subscribe(jobs.JobID(jobID))
	defer unsub()

	// Idle-connection heartbeat, matching handleTurnStream: a deterministic
	// story can run to completion between two heartbeat frames with nothing
	// else on the wire, and a dev proxy or browser can drop a silent SSE
	// connection before the terminal "done" frame arrives.
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			emit(materializeStreamFrame{Type: "ping"})
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if se, ok := ev.Progress.(materialize.StageEvent); ok {
				emit(materializeStreamFrame{Type: "stage", StageID: se.Stage, Status: se.Status})
			}
			switch ev.Status {
			case jobs.JobRunning, jobs.JobAwaitingInput:
				emit(materializeStreamFrame{Type: "status", Status: jobStatusToWire(ev.Status)})
			case jobs.JobDone:
				if a, ok := materializeArtifactFromResult(state, ev); ok {
					emit(materializeStreamFrame{Type: "artifact", Kind: a.Kind, Title: a.Title, Path: a.Path})
				}
				emit(materializeStreamFrame{Type: "status", Status: jobStatusToWire(ev.Status)})
				emit(materializeStreamFrame{Type: "done"})
				return
			case jobs.JobFailed:
				emit(materializeStreamFrame{Type: "error", Message: ev.Error})
				emit(materializeStreamFrame{Type: "status", Status: jobStatusToWire(ev.Status)})
				emit(materializeStreamFrame{Type: "done"})
				return
			case jobs.JobCancelled:
				emit(materializeStreamFrame{Type: "status", Status: jobStatusToWire(ev.Status)})
				emit(materializeStreamFrame{Type: "done"})
				return
			}
		}
	}
}
