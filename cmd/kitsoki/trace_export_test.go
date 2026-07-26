package main

// trace_export_test.go covers the `kitsoki trace export` CLI shape against
// the default SQLite backend: exact-id export to a file (validated by the
// canonical JSONL oracle), prefix resolution via --app, stdout output, and
// the not-found error path. The byte-compatibility determinism proof against
// the Postgres backend lives in internal/store/trace_export_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/app"
	"kitsoki/internal/store"
)

// seedTraceExportSession creates a sqlite session db with one session and a
// few events; returns the db path and session id.
func seedTraceExportSession(t *testing.T) (string, app.SessionID) {
	t.Helper()
	resetDBBackend(t)
	t.Setenv(envDBBackend, "")
	dbBackendFlag = ""

	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	s, err := openSessionStore(dbPath)
	if err != nil {
		t.Fatalf("openSessionStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	def := &app.AppDef{}
	def.App.ID = "export-app"
	sid, err := s.CreateSession(context.Background(), def)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	events := []store.Event{
		{Turn: 1, Kind: store.TurnStarted, Payload: json.RawMessage(`{"input":"hello"}`)},
		{Turn: 1, Kind: store.TurnEnded, Payload: json.RawMessage(`{"outcome":"transitioned","to":"hall"}`)},
	}
	if err := s.AppendEvents(sid, events); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	return dbPath, sid
}

// runTraceExport executes `trace export` with args, returning stdout.
func runTraceExport(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := traceCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"export"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestTraceExportCmd_WritesValidTraceFile(t *testing.T) {
	dbPath, sid := seedTraceExportSession(t)
	outPath := filepath.Join(t.TempDir(), "exported.jsonl")

	stdout, err := runTraceExport(t, "--session", string(sid), "--db", dbPath, "--out", outPath)
	if err != nil {
		t.Fatalf("trace export: %v", err)
	}
	if !strings.Contains(stdout, "2 events") {
		t.Fatalf("stdout %q does not report the event count", stdout)
	}
	if err := store.ValidateJSONL(outPath); err != nil {
		t.Fatalf("exported trace fails the canonical JSONL oracle: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read exported trace: %v", err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) != 3 { // header + 2 events
		t.Fatalf("got %d lines, want 3 (header + 2 events)", len(lines))
	}
	if !bytes.Contains(lines[0], []byte(`"kind":"session.header"`)) {
		t.Fatalf("line 1 is not the session.header: %s", lines[0])
	}
}

func TestTraceExportCmd_PrefixResolutionAndStdout(t *testing.T) {
	dbPath, sid := seedTraceExportSession(t)

	stdout, err := runTraceExport(t, "--session", string(sid)[:8], "--app", "export-app", "--db", dbPath)
	if err != nil {
		t.Fatalf("trace export by prefix: %v", err)
	}
	// Stdout carries the raw trace: header first, then events.
	if !strings.HasPrefix(stdout, `{"kind":"session.header"`) {
		t.Fatalf("stdout does not start with the session.header: %q", stdout)
	}
	if !strings.Contains(stdout, `"kind":"turn.end"`) {
		t.Fatalf("stdout missing the turn.end event: %q", stdout)
	}
}

func TestTraceExportCmd_UnknownSessionFails(t *testing.T) {
	dbPath, _ := seedTraceExportSession(t)
	_, err := runTraceExport(t, "--session", "no-such-session", "--db", dbPath)
	if err == nil {
		t.Fatal("unknown session accepted; want error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error %q does not say not found", err)
	}
}
