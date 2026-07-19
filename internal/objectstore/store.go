// Package objectstore provides the S3-compatible object storage seam used by
// remote Capsule workers: frozen, content-addressed source material is written
// once under sources/<sha>/ and worker output streams durably to runs/<job>/.
//
// The package deliberately exposes a small interface so tests and flows run
// against the in-memory Fake while production uses the Spaces client (a
// dependency-free SigV4 implementation targeting DigitalOcean Spaces or any
// S3-compatible endpoint). Frozen-source immutability is a write-once
// convention at the call site (Head before Put); keys are content-addressed,
// so racing writers produce identical bytes.
package objectstore

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound reports that no object exists at the requested key.
var ErrNotFound = errors.New("objectstore: object not found")

// Meta describes a stored object.
type Meta struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag,omitempty"`
	ContentType  string    `json:"content_type,omitempty"`
	LastModified time.Time `json:"last_modified,omitzero"`
}

// PutOptions carries optional attributes for Put.
type PutOptions struct {
	ContentType string
}

// Store is the minimal object-storage contract shared by the Fake and the
// Spaces client. Size must be the exact body length; implementations reject
// mismatches rather than truncate.
type Store interface {
	Put(ctx context.Context, key string, body io.Reader, size int64, opts PutOptions) (Meta, error)
	Get(ctx context.Context, key string) (io.ReadCloser, Meta, error)
	Head(ctx context.Context, key string) (Meta, error)
	List(ctx context.Context, prefix string) ([]Meta, error)
	Delete(ctx context.Context, key string) error
	// Presign returns a time-limited URL for method GET or PUT on key,
	// letting a worker touch exactly one object without holding credentials.
	Presign(ctx context.Context, method, key string, expiry time.Duration) (string, error)
}
