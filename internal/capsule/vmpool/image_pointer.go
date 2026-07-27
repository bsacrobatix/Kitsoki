package vmpool

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	// WorkerImagePointerSchema identifies the root-owned deployment pointer
	// consumed by hosted vmpool workers.
	WorkerImagePointerSchema = "kitsoki/worker-image-pointer/v1"
	maxImagePointerBytes     = 16 << 10
)

var (
	sourceSHAPattern   = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	imageIDPattern     = regexp.MustCompile(`^[1-9][0-9]*$`)
	imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	environmentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

// ImagePointer is the immutable image identity selected for one worker lease.
// The deployment controller updates the containing file by atomic rename;
// LoadWorkerImagePointer opens one inode and reads that stable snapshot.
type ImagePointer struct {
	Schema      string    `json:"schema"`
	Environment string    `json:"environment"`
	Generation  uint64    `json:"generation"`
	SourceSHA   string    `json:"source_sha"`
	ImageID     string    `json:"image_id"`
	ImageDigest string    `json:"image_digest"`
	ActivatedAt time.Time `json:"activated_at"`
}

// LoadWorkerImagePointer strictly loads a root-owned, regular, non-symlink
// pointer. It fails closed on unsafe ownership/mode, malformed or oversized
// content, unknown fields, and incomplete identities.
func LoadWorkerImagePointer(path string) (ImagePointer, error) {
	return loadWorkerImagePointer(path, 0)
}

func loadWorkerImagePointer(path string, ownerUID uint32) (ImagePointer, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ImagePointer{}, fmt.Errorf("vmpool: worker image pointer path is required")
	}
	if !filepath.IsAbs(path) {
		return ImagePointer{}, fmt.Errorf("vmpool: worker image pointer path must be absolute: %q", path)
	}
	path = canonicalWorkerImagePointerPath(path, runtime.GOOS)
	f, size, err := openTrustedImagePointer(path, ownerUID)
	if err != nil {
		return ImagePointer{}, err
	}
	defer f.Close()

	if size <= 0 || size > maxImagePointerBytes {
		return ImagePointer{}, fmt.Errorf("vmpool: worker image pointer %s size %d is outside 1..%d bytes", path, size, maxImagePointerBytes)
	}

	raw, err := io.ReadAll(io.LimitReader(f, maxImagePointerBytes+1))
	if err != nil {
		return ImagePointer{}, fmt.Errorf("vmpool: read worker image pointer %s: %w", path, err)
	}
	if len(raw) > maxImagePointerBytes {
		return ImagePointer{}, fmt.Errorf("vmpool: worker image pointer %s exceeds %d bytes", path, maxImagePointerBytes)
	}

	var pointer ImagePointer
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pointer); err != nil {
		return ImagePointer{}, fmt.Errorf("vmpool: parse worker image pointer %s: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return ImagePointer{}, fmt.Errorf("vmpool: parse worker image pointer %s: multiple JSON values are not allowed", path)
		}
		return ImagePointer{}, fmt.Errorf("vmpool: parse worker image pointer %s: %w", path, err)
	}
	if err := pointer.validate(); err != nil {
		return ImagePointer{}, fmt.Errorf("vmpool: validate worker image pointer %s: %w", path, err)
	}
	return pointer, nil
}

// canonicalWorkerImagePointerPath translates only the Darwin-owned /etc
// compatibility alias.  On macOS, /etc is a symlink to /private/etc; walking
// the configured spelling would correctly trip the no-symlink parent guard,
// even though the deployment-controlled pointer itself is under the canonical
// root-owned directory.  Translate that one OS alias before the trust walk so
// every directory that is actually opened is still checked and no caller
// supplied symlink is followed.
//
// This deliberately does not use EvalSymlinks: that would turn an arbitrary
// caller-controlled symlink into an accepted path.  All other symlink parents
// remain rejected by validateImagePointerParents.
func canonicalWorkerImagePointerPath(path, goos string) string {
	path = filepath.Clean(path)
	if goos != "darwin" {
		return path
	}
	if path == "/etc" {
		return "/private/etc"
	}
	if strings.HasPrefix(path, "/etc/") {
		return "/private/etc/" + strings.TrimPrefix(path, "/etc/")
	}
	return path
}

func (p ImagePointer) validate() error {
	if p.Schema != WorkerImagePointerSchema {
		return fmt.Errorf("schema %q, want %q", p.Schema, WorkerImagePointerSchema)
	}
	if !environmentPattern.MatchString(p.Environment) {
		return fmt.Errorf("environment %q is invalid", p.Environment)
	}
	if p.Generation == 0 {
		return fmt.Errorf("generation must be positive")
	}
	if !sourceSHAPattern.MatchString(p.SourceSHA) {
		return fmt.Errorf("source_sha must be a 40- or 64-character lowercase hexadecimal SHA")
	}
	if !imageIDPattern.MatchString(p.ImageID) {
		return fmt.Errorf("image_id must be a positive decimal provider image id")
	}
	if !imageDigestPattern.MatchString(p.ImageDigest) {
		return fmt.Errorf("image_digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	if p.ActivatedAt.IsZero() {
		return fmt.Errorf("activated_at is required")
	}
	return nil
}
