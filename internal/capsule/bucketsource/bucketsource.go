// Package bucketsource bridges the capsule executor's source/output transport
// to shared object storage (internal/objectstore). Frozen source bundles are
// published write-once under <prefix>/<head>/ and handed to workers as
// presigned fetch URLs; run outputs mirror under <prefix>/<execution-id>/ so
// results survive ephemeral worker hosts.
package bucketsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/workerserver"
	"kitsoki/internal/objectstore"
)

const (
	SourceObjectMetaSchema = "capsule-bucket-source/v1"
	DefaultSourcePrefix    = "sources"
	DefaultRunPrefix       = "runs"
	DefaultPresignTTL      = time.Hour
	bundleContentType      = "application/vnd.git.bundle"
)

// SourceObjectMeta is the sidecar written next to each published bundle so
// bucket contents are self-describing without fetching the bundle itself.
type SourceObjectMeta struct {
	Schema       string    `json:"schema"`
	Head         string    `json:"head"`
	BundleDigest string    `json:"bundle_digest"`
	Size         int64     `json:"size"`
	PublishedAt  time.Time `json:"published_at"`
}

// Publisher implements executor.SourceObjects on an object store. Keys are
// content-addressed by commit, so the bucket copy is written once and treated
// as frozen: a Head hit skips the upload entirely, and racing writers publish
// identical bytes.
type Publisher struct {
	Store      objectstore.Store
	Prefix     string
	PresignTTL time.Duration
	Now        func() time.Time
}

// EnsureBundle publishes bundle write-once and returns the worker fetch
// reference. On a cache hit the reference carries the digest/size recorded in
// the published meta.json sidecar — git bundles for the same head are not
// guaranteed byte-identical across builds, and the worker must verify the
// bytes it will actually download.
func (p Publisher) EnsureBundle(ctx context.Context, bundle executor.SourceBundle) (executor.SourceFetchRequest, error) {
	if p.Store == nil {
		return executor.SourceFetchRequest{}, fmt.Errorf("bucketsource: object store is required")
	}
	if err := executor.ValidateSourceBundle(bundle, 0); err != nil {
		return executor.SourceFetchRequest{}, err
	}
	prefix := p.Prefix
	if prefix == "" {
		prefix = DefaultSourcePrefix
	}
	ttl := p.PresignTTL
	if ttl <= 0 {
		ttl = DefaultPresignTTL
	}
	now := p.Now
	if now == nil {
		now = time.Now
	}
	key := prefix + "/" + bundle.Head + "/bundle.git"
	metaKey := prefix + "/" + bundle.Head + "/meta.json"
	published := SourceObjectMeta{Schema: SourceObjectMetaSchema, Head: bundle.Head, BundleDigest: bundle.Digest, Size: bundle.Size, PublishedAt: now().UTC()}
	existing, err := readMeta(ctx, p.Store, metaKey)
	switch {
	case err == nil && existing.Head == bundle.Head:
		// Frozen copy already published; the sidecar describes its bytes.
		published = existing
	case err == nil:
		return executor.SourceFetchRequest{}, fmt.Errorf("bucketsource: published metadata at %s names head %s, want %s", metaKey, existing.Head, bundle.Head)
	case errors.Is(err, objectstore.ErrNotFound):
		if _, err := p.Store.Put(ctx, key, bytes.NewReader(bundle.Data), bundle.Size, objectstore.PutOptions{ContentType: bundleContentType}); err != nil {
			return executor.SourceFetchRequest{}, fmt.Errorf("bucketsource: publish bundle: %w", err)
		}
		raw, marshalErr := json.Marshal(published)
		if marshalErr != nil {
			return executor.SourceFetchRequest{}, marshalErr
		}
		if _, err := p.Store.Put(ctx, metaKey, bytes.NewReader(raw), int64(len(raw)), objectstore.PutOptions{ContentType: "application/json"}); err != nil {
			return executor.SourceFetchRequest{}, fmt.Errorf("bucketsource: publish bundle metadata: %w", err)
		}
	default:
		return executor.SourceFetchRequest{}, fmt.Errorf("bucketsource: inspect published source: %w", err)
	}
	link, err := p.Store.Presign(ctx, http.MethodGet, key, ttl)
	if err != nil {
		return executor.SourceFetchRequest{}, fmt.Errorf("bucketsource: presign bundle fetch: %w", err)
	}
	return executor.SourceFetchRequest{Schema: executor.SourceFetchSchema, URL: link, Digest: published.BundleDigest, Size: published.Size}, nil
}

func readMeta(ctx context.Context, store objectstore.Store, key string) (SourceObjectMeta, error) {
	rc, _, err := store.Get(ctx, key)
	if err != nil {
		return SourceObjectMeta{}, err
	}
	defer rc.Close()
	var meta SourceObjectMeta
	if err := json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&meta); err != nil {
		return SourceObjectMeta{}, fmt.Errorf("bucketsource: decode %s: %w", key, err)
	}
	if meta.Schema != SourceObjectMetaSchema {
		return SourceObjectMeta{}, fmt.Errorf("bucketsource: %s has unsupported schema %q", key, meta.Schema)
	}
	return meta, nil
}

var _ executor.SourceObjects = Publisher{}

// OutputMirror implements workerserver.OutputSink on an object store. Each
// execution mirrors under <prefix>/<execution-id>/: run.json on every
// checkpoint, plus story-trace.jsonl at terminal.
type OutputMirror struct {
	Store  objectstore.Store
	Prefix string
}

func (m OutputMirror) prefix() string {
	if m.Prefix == "" {
		return DefaultRunPrefix
	}
	return m.Prefix
}

func (m OutputMirror) MirrorRun(ctx context.Context, record workerserver.RunRecord) error {
	if m.Store == nil {
		return fmt.Errorf("bucketsource: object store is required")
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	key := m.prefix() + "/" + record.ExecutionID + "/run.json"
	if _, err := m.Store.Put(ctx, key, bytes.NewReader(raw), int64(len(raw)), objectstore.PutOptions{ContentType: "application/json"}); err != nil {
		return fmt.Errorf("bucketsource: mirror run record: %w", err)
	}
	return nil
}

func (m OutputMirror) MirrorTerminal(ctx context.Context, record workerserver.RunRecord, runDir string) error {
	if m.Store == nil {
		return fmt.Errorf("bucketsource: object store is required")
	}
	tracePath := filepath.Join(runDir, "story-trace.jsonl")
	info, err := os.Stat(tracePath)
	if err != nil {
		if os.IsNotExist(err) {
			// A run that failed before the story started has no trace; the
			// mirrored run.json already carries the failure timeline.
			return nil
		}
		return err
	}
	file, err := os.Open(tracePath)
	if err != nil {
		return err
	}
	defer file.Close()
	key := m.prefix() + "/" + record.ExecutionID + "/story-trace.jsonl"
	if _, err := m.Store.Put(ctx, key, file, info.Size(), objectstore.PutOptions{ContentType: "application/jsonl"}); err != nil {
		return fmt.Errorf("bucketsource: mirror story trace: %w", err)
	}
	return nil
}

var _ workerserver.OutputSink = OutputMirror{}
