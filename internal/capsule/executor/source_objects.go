package executor

import (
	"context"
	"fmt"
	"strings"
)

const SourceFetchSchema = "capsule-source-fetch/v1"

// SourceObjects publishes a sealed source bundle to shared object storage and
// returns a complete fetch reference for it. When configured on a remote
// worker transport, the controller hands the worker this reference instead of
// the bundle bytes, so large sources move bucket→worker (fast, resumable, and
// reusable across workers) rather than controller→worker, and the worker
// never needs storage credentials. The returned request's digest/size must
// describe the published object — which, on a cache hit, may be an equally
// valid earlier bundle of the same head with different bytes than the
// caller's freshly built one.
type SourceObjects interface {
	EnsureBundle(ctx context.Context, bundle SourceBundle) (SourceFetchRequest, error)
}

// SourceFetchRequest is the worker-side contract for fetch-by-reference: the
// body of POST /v1/capsules/sources/{head}. The worker downloads at most Size
// bytes from URL and verifies Digest before accepting the source.
type SourceFetchRequest struct {
	Schema string `json:"schema"`
	URL    string `json:"url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func ValidateSourceFetchRequest(request SourceFetchRequest, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBundleSize
	}
	if request.Schema != SourceFetchSchema {
		return fmt.Errorf("capsule source: unsupported fetch contract")
	}
	if strings.TrimSpace(request.URL) == "" {
		return fmt.Errorf("capsule source: fetch URL is required")
	}
	if !strings.HasPrefix(request.Digest, "sha256:") {
		return fmt.Errorf("capsule source: fetch digest must be sha256")
	}
	if request.Size <= 0 || request.Size > maxBytes {
		return fmt.Errorf("capsule source: fetch size %d out of range", request.Size)
	}
	return nil
}
