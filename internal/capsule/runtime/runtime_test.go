package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"kitsoki/internal/capsule/control"
)

func TestLoadStrictDeclaration(t *testing.T) {
	def, err := Load([]byte(`schema: capsule-runtime/v1
commands: {serve: {argv: [server]}}
services: {web: {command: serve, ports: {http: {protocol: http, env: PORT, exposure: review}}, health: {port: http, timeout: 1s}}}
profiles: {local: {provider: host}}
`))
	if err != nil || def.Digest == "" {
		t.Fatalf("Load() = %#v, %v", def, err)
	}
	_, err = Load([]byte(`schema: capsule-runtime/v1
unknown: no
commands: {serve: {argv: [server]}}
services: {web: {command: serve}}
profiles: {local: {provider: host}}
`))
	if err == nil {
		t.Fatal("Load accepted unknown field")
	}
}

func TestManagerUpBindsExactSourceAndStopsOnlyOwnedProcesses(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)}
	processes := &fakeLauncher{}
	broker := &fakeBroker{}
	m := testManager(clock, processes, broker, fakeListener{verified: true})
	r, err := m.Up(context.Background(), UpRequest{ID: "rt-1", Owner: "owner", SourceSHA: "abc", Profile: "local", Purpose: PurposeChange, TTL: time.Minute, Workspace: control.Handle{ID: "ws", Generation: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Record.State != StateReady || r.Record.SourceManifestDigest != SourceManifestDigest("refs/heads/review", "abc") || len(r.Record.Services) != 1 || !r.Record.Services[0].HealthPassed {
		t.Fatalf("unexpected receipt: %#v", r)
	}
	if len(broker.requests) != 1 || broker.requests[0].DefinitionDigest != r.Record.DefinitionDigest || broker.requests[0].Generation != 7 {
		t.Fatalf("broker request not bound: %#v", broker.requests)
	}
	if got := processes.specs[0]; got.Argv[0] != "server" || got.Env["PORT"] != "43101" || got.Owner != "owner" {
		t.Fatalf("unexpected process spec: %#v", got)
	}
	if _, err := m.Stop(context.Background(), "rt-1", "other"); err == nil {
		t.Fatal("Stop allowed other owner")
	}
	stopped, err := m.Stop(context.Background(), "rt-1", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Record.State != StateStopped || !processes.processes["rt-1-web-7"].stopped || len(broker.released) != 1 {
		t.Fatalf("stop was not scoped: %#v %#v", processes, broker)
	}
}

func TestManagerRejectsWrongSHAAndListenerFailureReleasesResources(t *testing.T) {
	m := testManager(&fakeClock{}, &fakeLauncher{}, &fakeBroker{}, fakeListener{verified: true})
	_, err := m.Up(context.Background(), UpRequest{ID: "wrong", Owner: "owner", SourceSHA: "other", Profile: "local", Purpose: PurposeChange, Workspace: control.Handle{ID: "ws", Generation: 1}})
	if !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("expected source mismatch, got %v", err)
	}
	broker := &fakeBroker{}
	processes := &fakeLauncher{}
	m = testManager(&fakeClock{}, processes, broker, fakeListener{})
	_, err = m.Up(context.Background(), UpRequest{ID: "bad-listener", Owner: "owner", SourceSHA: "abc", Profile: "local", Purpose: PurposeWave, Workspace: control.Handle{ID: "ws", Generation: 1}})
	if !errors.Is(err, ErrListenerPolicy) || len(broker.released) != 1 || !processes.processes["bad-listener-web-1"].stopped {
		t.Fatalf("listener failure did not clean up: err=%v broker=%#v processes=%#v", err, broker, processes)
	}
}

func TestReapAndUnsupportedProvider(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)}
	processes := &fakeLauncher{}
	m := testManager(clock, processes, &fakeBroker{}, fakeListener{verified: true})
	_, err := m.Up(context.Background(), UpRequest{ID: "expires", Owner: "owner", SourceSHA: "abc", Profile: "local", Purpose: PurposeRelease, TTL: time.Second, Workspace: control.Handle{ID: "ws", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	got, err := m.Reap(context.Background())
	if err != nil || len(got) != 1 || got[0].Record.State != StateStopped {
		t.Fatalf("Reap = %#v, %v", got, err)
	}
	m.Definition.Profiles["docker"] = Profile{Provider: "docker"}
	_, err = m.Up(context.Background(), UpRequest{ID: "docker", Owner: "owner", SourceSHA: "abc", Profile: "docker", Purpose: PurposeChange, Workspace: control.Handle{ID: "ws", Generation: 1}})
	if !errors.Is(err, ErrUnsupportedProvider) {
		t.Fatalf("expected unsupported provider, got %v", err)
	}
}

func TestFileStorePersistsRuntimeReceipt(t *testing.T) {
	path := t.TempDir() + "/runtime-records.json"
	store := NewFileStore(path)
	if err := store.Create(context.Background(), Record{ID: "durable", State: StateReady, SourceSHA: "abc"}); err != nil {
		t.Fatal(err)
	}
	got, err := NewFileStore(path).Get(context.Background(), "durable")
	if err != nil || got.SourceSHA != "abc" || got.State != StateReady {
		t.Fatalf("persisted record = %#v, %v", got, err)
	}
}

func testManager(clock *fakeClock, processes *fakeLauncher, broker *fakeBroker, listener ListenerInspector) *Manager {
	return &Manager{Definition: testDefinition(), Workspaces: fakeWorkspace{}, Endpoints: broker, Processes: processes, Listeners: listener, Health: fakeHealth{}, Store: NewMemoryStore(), Clock: clock, NewID: func() string { return "generated" }}
}
func testDefinition() Definition {
	d, err := Load([]byte(`schema: capsule-runtime/v1
commands: {serve: {argv: [server, --strict-port]}}
services: {web: {command: serve, ports: {http: {protocol: http, env: PORT, exposure: review}}, health: {port: http, path: /healthz, timeout: 1s}}}
profiles: {local: {provider: host}}
`))
	if err != nil {
		panic(err)
	}
	return d
}

type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time {
	if f.now.IsZero() {
		return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return f.now
}

type fakeWorkspace struct{}

func (fakeWorkspace) Resolve(context.Context, control.Handle, string) (Workspace, error) {
	return Workspace{Path: "/workspace", Head: "abc", SourceRef: "refs/heads/review"}, nil
}

type fakeBroker struct {
	requests []EndpointRequest
	released []EndpointLease
}

func (b *fakeBroker) Allocate(_ context.Context, r EndpointRequest) (EndpointLease, error) {
	b.requests = append(b.requests, r)
	return EndpointLease{ID: "lease-" + r.Role, Port: 43101, RuntimeID: r.RuntimeID, Owner: r.Owner, Generation: r.Generation, Service: r.Service, Role: r.Role, Exposure: r.Exposure}, nil
}
func (b *fakeBroker) Release(_ context.Context, l EndpointLease) error {
	b.released = append(b.released, l)
	return nil
}

type fakeLauncher struct {
	specs     []ProcessSpec
	processes map[string]*fakeProcess
}

func (l *fakeLauncher) Start(_ context.Context, s ProcessSpec) (Process, error) {
	if l.processes == nil {
		l.processes = map[string]*fakeProcess{}
	}
	l.specs = append(l.specs, s)
	p := &fakeProcess{id: fmt.Sprintf("%s-%s-%d", s.RuntimeID, s.Service, s.Generation)}
	l.processes[p.id] = p
	return p, nil
}
func (l *fakeLauncher) Process(id string) (Process, bool) { p, ok := l.processes[id]; return p, ok }

type fakeProcess struct {
	id      string
	stopped bool
}

func (p *fakeProcess) ID() string                         { return p.id }
func (p *fakeProcess) Stop(context.Context) error         { p.stopped = true; return nil }
func (*fakeProcess) Logs(context.Context) ([]byte, error) { return []byte("log"), nil }

type fakeListener struct{ verified bool }

func (f fakeListener) Attest(context.Context, ListenerRequest) (Attestation, error) {
	return Attestation{ListenerPID: "42", Verified: f.verified}, nil
}

type fakeHealth struct{}

func (fakeHealth) Check(context.Context, HealthRequest) error { return nil }
