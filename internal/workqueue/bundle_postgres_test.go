package workqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/dbruntime/pgtest"
)

func TestPostgresBundleValidatorImportsAndResolvesSharedEvidence(t *testing.T) {
	db := pgtest.Open(t)
	root := t.TempDir()
	raw := []byte("shared portable bundle")
	if err := os.WriteFile(
		filepath.Join(root, "job.bundle"), raw, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	first, err := NewPostgresBundleValidator(db, root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := first.ValidateBundle(
		context.Background(), "job.bundle", digest,
	)
	if err != nil || !strings.HasPrefix(ref, postgresBundleRefPrefix) {
		t.Fatalf("validate = %q, %v", ref, err)
	}
	// A second daemon with a different local intake root resolves the same
	// canonical retained bundle from PostgreSQL.
	second, err := NewPostgresBundleValidator(db, t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	got, err := second.Bundle(context.Background(), ref)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("shared bundle = %q, %v", got, err)
	}
}
