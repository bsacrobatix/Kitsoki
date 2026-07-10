package control

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var instanceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// Manager owns definition resolution, instance leases, and provider lifecycle.
// It is the only component that knows machine paths behind opaque handles.
type Manager struct {
	Definitions DefinitionStore
	Instances   InstanceStore
	Providers   map[string]WorkspaceProvider
	Grant       ScopeGrant
	Events      EventSink
	Now         func() time.Time
}

type CreateRequest struct{ ID, DefinitionID, Owner, Provider string }

func (m *Manager) Create(ctx context.Context, req CreateRequest) (Handle, error) {
	if err := m.ready(); err != nil {
		return Handle{}, err
	}
	if !instanceIDPattern.MatchString(req.ID) {
		return Handle{}, fmt.Errorf("capsule control: invalid instance id %q", req.ID)
	}
	if strings.TrimSpace(req.Owner) == "" {
		return Handle{}, fmt.Errorf("capsule control: owner is required")
	}
	if !m.Grant.Allows("definition", req.DefinitionID) {
		return Handle{}, fmt.Errorf("%w: definition %q", ErrDenied, req.DefinitionID)
	}
	def, err := m.Definitions.Get(ctx, req.DefinitionID)
	if err != nil {
		return Handle{}, err
	}
	providerName := req.Provider
	if providerName == "" {
		providerName = string(def.Source.Kind)
	}
	if !m.Grant.Allows("executor", providerName) {
		return Handle{}, fmt.Errorf("%w: executor %q", ErrDenied, providerName)
	}
	provider := m.Providers[providerName]
	if provider == nil {
		return Handle{}, fmt.Errorf("capsule control: provider %q is not configured", providerName)
	}
	if existing, err := m.Instances.Get(ctx, req.ID); err == nil {
		if existing.Lease.Owner != req.Owner || existing.DefinitionID != def.ID {
			return Handle{}, fmt.Errorf("%w: instance %q", ErrLeaseConflict, req.ID)
		}
		if existing.State == StateClosed {
			return Handle{}, fmt.Errorf("%w: instance %q is closed", ErrInvalidState, req.ID)
		}
		return Handle{ID: existing.ID, Generation: existing.Generation}, nil
	} else if !strings.Contains(err.Error(), ErrNotFound.Error()) {
		return Handle{}, err
	}
	root := m.Grant.WorkspaceRoots[0]
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Handle{}, fmt.Errorf("capsule control: create workspace root: %w", err)
	}
	path, err := ResolveWorkspacePath(root, req.ID, false)
	if err != nil {
		return Handle{}, err
	}
	now := m.now()
	in, err := m.Instances.Create(ctx, Instance{ID: req.ID, DefinitionID: def.ID, DefinitionDigest: def.Digest, Provider: provider.Name(), Path: path, State: StateMaterializing, Lease: Lease{Owner: req.Owner, Acquired: now}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return Handle{}, err
	}
	_ = m.emit(ctx, "capsule.workspace.materializing", in)
	materialized, err := provider.Create(ctx, def, in)
	if err != nil {
		_, _ = m.Instances.CompareAndSwap(ctx, in.ID, in.Generation, func(cur *Instance) error { cur.State = StateFailed; return nil })
		_ = m.emit(ctx, "capsule.workspace.failed", in)
		return Handle{}, err
	}
	in, err = m.Instances.CompareAndSwap(ctx, in.ID, in.Generation, func(cur *Instance) error {
		cur.Path = materialized.Path
		cur.SourceRef = materialized.SourceRef
		cur.Head = materialized.Head
		cur.Branch = materialized.Branch
		cur.State = StateReady
		return nil
	})
	if err != nil {
		return Handle{}, err
	}
	_ = m.emit(ctx, "capsule.workspace.ready", in)
	return Handle{ID: in.ID, Generation: in.Generation}, nil
}

func (m *Manager) Status(ctx context.Context, h Handle) (Instance, error) {
	if err := m.ready(); err != nil {
		return Instance{}, err
	}
	in, err := m.Instances.Get(ctx, h.ID)
	if err != nil {
		return Instance{}, err
	}
	if in.Generation != h.Generation {
		return Instance{}, fmt.Errorf("%w: instance %q", ErrStale, h.ID)
	}
	return in, nil
}

func (m *Manager) Close(ctx context.Context, h Handle, owner string) error {
	in, err := m.Status(ctx, h)
	if err != nil {
		return err
	}
	if in.Lease.Owner != owner {
		return fmt.Errorf("%w: instance %q", ErrLeaseConflict, h.ID)
	}
	provider := m.Providers[in.Provider]
	if provider == nil {
		return fmt.Errorf("capsule control: provider %q is not configured", in.Provider)
	}
	if err := provider.Close(ctx, in); err != nil {
		return err
	}
	in, err = m.Instances.CompareAndSwap(ctx, h.ID, h.Generation, func(cur *Instance) error { cur.State = StateClosed; return nil })
	if err != nil {
		return err
	}
	return m.emit(ctx, "capsule.workspace.closed", in)
}

func (m *Manager) ready() error {
	if m.Definitions == nil || m.Instances == nil {
		return fmt.Errorf("capsule control: definitions and instances are required")
	}
	if err := m.Grant.Validate(); err != nil {
		return err
	}
	return nil
}
func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}
func (m *Manager) emit(ctx context.Context, kind string, in Instance) error {
	if m.Events == nil {
		return nil
	}
	return m.Events.Emit(ctx, Event{Kind: kind, InstanceID: in.ID, Generation: in.Generation, Fields: map[string]any{"definition_id": in.DefinitionID, "definition_digest": in.DefinitionDigest, "provider": in.Provider, "state": in.State, "lease_owner": in.Lease.Owner, "source_ref": in.SourceRef, "head": in.Head}})
}

// WorkspacePath is intentionally package-private authority exposed only to
// same-process providers/front doors after lease and generation verification.
func (m *Manager) WorkspacePath(ctx context.Context, h Handle) (string, error) {
	in, err := m.Status(ctx, h)
	if err != nil {
		return "", err
	}
	path := in.Path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	for _, root := range m.Grant.WorkspaceRoots {
		if real, err := filepath.EvalSymlinks(root); err == nil {
			root = real
		}
		if rel, e := filepath.Rel(root, path); e == nil && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return path, nil
		}
	}
	return "", fmt.Errorf("%w: instance path is outside grant", ErrDenied)
}
