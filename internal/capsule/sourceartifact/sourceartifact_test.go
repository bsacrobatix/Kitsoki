package sourceartifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/objectstore"
)

func fixture(t *testing.T) (Identity, executor.SourceBundle) {
	t.Helper()
	raw := []byte("bundle bytes")
	sum := sha256.Sum256(raw)
	head := strings.Repeat("a", 40)
	return Identity{ProjectID: "pog", WorkspaceID: "external-admission-upgrade-20260727", WorkspaceGen: 7, DefinitionDigest: "sha256:def", Branch: "agent/external-admission-upgrade-20260727", Head: head}, executor.SourceBundle{Schema: executor.SourceBundleSchema, Format: executor.SourceBundleFormat, Head: head, Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(raw)), Data: raw}
}

func TestPublishResolveBindsBundleToRegisteredIdentity(t *testing.T) {
	identity, bundle := fixture(t)
	manifest, err := New(identity, bundle, "")
	if err != nil {
		t.Fatal(err)
	}
	store := objectstore.NewFake()
	if err := Publish(context.Background(), store, manifest, bundle); err != nil {
		t.Fatal(err)
	}
	resolved, got, err := Resolve(context.Background(), store, manifestKey(manifest.BundleKey))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Identity != identity || got.Digest != bundle.Digest || string(got.Data) != string(bundle.Data) {
		t.Fatalf("resolved artifact lost identity or bytes: %#v %#v", resolved, got)
	}
}

func TestNewRejectsHeadSubstitution(t *testing.T) {
	identity, bundle := fixture(t)
	identity.Head = strings.Repeat("b", 40)
	if _, err := New(identity, bundle, ""); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("head substitution accepted: %v", err)
	}
}

func TestResolveRejectsManifestKeySubstitution(t *testing.T) {
	identity, bundle := fixture(t)
	manifest, err := New(identity, bundle, "")
	if err != nil {
		t.Fatal(err)
	}
	store := objectstore.NewFake()
	if err := Publish(context.Background(), store, manifest, bundle); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Resolve(context.Background(), store, manifest.BundleKey); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("bundle key was accepted as manifest: %v", err)
	}
}

func TestMaterializeRejectsIdentityBundleMismatchBeforeGitFallback(t *testing.T) {
	identity, bundle := fixture(t)
	manifest, err := New(identity, bundle, "")
	if err != nil {
		t.Fatal(err)
	}
	bundle.Head = strings.Repeat("b", 40)
	if _, err := Materialize(context.Background(), t.TempDir(), manifest, bundle); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("materialize accepted substituted source or fell back: %v", err)
	}
}
