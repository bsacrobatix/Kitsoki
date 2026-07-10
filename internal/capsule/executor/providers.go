package executor

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// HostProvider is the explicit host-current compatibility provider. It does
// not install tools or widen network policy; Prepare only validates the sealed
// envelope and advertised capability, while the supplied story task does the
// actual execution.
type HostProvider struct {
	Cap      Capabilities
	mu       sync.Mutex
	prepared map[string]Prepared
}

func NewHostProvider() *HostProvider {
	return &HostProvider{Cap: Capabilities{ID: "host", Placements: []string{"host"}, Isolation: "supervised", Networks: []string{"none", "replay", "live"}, Cancellable: false}, prepared: map[string]Prepared{}}
}
func (p *HostProvider) Describe(context.Context) (Capabilities, error) { return p.Cap, nil }
func (p *HostProvider) Prepare(_ context.Context, e Envelope) (Prepared, error) {
	sealed, err := Seal(e)
	if err != nil {
		return Prepared{}, err
	}
	if !supports(p.Cap.Networks, sealed.Policy.Network) {
		return Prepared{}, fmt.Errorf("capsule executor: host cannot satisfy network %s", sealed.Policy.Network)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if out := p.prepared[sealed.Digest]; out.ID != "" {
		return out, nil
	}
	out := Prepared{ID: "host-" + sealed.Digest[len(sealed.Digest)-12:], Envelope: sealed, Placement: "host", Applied: sealed.Policy}
	p.prepared[sealed.Digest] = out
	return out, nil
}
func (p *HostProvider) Run(ctx context.Context, prepared Prepared, task Task, sink EventSink) (Result, error) {
	return runTask(ctx, prepared, task, sink)
}
func (*HostProvider) Cancel(context.Context, string) error {
	return fmt.Errorf("capsule executor: host provider does not support cancellation")
}

// RemoteWorker is a one-shot transport seam. A queue, SSH invocation, GitHub
// Action, or long-lived streaming service can implement it without changing
// the envelope or result format.
type RemoteWorker interface {
	Describe(context.Context) (Capabilities, error)
	Run(context.Context, Prepared, Task, EventSink) (Result, error)
	Cancel(context.Context, string) error
}
type RemoteProvider struct {
	Worker   RemoteWorker
	mu       sync.Mutex
	prepared map[string]Prepared
}

func NewRemoteProvider(worker RemoteWorker) *RemoteProvider {
	return &RemoteProvider{Worker: worker, prepared: map[string]Prepared{}}
}
func (p *RemoteProvider) Describe(ctx context.Context) (Capabilities, error) {
	if p.Worker == nil {
		return Capabilities{}, fmt.Errorf("capsule executor: remote worker is required")
	}
	return p.Worker.Describe(ctx)
}
func (p *RemoteProvider) Prepare(ctx context.Context, e Envelope) (Prepared, error) {
	if p.Worker == nil {
		return Prepared{}, fmt.Errorf("capsule executor: remote worker is required")
	}
	sealed, err := Seal(e)
	if err != nil {
		return Prepared{}, err
	}
	cap, err := p.Worker.Describe(ctx)
	if err != nil {
		return Prepared{}, err
	}
	if !supports(cap.Networks, sealed.Policy.Network) {
		return Prepared{}, fmt.Errorf("capsule executor: remote worker cannot satisfy network %s", sealed.Policy.Network)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if out := p.prepared[sealed.Digest]; out.ID != "" {
		return out, nil
	}
	out := Prepared{ID: "remote-" + sealed.Digest[len(sealed.Digest)-12:], Envelope: sealed, Placement: first(cap.Placements, "remote"), Applied: sealed.Policy}
	p.prepared[sealed.Digest] = out
	return out, nil
}
func (p *RemoteProvider) Run(ctx context.Context, prepared Prepared, task Task, sink EventSink) (Result, error) {
	if p.Worker == nil {
		return Result{}, fmt.Errorf("capsule executor: remote worker is required")
	}
	result, err := p.Worker.Run(ctx, prepared, task, sink)
	result.ExecutionID = prepared.ID
	sort.Strings(result.Artifacts)
	return result, err
}
func (p *RemoteProvider) Cancel(ctx context.Context, id string) error {
	if p.Worker == nil {
		return fmt.Errorf("capsule executor: remote worker is required")
	}
	return p.Worker.Cancel(ctx, id)
}
func first(in []string, fallback string) string {
	if len(in) > 0 {
		return in[0]
	}
	return fallback
}

// FakeRemoteWorker is a deterministic streaming-worker double. It invokes the
// same task callback as host mode but marks provider facts as remote, allowing
// byte-for-byte result-parity coverage without a network or LLM.
type FakeRemoteWorker struct {
	Cap       Capabilities
	Cancelled map[string]bool
	mu        sync.Mutex
}

func NewFakeRemoteWorker() *FakeRemoteWorker {
	return &FakeRemoteWorker{Cap: Capabilities{ID: "fake-remote", Placements: []string{"fake-remote"}, Isolation: "supervised", Networks: []string{"none", "replay"}, Cancellable: true}, Cancelled: map[string]bool{}}
}
func (w *FakeRemoteWorker) Describe(context.Context) (Capabilities, error) { return w.Cap, nil }
func (w *FakeRemoteWorker) Run(ctx context.Context, p Prepared, task Task, sink EventSink) (Result, error) {
	return runTask(ctx, p, task, sink)
}
func (w *FakeRemoteWorker) Cancel(_ context.Context, id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Cancelled[id] = true
	return nil
}
func runTask(ctx context.Context, p Prepared, task Task, sink EventSink) (Result, error) {
	if task == nil {
		return Result{}, fmt.Errorf("capsule executor: task is required")
	}
	if sink != nil {
		_ = sink.Emit(ctx, Event{Kind: "capsule.executor.started", EnvelopeDigest: p.Envelope.Digest, ExecutionID: p.ID})
	}
	out, err := task(ctx, p)
	out.ExecutionID = p.ID
	sort.Strings(out.Artifacts)
	if sink != nil {
		kind := "capsule.executor.finished"
		if err != nil {
			kind = "capsule.executor.failed"
		}
		_ = sink.Emit(ctx, Event{Kind: kind, EnvelopeDigest: p.Envelope.Digest, ExecutionID: p.ID})
	}
	return out, err
}
