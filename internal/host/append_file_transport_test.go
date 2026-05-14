package host_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/host"
)

func TestAppendFileTransport_RegisteredAsBuiltin(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)
	if _, ok := r.Get("host.append_to_file"); !ok {
		t.Fatal("host.append_to_file missing")
	}
}

func TestAppendFileTransport_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bug.md")
	if err := os.WriteFile(path, []byte(sampleBug), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := host.AppendFileTransportHandler(context.Background(), map[string]any{
		"thread": path,
		"body":   "Reproduction artifact: see attached log.",
		"author": "llm-judge",
		"title":  "Reproduction",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["ok"] != true {
		t.Fatalf("ok: %v", res.Data["ok"])
	}
	raw, _ := os.ReadFile(path)
	s := string(raw)
	if !strings.Contains(s, "## Comment ") {
		t.Fatalf("missing comment heading: %s", s)
	}
	if !strings.Contains(s, "by llm-judge") {
		t.Fatalf("missing author tag: %s", s)
	}
	if !strings.Contains(s, "### Reproduction") {
		t.Fatalf("title not rendered: %s", s)
	}
	if !strings.Contains(s, "Reproduction artifact: see attached log.") {
		t.Fatalf("body missing: %s", s)
	}
	// Original frontmatter and prose should still be there.
	if !strings.Contains(s, "Esc in foyer hangs the TUI") {
		t.Fatalf("original title lost: %s", s)
	}
}

func TestAppendFileTransport_BootstrapsNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "newbug.md")

	res, err := host.AppendFileTransportHandler(context.Background(), map[string]any{
		"thread":   path,
		"body":     "First comment on a fresh thread.",
		"author":   "kitsoki",
		"phase_id": "phase_1",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}

	// The new file must be parseable as a bug file.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(raw)
	if !strings.HasPrefix(s, "---\n") {
		t.Fatalf("expected frontmatter prefix: %s", s)
	}
	if !strings.Contains(s, "First comment on a fresh thread.") {
		t.Fatalf("body missing: %s", s)
	}
	if !strings.Contains(s, "_phase: phase_1_") {
		t.Fatalf("phase_id missing: %s", s)
	}
}

func TestAppendFileTransport_RequiresThread(t *testing.T) {
	res, err := host.AppendFileTransportHandler(context.Background(), map[string]any{
		"body": "x",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error")
	}
}

func TestAppendFileTransport_RequiresBody(t *testing.T) {
	dir := t.TempDir()
	res, err := host.AppendFileTransportHandler(context.Background(), map[string]any{
		"thread": filepath.Join(dir, "x.md"),
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error")
	}
}

func TestAppendFileTransport_RoundTripsViaTicketGet(t *testing.T) {
	root := seedTicketsRoot(t, map[string]string{
		"2026-05-14T10-tui-hangs.md": sampleBug,
	})
	target := filepath.Join(root, "issues", "bugs", "2026-05-14T10-tui-hangs.md")

	// Append via the transport.
	res, err := host.AppendFileTransportHandler(context.Background(), map[string]any{
		"thread": target,
		"body":   "Round-trip body.",
		"author": "kitsoki",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("append: %v / %s", err, res.Error)
	}

	// Read back via the ticket handler.
	got, err := host.LocalFilesTicketHandler(context.Background(), map[string]any{
		"op":   "get",
		"root": root,
		"id":   "2026-05-14T10-tui-hangs",
	})
	if err != nil || got.Error != "" {
		t.Fatalf("get: %v / %s", err, got.Error)
	}
	cm, _ := got.Data["comments"].([]map[string]any)
	if len(cm) != 1 {
		t.Fatalf("expected 1 comment via ticket.get, got %d", len(cm))
	}
	if !strings.Contains(cm[0]["body"].(string), "Round-trip body.") {
		t.Fatalf("body via ticket: %v", cm[0]["body"])
	}
}
