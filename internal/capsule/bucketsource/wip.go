package bucketsource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/objectstore"
)

// WIPExportSchema identifies the sidecar written alongside a WIP bundle.
const WIPExportSchema = "capsule-wip-export/v1"

// WIPExport describes a workspace's uncommitted-work-in-progress bundle
// mirrored to the bucket so agent commits survive an ephemeral worker VM.
type WIPExport struct {
	Schema      string   `json:"schema"`
	ExecutionID string   `json:"execution_id"`
	SealedHead  string   `json:"sealed_head"`
	Head        string   `json:"head"`
	Refs        []string `json:"refs"`
	BundleKey   string   `json:"bundle_key"`
	Size        int64    `json:"size"`
	Dirty       bool     `json:"dirty"`
}

// ExportWIP inspects a git workspace and, if HEAD differs from sealedHead or
// the repo has refs beyond it, writes a bundle of all local refs to the store
// under runs/<execution-id>/wip/refs.bundle and returns its Meta description.
// A clean workspace still at sealedHead returns (nil, nil).
//
// The worktree is never mutated: a dirty worktree is recorded via
// WIPExport.Dirty and only the committed state is bundled.
func ExportWIP(ctx context.Context, store objectstore.Store, prefix, executionID, workspace, sealedHead string) (*WIPExport, error) {
	if store == nil {
		return nil, fmt.Errorf("bucketsource: object store is required")
	}
	if prefix == "" {
		prefix = DefaultRunPrefix
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("bucketsource: resolve workspace: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	sealedHead = strings.TrimSpace(sealedHead)

	head, err := gitOutputWIP(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("bucketsource: read HEAD: %w", err)
	}
	head = strings.TrimSpace(head)

	status, err := gitOutputWIP(ctx, root, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("bucketsource: inspect workspace: %w", err)
	}
	dirty := strings.TrimSpace(status) != ""

	refsOut, err := gitOutputWIP(ctx, root, "for-each-ref", "--format=%(refname)")
	if err != nil {
		return nil, fmt.Errorf("bucketsource: list refs: %w", err)
	}
	var refs []string
	for _, line := range strings.Split(refsOut, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			refs = append(refs, line)
		}
	}

	clean := !dirty && sealedHead != "" && head == sealedHead
	if clean {
		// HEAD matches the sealed source, but another ref (or a detached
		// commit) may still carry work beyond it.
		beyond, err := gitOutputWIP(ctx, root, "rev-list", "--all", "HEAD", "--not", sealedHead)
		if err != nil {
			return nil, fmt.Errorf("bucketsource: inspect refs beyond sealed head: %w", err)
		}
		clean = strings.TrimSpace(beyond) == ""
	}
	if clean {
		return nil, nil
	}

	tmpDir, err := os.MkdirTemp("", "kitsoki-capsule-wip-*")
	if err != nil {
		return nil, fmt.Errorf("bucketsource: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	bundlePath := filepath.Join(tmpDir, "refs.bundle")
	if _, err := gitOutputWIP(ctx, root, "bundle", "create", bundlePath, "--all"); err != nil {
		return nil, fmt.Errorf("bucketsource: create wip bundle: %w", err)
	}
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("bucketsource: read wip bundle: %w", err)
	}
	if size := int64(len(data)); size <= 0 || size > executor.DefaultMaxBundleSize {
		return nil, fmt.Errorf("bucketsource: wip bundle size %d exceeds allowed maximum %d", size, executor.DefaultMaxBundleSize)
	}

	bundleKey := prefix + "/" + executionID + "/wip/refs.bundle"
	if _, err := store.Put(ctx, bundleKey, bytes.NewReader(data), int64(len(data)), objectstore.PutOptions{ContentType: bundleContentType}); err != nil {
		return nil, fmt.Errorf("bucketsource: publish wip bundle: %w", err)
	}

	export := &WIPExport{
		Schema:      WIPExportSchema,
		ExecutionID: executionID,
		SealedHead:  sealedHead,
		Head:        head,
		Refs:        refs,
		BundleKey:   bundleKey,
		Size:        int64(len(data)),
		Dirty:       dirty,
	}
	raw, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("bucketsource: marshal wip export: %w", err)
	}
	sidecarKey := prefix + "/" + executionID + "/wip/wip.json"
	if _, err := store.Put(ctx, sidecarKey, bytes.NewReader(raw), int64(len(raw)), objectstore.PutOptions{ContentType: "application/json"}); err != nil {
		return nil, fmt.Errorf("bucketsource: publish wip export metadata: %w", err)
	}
	return export, nil
}

// gitOutputWIP runs git in root, following the gitOutput idiom in
// executor/source_bundle.go (kept package-local since that helper is
// unexported there).
func gitOutputWIP(ctx context.Context, root string, args ...string) (string, error) {
	argv := append([]string{"-C", root}, args...)
	out, err := exec.CommandContext(ctx, "git", argv...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
