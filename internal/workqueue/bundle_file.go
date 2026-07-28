package workqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const DefaultMaxBundleBytes int64 = 1 << 30

// FileBundleValidator verifies retained bundles below one daemon-owned root.
// Bundle refs are relative paths; absolute paths, traversal, symlinks, special
// files, oversized files, and digest mismatches fail closed.
type FileBundleValidator struct {
	root     string
	maxBytes int64
}

func NewFileBundleValidator(root string, maxBytes int64) (*FileBundleValidator, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("workqueue bundle root must be absolute")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBundleBytes
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create workqueue bundle root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workqueue bundle root: %w", err)
	}
	return &FileBundleValidator{root: resolved, maxBytes: maxBytes}, nil
}

func (v *FileBundleValidator) ValidateBundle(
	ctx context.Context,
	ref, digest string,
) (string, error) {
	_, err := v.readBundle(ctx, ref, digest)
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(ref)), nil
}

func (v *FileBundleValidator) readBundle(
	ctx context.Context,
	ref, digest string,
) ([]byte, error) {
	if v == nil {
		return nil, ErrInvalid
	}
	ref = filepath.Clean(strings.TrimSpace(ref))
	if ref == "." || filepath.IsAbs(ref) {
		return nil, ErrInvalid
	}
	candidate := filepath.Join(v.root, ref)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, ErrInvalid
	}
	relative, err := filepath.Rel(v.root, resolved)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, ErrInvalid
	}
	linkInfo, err := os.Lstat(candidate)
	if err != nil || !linkInfo.Mode().IsRegular() ||
		linkInfo.Mode()&os.ModeSymlink != 0 ||
		linkInfo.Size() <= 0 || linkInfo.Size() > v.maxBytes {
		return nil, ErrInvalid
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, ErrInvalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() ||
		!os.SameFile(linkInfo, info) || info.Size() != linkInfo.Size() {
		return nil, ErrInvalid
	}
	hash := sha256.New()
	var data strings.Builder
	n, err := io.Copy(
		io.MultiWriter(hash, &data),
		io.LimitReader(file, v.maxBytes+1),
	)
	if err != nil || n != info.Size() || n > v.maxBytes {
		return nil, ErrInvalid
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != strings.TrimSpace(digest) {
		return nil, ErrInvalid
	}
	return []byte(data.String()), nil
}
