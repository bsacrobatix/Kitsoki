package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"kitsoki/internal/capsule/control"
)

type Manager struct {
	Definition Definition
	Workspaces WorkspaceResolver
	Endpoints  EndpointBroker
	Processes  ProcessLauncher
	Listeners  ListenerInspector
	Health     HealthChecker
	Store      RecordStore
	Clock      Clock
	NewID      func() string
}
type UpRequest struct {
	ID, Owner, SourceSHA, Profile string
	Workspace                     control.Handle
	Purpose                       Purpose
	TTL                           time.Duration
}

func (m *Manager) Up(ctx context.Context, req UpRequest) (Receipt, error) {
	if err := m.ready(); err != nil {
		return Receipt{}, err
	}
	if strings.TrimSpace(req.ID) == "" {
		req.ID = m.NewID()
	}
	if strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.Owner) == "" || strings.TrimSpace(req.SourceSHA) == "" {
		return Receipt{}, fmt.Errorf("capsule runtime: id, owner, and source SHA are required")
	}
	if req.Workspace.ID == "" || req.Workspace.Generation == 0 {
		return Receipt{}, fmt.Errorf("capsule runtime: workspace handle is required")
	}
	if req.Purpose != PurposeChange && req.Purpose != PurposeWave && req.Purpose != PurposeRelease {
		return Receipt{}, fmt.Errorf("capsule runtime: invalid purpose %q", req.Purpose)
	}
	profile, ok := m.Definition.Profiles[req.Profile]
	if !ok {
		return Receipt{}, fmt.Errorf("capsule runtime: unknown profile %q", req.Profile)
	}
	if profile.Provider != "host" {
		return Receipt{}, fmt.Errorf("%w: %s", ErrUnsupportedProvider, profile.Provider)
	}
	ws, err := m.Workspaces.Resolve(ctx, req.Workspace, req.Owner)
	if err != nil {
		return Receipt{}, err
	}
	if ws.Head != req.SourceSHA {
		return Receipt{}, fmt.Errorf("%w: requested %s, workspace is %s", ErrSourceMismatch, req.SourceSHA, ws.Head)
	}
	now := m.now()
	r := Record{ID: req.ID, Owner: req.Owner, Generation: req.Workspace.Generation, SourceSHA: req.SourceSHA, SourceRef: ws.SourceRef, DefinitionDigest: m.Definition.Digest, Profile: req.Profile, Provider: profile.Provider, Purpose: req.Purpose, State: StateStarting, Workspace: req.Workspace, StartedAt: now}
	if req.TTL > 0 {
		r.ExpiresAt = now.Add(req.TTL)
	}
	if err := m.Store.Create(ctx, r); err != nil {
		return Receipt{}, err
	}
	allocated := []EndpointLease{}
	started := []Process{}
	fail := func(cause error) (Receipt, error) {
		for i := len(started) - 1; i >= 0; i-- {
			_ = started[i].Stop(ctx)
		}
		for i := len(allocated) - 1; i >= 0; i-- {
			_ = m.Endpoints.Release(ctx, allocated[i])
		}
		_, _ = m.Store.Update(ctx, req.ID, func(cur *Record) error { cur.State = StateFailed; cur.Failure = cause.Error(); return nil })
		return Receipt{}, cause
	}
	for _, name := range orderedServices(m.Definition.Services) {
		svc := m.Definition.Services[name]
		endpoints := make([]EndpointLease, 0, len(svc.Ports))
		env := map[string]string{}
		for _, role := range orderedPorts(svc.Ports) {
			port := svc.Ports[role]
			lease, err := m.Endpoints.Allocate(ctx, EndpointRequest{RuntimeID: req.ID, Owner: req.Owner, Generation: r.Generation, Service: name, Role: role, Protocol: port.Protocol, Exposure: port.Exposure, DefinitionDigest: m.Definition.Digest, SourceSHA: req.SourceSHA})
			if err != nil {
				return fail(fmt.Errorf("capsule runtime: allocate %s/%s: %w", name, role, err))
			}
			if lease.RuntimeID != req.ID || lease.Owner != req.Owner || lease.Generation != r.Generation || lease.Service != name || lease.Role != role || lease.Port <= 0 {
				_ = m.Endpoints.Release(ctx, lease)
				return fail(fmt.Errorf("capsule runtime: broker returned invalid lease for %s/%s", name, role))
			}
			allocated = append(allocated, lease)
			endpoints = append(endpoints, lease)
			env[port.Env] = fmt.Sprintf("%d", lease.Port)
		}
		proc, err := m.Processes.Start(ctx, ProcessSpec{RuntimeID: req.ID, Owner: req.Owner, Generation: r.Generation, Service: name, Directory: ws.Path, Argv: append([]string(nil), m.Definition.Commands[svc.Command].Argv...), Env: env})
		if err != nil {
			return fail(fmt.Errorf("capsule runtime: start %s: %w", name, err))
		}
		started = append(started, proc)
		receipt := ServiceReceipt{Name: name, ProcessID: proc.ID(), Endpoints: endpoints}
		if svc.Health != nil {
			endpoint, ok := endpointByRole(endpoints, svc.Health.Port)
			if !ok {
				return fail(fmt.Errorf("capsule runtime: health endpoint missing for %s", name))
			}
			healthCtx, cancel := context.WithTimeout(ctx, svc.Health.Timeout)
			err = m.Health.Check(healthCtx, HealthRequest{Service: name, Endpoint: endpoint, Health: *svc.Health})
			cancel()
			if err != nil {
				return fail(fmt.Errorf("capsule runtime: health %s: %w", name, err))
			}
			receipt.HealthPassed = true
		}
		for _, endpoint := range endpoints {
			attestation, err := m.Listeners.Attest(ctx, ListenerRequest{ProcessID: proc.ID(), Endpoint: endpoint})
			if err != nil {
				return fail(fmt.Errorf("capsule runtime: attest %s/%s: %w", name, endpoint.Role, err))
			}
			if !attestation.Verified || attestation.ListenerPID == "" {
				return fail(fmt.Errorf("%w: %s/%s", ErrListenerPolicy, name, endpoint.Role))
			}
			receipt.Attestations = append(receipt.Attestations, attestation)
		}
		_, err = m.Store.Update(ctx, req.ID, func(cur *Record) error { cur.Services = append(cur.Services, receipt); return nil })
		if err != nil {
			return fail(err)
		}
	}
	r, err = m.Store.Update(ctx, req.ID, func(cur *Record) error { cur.State = StateReady; return nil })
	if err != nil {
		return fail(err)
	}
	return Receipt{Schema: Schema, Record: r}, nil
}

func (m *Manager) Status(ctx context.Context, id string) (Receipt, error) {
	r, err := m.Store.Get(ctx, id)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Schema: Schema, Record: r}, nil
}
func (m *Manager) Stop(ctx context.Context, id, owner string) (Receipt, error) {
	r, err := m.Store.Get(ctx, id)
	if err != nil {
		return Receipt{}, err
	}
	if r.Owner != owner {
		return Receipt{}, fmt.Errorf("capsule runtime: owner mismatch")
	}
	if r.State == StateStopped {
		return Receipt{Schema: Schema, Record: r}, nil
	}
	for i := len(r.Services) - 1; i >= 0; i-- {
		p, ok := m.Processes.Process(r.Services[i].ProcessID)
		if !ok {
			return Receipt{}, fmt.Errorf("capsule runtime: process %q is missing", r.Services[i].ProcessID)
		}
		if err := p.Stop(ctx); err != nil {
			return Receipt{}, err
		}
		for _, e := range r.Services[i].Endpoints {
			if err := m.Endpoints.Release(ctx, e); err != nil {
				return Receipt{}, err
			}
		}
	}
	r, err = m.Store.Update(ctx, id, func(cur *Record) error { cur.State = StateStopped; cur.StoppedAt = m.now(); return nil })
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Schema: Schema, Record: r}, nil
}
func (m *Manager) Logs(ctx context.Context, id, service string) ([]byte, error) {
	r, err := m.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, s := range r.Services {
		if s.Name == service {
			p, ok := m.Processes.Process(s.ProcessID)
			if !ok {
				return nil, fmt.Errorf("capsule runtime: process %q is missing", s.ProcessID)
			}
			return p.Logs(ctx)
		}
	}
	return nil, fmt.Errorf("capsule runtime: service %q not found", service)
}
func (m *Manager) Reap(ctx context.Context) ([]Receipt, error) {
	records, err := m.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []Receipt{}
	for _, r := range records {
		if r.State == StateReady && !r.ExpiresAt.IsZero() && !m.now().Before(r.ExpiresAt) {
			stopped, err := m.Stop(ctx, r.ID, r.Owner)
			if err != nil {
				return out, err
			}
			out = append(out, stopped)
		}
	}
	return out, nil
}

func (m *Manager) ready() error {
	if _, err := Seal(m.Definition); err != nil {
		return err
	}
	if m.Workspaces == nil || m.Endpoints == nil || m.Processes == nil || m.Listeners == nil || m.Health == nil || m.Store == nil || m.NewID == nil {
		return fmt.Errorf("capsule runtime: manager dependencies are required")
	}
	return nil
}
func (m *Manager) now() time.Time {
	if m.Clock == nil {
		return time.Now().UTC()
	}
	return m.Clock.Now().UTC()
}
func orderedServices(services map[string]Service) []string {
	out := make([]string, 0, len(services))
	for name := range services {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
func orderedPorts(ports map[string]Port) []string {
	out := make([]string, 0, len(ports))
	for name := range ports {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
func endpointByRole(endpoints []EndpointLease, role string) (EndpointLease, bool) {
	for _, e := range endpoints {
		if e.Role == role {
			return e, true
		}
	}
	return EndpointLease{}, false
}
