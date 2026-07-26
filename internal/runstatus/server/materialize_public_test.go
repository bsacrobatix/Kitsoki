package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
	"kitsoki/internal/materialize"
)

func TestTypedMaterializeStatusAndSSEExposeHandlesWithoutPaths(t *testing.T) {
	sched := jobs.NewInMemoryScheduler()
	jobID, err := sched.Submit(context.Background(), jobs.JobSpec{
		SessionID: app.SessionID("typed-session"),
		Kind:      "graph.materialize",
		Handler: func(context.Context, map[string]any) (host.Result, error) {
			return host.Result{Data: map[string]any{
				"artifact_handles": []string{"flow-evidence:" + strings.Repeat("a", 64)},
				"receipt_ids":      []string{"ar_" + strings.Repeat("1", 32)},
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sched.WaitIdle(waitCtx); err != nil {
		t.Fatal(err)
	}

	handle := "flow-evidence:" + strings.Repeat("a", 64)
	receipt := "ar_" + strings.Repeat("1", 32)
	state := &materializeJobState{
		nodeID: "app-one",
		stages: []materialize.Stage{{ID: "record", Status: "complete"}},
		status: string(jobs.JobDone),
		artifacts: []materializeArtifact{{
			Kind: "evidence", Title: "Evidence", Handle: handle,
		}},
		receiptIDs: []string{receipt},
	}
	server := &Server{
		materializeSched: sched,
		materializeJobs:  map[jobs.JobID]*materializeJobState{jobID: state},
	}

	status, rerr := server.materializeStatus(map[string]any{"job_id": string(jobID)})
	if rerr != nil {
		t.Fatal(rerr)
	}
	statusJSON, _ := json.Marshal(status)
	assertTypedPublicMaterializePayload(t, string(statusJSON), handle, receipt)

	request := httptest.NewRequest("GET", "/rpc/materialize-stream?job="+string(jobID), nil)
	response := httptest.NewRecorder()
	server.handleMaterializeStream(response, request)
	assertTypedPublicMaterializePayload(t, response.Body.String(), handle, receipt)
}

func assertTypedPublicMaterializePayload(t *testing.T, payload, handle, receipt string) {
	t.Helper()
	if !strings.Contains(payload, handle) {
		t.Fatalf("payload omitted opaque handle: %s", payload)
	}
	if receipt != "" && !strings.Contains(payload, receipt) {
		t.Fatalf("payload omitted canonical receipt: %s", payload)
	}
	for _, forbidden := range []string{
		`"path":`,
		"catalog_path",
		"story_path",
		"artifact_path",
		"file://",
		"/private/",
		"/tmp/",
		"../",
		"command",
		"script",
		"url",
	} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("typed public payload contains forbidden authority %q: %s", forbidden, payload)
		}
	}
}
