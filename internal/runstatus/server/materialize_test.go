package server_test

// materialize_test.go — httptest flow coverage for the graph.materialize.*
// RPC family and /rpc/materialize-stream SSE endpoint (node-artifact-
// materialization plan slice 4). Drives the actual slice-2 pilot story in
// the sibling POG checkout (stories/materialize-work-item) end to end
// through start + stream, following server_test.go's rpcCall /
// rpcCallExpectError conventions and turn_stream_test.go's SSE-frame-reading
// pattern.

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/runstatus/server"
)

// materializeCatalogCopy copies testdata/materialize-catalog.yaml into a
// fresh t.TempDir() and returns the copy's path. graph.materialize.start's
// write-back (internal/materialize/writeback.go, slice 6) edits the catalog
// file in place — a flow test that drives a job to completion must run
// against a disposable copy, not the checked-in fixture, or every run would
// leave it mutated with the previous run's job id/timestamp.
func materializeCatalogCopy(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/materialize-catalog.yaml")
	require.NoError(t, err)
	dst := filepath.Join(t.TempDir(), "materialize-catalog.yaml")
	require.NoError(t, os.WriteFile(dst, raw, 0o644))
	return dst
}

// pogRepoRoot locates the POG checkout that carries the node-artifact-
// materialization plan's pilot story (stories/materialize-work-item) and its
// bound work-item type_registry entry, via the POG_REPO_ROOT env var. This
// flow test is only meaningful against the real story; an unset var or
// missing checkout (e.g. a bare kitsoki CI runner with no POG sibling) skips
// rather than fails — mirroring internal/materialize/spike_test.go's
// convention.
func pogRepoRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("POG_REPO_ROOT")
	if root == "" {
		t.Skip("requires POG_REPO_ROOT pointing at a POG checkout with stories/materialize-work-item")
	}
	if _, err := os.Stat(root + "/stories/materialize-work-item/app.yaml"); err != nil {
		t.Skipf("requires the POG checkout's pilot story at %s (not found: %v)", root, err)
	}
	return root
}

func newMaterializeServer(t *testing.T) *httptest.Server {
	t.Helper()
	root := pogRepoRoot(t)
	ts := httptest.NewServer(server.NewMulti(newStubProvider(), server.WithMaterializeRoot(root)).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// materializeStreamFrame mirrors the unexported server-side type for test
// decoding (same convention as turnStreamFrame in turn_stream_test.go).
type materializeStreamFrame struct {
	Type    string `json:"type"`
	GateID  string `json:"gate_id"`
	Passed  bool   `json:"passed"`
	StageID string `json:"stage_id"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

func readMaterializeStreamFrames(t *testing.T, ts *httptest.Server, jobID string) []materializeStreamFrame {
	t.Helper()
	resp, err := http.Get(ts.URL + "/rpc/materialize-stream?job=" + jobID)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "expected 200 on materialize-stream")
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	var frames []materializeStreamFrame
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")
		var f materializeStreamFrame
		require.NoError(t, json.Unmarshal([]byte(raw), &f), "unmarshal frame: %s", raw)
		frames = append(frames, f)
		if f.Type == "done" {
			break
		}
	}
	return frames
}

// TestMaterialize_StartAndStream_PilotStory drives graph.materialize.start
// against a fixture catalog bound to the real pilot story, then reads the
// full /rpc/materialize-stream frame sequence for the job it starts,
// asserting the gate/stage/artifact/status/done frames the plan specifies —
// and finally cross-checks graph.materialize.status (the poll fallback)
// against what the stream showed.
func TestMaterialize_StartAndStream_PilotStory(t *testing.T) {
	ts := newMaterializeServer(t)
	root := pogRepoRoot(t)
	catalogPath := materializeCatalogCopy(t)
	// The real pilot story writes its artifact under RepoRoot/.artifacts/
	// (RepoRoot here is the sibling POG checkout, per WithMaterializeRoot) —
	// clean up that one write, not the whole POG .artifacts/ dir, which
	// carries unrelated checked-in demo assets.
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(root, ".artifacts", "wi-ready")) })

	var start struct {
		JobID  string `json:"job_id"`
		Stages []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"stages"`
	}
	rpcCall(t, ts, "graph.materialize.start", map[string]any{
		"catalog": catalogPath,
		"node_id": "wi-ready",
		"params":  map[string]any{"depth": 3, "audience": "public"},
	}, &start)

	require.NotEmpty(t, start.JobID)
	require.Len(t, start.Stages, 4)
	wantStageIDs := []string{"gather", "draft", "verify", "done"}
	for i, want := range wantStageIDs {
		assert.Equal(t, want, start.Stages[i].ID)
		assert.NotEmpty(t, start.Stages[i].Title)
	}
	// The pilot story's rooms carry description: text distinct from the bare
	// room id, proving the title lookup found app.State.Description.
	assert.Equal(t, "Gathering context", start.Stages[0].Title)

	frames := readMaterializeStreamFrames(t, ts, start.JobID)
	require.NotEmpty(t, frames)

	var gateFrames, stageFrames []materializeStreamFrame
	var artifactFrame *materializeStreamFrame
	var sawComplete, sawDone bool
	for i := range frames {
		f := frames[i]
		switch f.Type {
		case "gate":
			gateFrames = append(gateFrames, f)
		case "stage":
			stageFrames = append(stageFrames, f)
		case "artifact":
			af := f
			artifactFrame = &af
		case "status":
			if f.Status == "complete" {
				sawComplete = true
			}
		case "done":
			sawDone = true
		case "error":
			t.Fatalf("unexpected error frame: %s", f.Message)
		}
	}

	require.Len(t, gateFrames, 2, "expected one gate frame per materialize.gates entry")
	gateIDs := []string{gateFrames[0].GateID, gateFrames[1].GateID}
	assert.ElementsMatch(t, []string{"gate", "owner"}, gateIDs)
	for _, g := range gateFrames {
		assert.True(t, g.Passed)
	}

	// Stage frames are best-effort: the pilot story is deterministic and can
	// finish before this test's GET reaches the stream endpoint (the
	// documented Subscribe-after-Start race — see materialize_stream.go's
	// package doc), in which case Subscribe replays only the single terminal
	// JobEvent and no per-room frames precede it. When stage frames DO show
	// up, they must be valid room ids in the pilot story's order.
	wantOrder := map[string]int{"gather": 0, "draft": 1, "verify": 2, "done": 3}
	last := -1
	for _, sf := range stageFrames {
		idx, ok := wantOrder[sf.StageID]
		require.True(t, ok, "unexpected stage id %q", sf.StageID)
		assert.GreaterOrEqual(t, idx, last, "stage frames must not regress: got %q after index %d", sf.StageID, last)
		last = idx
	}

	// The terminal artifact/status/done sequence always fires, regardless of
	// which race the stream landed in above.
	require.NotNil(t, artifactFrame, "expected an artifact frame")
	assert.Equal(t, "document", artifactFrame.Kind)
	assert.Equal(t, ".artifacts/wi-ready/brief.md", artifactFrame.Path)

	assert.True(t, sawComplete, "expected a status=complete frame")
	assert.True(t, sawDone, "expected a terminal done frame")

	// The poll fallback should agree with what the stream showed. The
	// stream already drove the job to completion; the server's own
	// bookkeeping goroutine (trackMaterializeJob) updates independently, so
	// poll briefly rather than assuming it has already caught up.
	var status struct {
		Status string `json:"status"`
		Stages []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"stages"`
		Artifacts []struct {
			Path string `json:"path"`
		} `json:"artifacts"`
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		rpcCall(t, ts, "graph.materialize.status", map[string]any{"job_id": start.JobID}, &status)
		if status.Status == "done" || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, "done", status.Status)

	require.Len(t, status.Stages, 4)
	for _, st := range status.Stages {
		assert.Equal(t, "complete", st.Status, "stage %s", st.ID)
	}
	require.Len(t, status.Artifacts, 1)
	assert.Equal(t, ".artifacts/wi-ready/brief.md", status.Artifacts[0].Path)

	// Write-back (slice 6): the job handler wrote the artifact's content to
	// disk under the POG repo root and persisted evidence: + materialization:
	// onto the node in the (copied) catalog — the durable truth the portal's
	// reload-on-done then reads. The handler writes this synchronously before
	// returning its Result, but the job is marked done by the scheduler right
	// after, so poll briefly rather than assume the write already landed.
	var artifactContent []byte
	var artifactErr error
	deadline = time.Now().Add(2 * time.Second)
	for {
		artifactContent, artifactErr = os.ReadFile(filepath.Join(root, ".artifacts", "wi-ready", "brief.md"))
		if artifactErr == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NoError(t, artifactErr, "materialize job should have written the artifact to disk")
	assert.NotEmpty(t, artifactContent)

	catalogRaw, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	catalogText := string(catalogRaw)
	assert.Contains(t, catalogText, "path: .artifacts/wi-ready/brief.md", "evidence entry not written back")
	assert.Contains(t, catalogText, "materialization:", "materialization: block not written back")
	assert.Contains(t, catalogText, "job_id: \""+start.JobID+"\"")
	assert.Contains(t, catalogText, "status: complete")
}

// TestMaterialize_Start_RejectsUnmetGates confirms the "rejects with the
// unmet field list" contract: a node missing gate/owner never gets a job.
func TestMaterialize_Start_RejectsUnmetGates(t *testing.T) {
	ts := newMaterializeServer(t)

	code, msg := rpcCallExpectError(t, ts, "graph.materialize.start", map[string]any{
		"catalog": "testdata/materialize-catalog.yaml",
		"node_id": "wi-not-ready",
	})
	assert.NotEqual(t, 0, code)
	assert.Contains(t, msg, "gate")
	assert.Contains(t, msg, "owner")
}

func TestMaterialize_Status_UnknownJob(t *testing.T) {
	ts := newMaterializeServer(t)

	code, msg := rpcCallExpectError(t, ts, "graph.materialize.status", map[string]any{"job_id": "nope"})
	assert.NotEqual(t, 0, code)
	assert.Contains(t, msg, "unknown job_id")
}

func TestMaterialize_Stream_UnknownJob404s(t *testing.T) {
	ts := newMaterializeServer(t)

	resp, err := http.Get(ts.URL + "/rpc/materialize-stream?job=nope")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestMaterialize_Cancel_UnknownJob confirms the cancel RPC reports an error
// (not a silent success) for an id the scheduler never issued.
func TestMaterialize_Cancel_UnknownJob(t *testing.T) {
	ts := newMaterializeServer(t)

	code, _ := rpcCallExpectError(t, ts, "graph.materialize.cancel", map[string]any{"job_id": "nope"})
	assert.NotEqual(t, 0, code)
}
