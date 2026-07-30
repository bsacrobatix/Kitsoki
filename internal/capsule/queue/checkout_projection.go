package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"kitsoki/internal/atomicfile"
)

const checkoutProjectionSchema = "capsule-queue-checkout-projection/v1"

// checkoutProjection remembers a checkout that could not follow a successful
// protected-ref CAS. CheckoutTree is the exact live filesystem tree after the
// warning; comparing it before the next CAS distinguishes unchanged projection
// lag from genuinely new user edits and prevents a new preserved-WIP branch on
// every landing.
type checkoutProjection struct {
	Schema          string `json:"schema"`
	TargetRef       string `json:"target_ref"`
	TargetSHA       string `json:"target_sha"`
	CheckoutTree    string `json:"checkout_tree"`
	PreservedBranch string `json:"preserved_branch,omitempty"`
}

func (p ProtectedFinalizer) projectionPath(target string) (string, error) {
	root := strings.TrimSpace(p.QueueRoot)
	if root == "" {
		root = filepath.Join(p.ProjectRoot, ".capsules", "queue")
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", err
		}
		root = abs
	}
	sum := sha256.Sum256([]byte(target))
	return filepath.Join(root, "checkout-projections", hex.EncodeToString(sum[:16])+".json"), nil
}

func (p ProtectedFinalizer) reusableProjection(ctx context.Context, target string) (checkoutProjection, bool, error) {
	path, err := p.projectionPath(target)
	if err != nil {
		return checkoutProjection{}, false, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return checkoutProjection{}, false, nil
	}
	if err != nil {
		return checkoutProjection{}, false, err
	}
	var projection checkoutProjection
	if err := json.Unmarshal(raw, &projection); err != nil {
		p.quarantineProjection(path, raw)
		return checkoutProjection{}, false, nil
	}
	if projection.Schema != checkoutProjectionSchema || projection.TargetRef != target ||
		projection.TargetSHA == "" || projection.CheckoutTree == "" {
		p.quarantineProjection(path, raw)
		return checkoutProjection{}, false, nil
	}
	liveTarget, err := gitOutput(ctx, p.ProjectRoot, "rev-parse", "--verify", target+"^{commit}")
	if err != nil || liveTarget != projection.TargetSHA {
		return projection, false, nil
	}
	tree, _, err := snapshotTree(ctx, p.ProjectRoot)
	if err != nil {
		return checkoutProjection{}, false, err
	}
	return projection, tree == projection.CheckoutTree, nil
}

func (p ProtectedFinalizer) quarantineProjection(path string, raw []byte) {
	sum := sha256.Sum256(raw)
	_ = os.Rename(path, path+".corrupt-"+hex.EncodeToString(sum[:8]))
}

func (p ProtectedFinalizer) writeProjection(ctx context.Context, target, targetSHA, preserved string) error {
	tree, _, err := snapshotTree(ctx, p.ProjectRoot)
	if err != nil {
		return err
	}
	path, err := p.projectionPath(target)
	if err != nil {
		return err
	}
	projection := checkoutProjection{
		Schema: checkoutProjectionSchema, TargetRef: target, TargetSHA: targetSHA,
		CheckoutTree: tree, PreservedBranch: preserved,
	}
	raw, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(raw, '\n'), 0o600, 0o700)
}

func (p ProtectedFinalizer) clearProjection(target string) {
	path, err := p.projectionPath(target)
	if err == nil {
		_ = os.Remove(path)
	}
}
