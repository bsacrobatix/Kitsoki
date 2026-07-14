package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AdoptDevWorkspace records a verified clone made by scripts/dev-workspace.sh
// in the native Capsule instance store. The compatibility script remains the
// only creator of the checkout; this method makes its durable provenance
// available to native Capsule CI without trusting a caller-provided path.
func (m *Manager) AdoptDevWorkspace(ctx context.Context, id string) (Handle, error) {
	if err := m.ready(); err != nil {
		return Handle{}, err
	}
	if !instanceIDPattern.MatchString(id) {
		return Handle{}, fmt.Errorf("capsule control: invalid instance id %q", id)
	}

	def, err := m.Definition(ctx, "development")
	if err != nil {
		return Handle{}, err
	}
	if def.Source.Kind != SourceDevWorkspaceScript {
		return Handle{}, fmt.Errorf("capsule control: definition %q is not a dev-workspace-script definition", def.ID)
	}
	root, err := m.workspaceRoot(def)
	if err != nil {
		return Handle{}, err
	}
	path, err := ResolveWorkspacePath(root, id, true)
	if err != nil {
		return Handle{}, fmt.Errorf("capsule control: resolve script workspace %q: %w", id, err)
	}
	manifest, err := readDevWorkspaceManifest(path)
	if err != nil {
		return Handle{}, err
	}
	if err := validateDevWorkspaceManifest(m.Grant.ProjectRoot, root, path, id, def, manifest); err != nil {
		return Handle{}, err
	}
	if _, err := os.Stat(filepath.Join(path, instanceSentinel)); err != nil {
		return Handle{}, fmt.Errorf("capsule control: script workspace %q is missing capsule sentinel: %w", id, err)
	}
	if _, err := os.Stat(filepath.Join(path, ".kitsoki-clone")); err != nil {
		return Handle{}, fmt.Errorf("capsule control: script workspace %q is missing clone sentinel: %w", id, err)
	}
	head, err := runGit(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return Handle{}, fmt.Errorf("capsule control: inspect script workspace %q head: %w", id, err)
	}
	branch, err := runGit(ctx, path, "branch", "--show-current")
	if err != nil {
		return Handle{}, fmt.Errorf("capsule control: inspect script workspace %q branch: %w", id, err)
	}
	head = strings.TrimSpace(head)
	branch = strings.TrimSpace(branch)
	if branch == "" || branch != manifest.Branch {
		return Handle{}, fmt.Errorf("capsule control: script workspace %q branch %q does not match manifest branch %q", id, branch, manifest.Branch)
	}

	owner := manifest.SessionID
	if owner == "" {
		owner = "scripts/dev-workspace.sh"
	}
	if existing, getErr := m.Instances.Get(ctx, id); getErr == nil {
		if existing.Provider != string(SourceDevWorkspaceScript) || existing.DefinitionID != def.ID {
			return Handle{}, fmt.Errorf("%w: instance %q", ErrLeaseConflict, id)
		}
		next, err := m.Instances.CompareAndSwap(ctx, id, existing.Generation, func(cur *Instance) error {
			cur.DefinitionDigest = def.Digest
			cur.Path = path
			cur.SourceRef = manifest.Base
			cur.Head = head
			cur.Branch = branch
			cur.State = StateReady
			if cur.Lease.Owner == "" {
				cur.Lease.Owner = owner
			}
			return nil
		})
		if err != nil {
			return Handle{}, err
		}
		return Handle{ID: next.ID, Generation: next.Generation}, nil
	} else if !strings.Contains(getErr.Error(), ErrNotFound.Error()) {
		return Handle{}, getErr
	}

	now := m.now()
	created, err := m.Instances.Create(ctx, Instance{
		ID:               id,
		DefinitionID:     def.ID,
		DefinitionDigest: def.Digest,
		Provider:         string(SourceDevWorkspaceScript),
		Path:             path,
		SourceRef:        manifest.Base,
		Head:             head,
		Branch:           branch,
		State:            StateReady,
		Generation:       1,
		Lease:            Lease{Owner: owner, Acquired: now},
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		return Handle{}, err
	}
	return Handle{ID: created.ID, Generation: created.Generation}, nil
}

type devWorkspaceManifest struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Root      string `json:"root"`
	Branch    string `json:"branch"`
	Base      string `json:"base"`
	Target    string `json:"target"`
	SessionID string `json:"session_id"`
	Workspace string `json:"workspace"`
	ManagedBy string `json:"managed_by"`
}

func readDevWorkspaceManifest(path string) (devWorkspaceManifest, error) {
	raw, err := os.ReadFile(filepath.Join(path, ".kitsoki-dev-workspace.json"))
	if err != nil {
		return devWorkspaceManifest{}, fmt.Errorf("capsule control: read script workspace manifest: %w", err)
	}
	var manifest devWorkspaceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return devWorkspaceManifest{}, fmt.Errorf("capsule control: parse script workspace manifest: %w", err)
	}
	return manifest, nil
}

func validateDevWorkspaceManifest(projectRoot, root, path, id string, def Definition, manifest devWorkspaceManifest) error {
	if manifest.ID != id || manifest.ManagedBy != "scripts/dev-workspace.sh" {
		return fmt.Errorf("capsule control: script workspace %q has invalid managed provenance", id)
	}
	if manifest.Branch == "" || manifest.Base == "" || manifest.Target == "" {
		return fmt.Errorf("capsule control: script workspace %q has incomplete branch provenance", id)
	}
	if def.Source.Development.Base != "" && manifest.Base != def.Source.Development.Base {
		return fmt.Errorf("capsule control: script workspace %q base %q does not match development definition base %q", id, manifest.Base, def.Source.Development.Base)
	}
	if manifest.Target != def.Source.Development.Target {
		return fmt.Errorf("capsule control: script workspace %q target %q does not match development definition target %q", id, manifest.Target, def.Source.Development.Target)
	}
	for label, paths := range map[string][2]string{
		"source":    [2]string{manifest.Source, projectRoot},
		"root":      [2]string{manifest.Root, root},
		"workspace": [2]string{manifest.Workspace, path},
	} {
		if !sameCleanPath(paths[0], paths[1]) {
			return fmt.Errorf("capsule control: script workspace %q manifest %s escapes its managed project", id, label)
		}
	}
	return nil
}

func sameCleanPath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(left); err == nil {
		left = resolved
	}
	if resolved, err := filepath.EvalSymlinks(right); err == nil {
		right = resolved
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
