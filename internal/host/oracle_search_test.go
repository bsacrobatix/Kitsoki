package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"kitsoki/internal/embed"
)

func TestOracleSearchHandler(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()

	// Create 3 small markdown files.
	files := map[string]string{
		"alpha.md": "## Alpha\nThis is about apples and fruits.\n",
		"beta.md":  "## Beta\nThis is about bananas and tropical plants.\n",
		"gamma.md": "## Gamma\nThis is about grapes and vineyards.\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	fakeEmbedder := embed.NewFakeEmbedder(8)
	store := embed.NewStore(storeDir)
	handler := NewOracleSearchHandler("test-model", dir, fakeEmbedder, store)

	args := map[string]any{
		"query":  "fruit",
		"corpus": "*.md",
		"top_k":  3,
	}

	res, err := handler(context.Background(), args)
	if err != nil {
		t.Fatalf("handler returned Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("handler returned Result.Error: %s", res.Error)
	}

	hitsRaw, ok := res.Data["hits"]
	if !ok {
		t.Fatal("result missing 'hits' key")
	}
	hits, ok := hitsRaw.([]map[string]any)
	if !ok {
		t.Fatalf("hits has unexpected type %T", hitsRaw)
	}
	if len(hits) < 1 {
		t.Fatal("expected at least 1 hit, got 0")
	}
	for i, h := range hits {
		if _, ok := h["path"]; !ok {
			t.Errorf("hit[%d] missing 'path'", i)
		}
		if _, ok := h["chunk_id"]; !ok {
			t.Errorf("hit[%d] missing 'chunk_id'", i)
		}
		if _, ok := h["text"]; !ok {
			t.Errorf("hit[%d] missing 'text'", i)
		}
		if _, ok := h["score"]; !ok {
			t.Errorf("hit[%d] missing 'score'", i)
		}
	}

	// top should match first hit.
	top, ok := res.Data["top"]
	if !ok {
		t.Fatal("result missing 'top' key")
	}
	if top == nil {
		t.Fatal("expected non-nil top")
	}
}

func TestOracleSearchMinScore(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc.md"), []byte("## Doc\nSome content.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeEmbedder := embed.NewFakeEmbedder(8)
	store := embed.NewStore(storeDir)
	handler := NewOracleSearchHandler("test-model", dir, fakeEmbedder, store)

	args := map[string]any{
		"query":     "anything",
		"corpus":    "*.md",
		"min_score": 2.0, // impossible for cosine similarity (max is 1.0)
	}

	res, err := handler(context.Background(), args)
	if err != nil {
		t.Fatalf("handler returned Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("handler returned Result.Error: %s", res.Error)
	}

	hitsRaw := res.Data["hits"]
	hits, _ := hitsRaw.([]map[string]any)
	if len(hits) != 0 {
		t.Errorf("expected 0 hits with min_score=2.0, got %d", len(hits))
	}
}

func TestOracleSearchEmptyCorpus(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()
	// No files in dir — glob matches nothing.

	fakeEmbedder := embed.NewFakeEmbedder(8)
	store := embed.NewStore(storeDir)
	handler := NewOracleSearchHandler("test-model", dir, fakeEmbedder, store)

	args := map[string]any{
		"query":  "anything",
		"corpus": "*.md",
	}

	res, err := handler(context.Background(), args)
	if err != nil {
		t.Fatalf("handler returned Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("handler returned Result.Error: %s", res.Error)
	}

	hitsRaw := res.Data["hits"]
	hits, _ := hitsRaw.([]map[string]any)
	if len(hits) != 0 {
		t.Errorf("expected 0 hits for empty corpus, got %d", len(hits))
	}
}
