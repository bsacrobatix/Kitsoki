// Package sourceartifact binds a frozen Git bundle to a registered Capsule
// identity.  It is intentionally separate from generic source-by-HEAD cache
// entries: a hosted promotion must never substitute the controller's main
// checkout merely because it has a commit with the same ancestry.
package sourceartifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/objectstore"
)

const Schema = "capsule-registered-source-artifact/v1"

type Identity struct {
	ProjectID        string `json:"project_id"`
	WorkspaceID      string `json:"workspace_id"`
	WorkspaceGen     uint64 `json:"workspace_generation"`
	DefinitionDigest string `json:"definition_digest"`
	Branch           string `json:"branch"`
	Head             string `json:"head"`
}

type Manifest struct {
	Schema       string   `json:"schema"`
	Identity     Identity `json:"identity"`
	BundleKey    string   `json:"bundle_key"`
	BundleDigest string   `json:"bundle_digest"`
	BundleBytes  int64    `json:"bundle_bytes"`
}

func New(identity Identity, bundle executor.SourceBundle, prefix string) (Manifest, error) {
	if strings.TrimSpace(identity.ProjectID) == "" || strings.TrimSpace(identity.WorkspaceID) == "" || identity.WorkspaceGen == 0 || strings.TrimSpace(identity.DefinitionDigest) == "" || strings.TrimSpace(identity.Branch) == "" || strings.TrimSpace(identity.Head) == "" {
		return Manifest{}, fmt.Errorf("capsule source artifact: registered identity is incomplete")
	}
	if err := executor.ValidateSourceBundle(bundle, 0); err != nil {
		return Manifest{}, fmt.Errorf("capsule source artifact: bundle: %w", err)
	}
	if bundle.Head != identity.Head {
		return Manifest{}, fmt.Errorf("capsule source artifact: bundle head %s does not match registered head %s", bundle.Head, identity.Head)
	}
	if prefix == "" {
		prefix = "capsule-registered-sources"
	}
	key := path.Join(prefix, identity.ProjectID, identity.WorkspaceID, fmt.Sprintf("%d", identity.WorkspaceGen), identity.Head, "bundle.git")
	return Manifest{Schema: Schema, Identity: identity, BundleKey: key, BundleDigest: bundle.Digest, BundleBytes: bundle.Size}, nil
}

func Validate(manifest Manifest) error {
	if manifest.Schema != Schema {
		return fmt.Errorf("capsule source artifact: unsupported schema %q", manifest.Schema)
	}
	if strings.TrimSpace(manifest.BundleKey) == "" || !strings.HasSuffix(manifest.BundleKey, "/bundle.git") || strings.Contains(manifest.BundleKey, "..") {
		return fmt.Errorf("capsule source artifact: unsafe bundle key")
	}
	if manifest.BundleDigest == "" || manifest.BundleBytes <= 0 {
		return fmt.Errorf("capsule source artifact: bundle metadata is incomplete")
	}
	return nil
}

func Publish(ctx context.Context, store objectstore.Store, manifest Manifest, bundle executor.SourceBundle) error {
	if store == nil {
		return fmt.Errorf("capsule source artifact: object store is required")
	}
	if err := Validate(manifest); err != nil {
		return err
	}
	if err := executor.ValidateSourceBundle(bundle, 0); err != nil {
		return err
	}
	if bundle.Head != manifest.Identity.Head || bundle.Digest != manifest.BundleDigest || bundle.Size != manifest.BundleBytes {
		return fmt.Errorf("capsule source artifact: bundle does not match manifest")
	}
	if err := putExact(ctx, store, manifest.BundleKey, bundle.Data, "application/vnd.git.bundle"); err != nil {
		return err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return putExact(ctx, store, manifestKey(manifest.BundleKey), raw, "application/json")
}

func Resolve(ctx context.Context, store objectstore.Store, key string) (Manifest, executor.SourceBundle, error) {
	if store == nil {
		return Manifest{}, executor.SourceBundle{}, fmt.Errorf("capsule source artifact: object store is required")
	}
	raw, err := getExact(ctx, store, key, 1<<20)
	if err != nil {
		return Manifest{}, executor.SourceBundle{}, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, executor.SourceBundle{}, fmt.Errorf("capsule source artifact: decode manifest: %w", err)
	}
	if err := Validate(m); err != nil {
		return Manifest{}, executor.SourceBundle{}, err
	}
	if manifestKey(m.BundleKey) != key {
		return Manifest{}, executor.SourceBundle{}, fmt.Errorf("capsule source artifact: manifest key substitution")
	}
	bundleRaw, err := getExact(ctx, store, m.BundleKey, m.BundleBytes)
	if err != nil {
		return Manifest{}, executor.SourceBundle{}, err
	}
	b := executor.SourceBundle{Schema: executor.SourceBundleSchema, Format: executor.SourceBundleFormat, Head: m.Identity.Head, Digest: m.BundleDigest, Size: m.BundleBytes, Data: bundleRaw}
	if err := executor.ValidateSourceBundle(b, 0); err != nil {
		return Manifest{}, executor.SourceBundle{}, err
	}
	return m, b, nil
}

// Materialize creates a host-local detached checkout solely from a resolved
// artifact.  There is deliberately no repository remote or main fallback.
// Callers remove the returned directory after CI; the immutable artifact is
// the durable/replayable source authority.
func Materialize(ctx context.Context, parent string, manifest Manifest, bundle executor.SourceBundle) (string, error) {
	if err := Validate(manifest); err != nil {
		return "", err
	}
	if err := executor.ValidateSourceBundle(bundle, 0); err != nil {
		return "", err
	}
	if bundle.Head != manifest.Identity.Head || bundle.Digest != manifest.BundleDigest || bundle.Size != manifest.BundleBytes {
		return "", fmt.Errorf("capsule source artifact: materialization bundle mismatch")
	}
	root, err := os.MkdirTemp(parent, "capsule-source-")
	if err != nil {
		return "", err
	}
	bundlePath := filepath.Join(root, "source.bundle")
	if err := os.WriteFile(bundlePath, bundle.Data, 0o600); err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}
	checkout := filepath.Join(root, "checkout")
	if out, err := exec.CommandContext(ctx, "git", "clone", "--no-checkout", bundlePath, checkout).CombinedOutput(); err != nil {
		_ = os.RemoveAll(root)
		return "", fmt.Errorf("capsule source artifact: clone: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", checkout, "checkout", "--detach", manifest.Identity.Head).CombinedOutput(); err != nil {
		_ = os.RemoveAll(root)
		return "", fmt.Errorf("capsule source artifact: checkout: %w: %s", err, strings.TrimSpace(string(out)))
	}
	out, err := exec.CommandContext(ctx, "git", "-C", checkout, "rev-parse", "HEAD").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != manifest.Identity.Head {
		_ = os.RemoveAll(root)
		return "", fmt.Errorf("capsule source artifact: materialized HEAD does not match identity")
	}
	return checkout, nil
}

func manifestKey(bundleKey string) string {
	return strings.TrimSuffix(bundleKey, "/bundle.git") + "/manifest.json"
}
func putExact(ctx context.Context, store objectstore.Store, key string, raw []byte, contentType string) error {
	if existing, err := getExact(ctx, store, key, int64(len(raw))); err == nil {
		if bytes.Equal(existing, raw) {
			return nil
		}
		return fmt.Errorf("capsule source artifact: immutable object %s differs", key)
	} else if !errors.Is(err, objectstore.ErrNotFound) {
		return err
	}
	_, err := store.Put(ctx, key, bytes.NewReader(raw), int64(len(raw)), objectstore.PutOptions{ContentType: contentType})
	return err
}
func getExact(ctx context.Context, store objectstore.Store, key string, max int64) ([]byte, error) {
	r, meta, err := store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if meta.Key != key || meta.Size <= 0 || meta.Size > max {
		return nil, fmt.Errorf("capsule source artifact: object %s size is invalid", key)
	}
	raw, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != meta.Size {
		return nil, fmt.Errorf("capsule source artifact: object %s size changed", key)
	}
	return raw, nil
}
