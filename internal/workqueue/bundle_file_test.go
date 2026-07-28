package workqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileBundleValidatorVerifiesRetainedRegularFile(t *testing.T) {
	root := t.TempDir()
	raw := []byte("portable bundle")
	path := filepath.Join(root, "job.bundle")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	validator, err := NewFileBundleValidator(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.ValidateBundle(
		context.Background(), "job.bundle", digest,
	); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"missing.bundle", "../escape", path} {
		if _, err := validator.ValidateBundle(
			context.Background(), ref, digest,
		); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe ref %q = %v", ref, err)
		}
	}
	if _, err := validator.ValidateBundle(
		context.Background(), "job.bundle",
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("digest mismatch = %v", err)
	}
}

func TestFileBundleValidatorRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.bundle")
	if err := os.WriteFile(target, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.bundle")); err != nil {
		t.Fatal(err)
	}
	validator, err := NewFileBundleValidator(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.ValidateBundle(
		context.Background(), "link.bundle",
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink = %v", err)
	}
}
