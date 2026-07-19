package vmpool

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Fake is an in-memory Provisioner double for tests: no network, no cloud
// account, fully deterministic. It is safe for concurrent use.
type Fake struct {
	// Now stamps CreatedAt on new instances; nil defaults to time.Now.
	Now func() time.Time
	// Images seeds ResolveImage: ref -> resolved image ID. A ref absent from
	// the map passes through unchanged, matching DO's slug pass-through.
	Images map[string]string

	mu        sync.Mutex
	nextID    int
	instances map[string]Instance
	created   []CreateParams
	destroyed []string
	snapshots []SnapshotCall
}

var _ Provisioner = (*Fake)(nil)

// SnapshotCall records one Fake.Snapshot invocation.
type SnapshotCall struct {
	InstanceID string
	Name       string
	ImageID    string
}

// NewFake builds an empty Fake provisioner.
func NewFake() *Fake {
	return &Fake{instances: map[string]Instance{}}
}

func (f *Fake) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Create allocates the next sequential instance ID. New instances start with
// provider status "new" and no IPs, awaiting Activate or Fail.
func (f *Fake) Create(_ context.Context, params CreateParams) (Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	inst := Instance{
		ID:        strconv.Itoa(f.nextID),
		Name:      params.Name,
		Status:    "new",
		CreatedAt: f.now(),
		Tags:      append([]string(nil), params.Tags...),
	}
	f.instances[inst.ID] = inst
	f.created = append(f.created, copyCreateParams(params))
	return inst, nil
}

// Activate transitions instanceID to provider status "active" with the given
// IPs. It is a no-op for an unknown instanceID.
func (f *Fake) Activate(instanceID, publicIP, privateIP string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.instances[instanceID]
	if !ok {
		return
	}
	inst.Status = "active"
	inst.PublicIP = publicIP
	inst.PrivateIP = privateIP
	f.instances[instanceID] = inst
}

// Fail transitions instanceID to provider status "errored". It is a no-op
// for an unknown instanceID.
func (f *Fake) Fail(instanceID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.instances[instanceID]
	if !ok {
		return
	}
	inst.Status = "errored"
	f.instances[instanceID] = inst
}

// Get reports found=false, nil error for an unknown instanceID.
func (f *Fake) Get(_ context.Context, instanceID string) (Instance, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.instances[instanceID]
	return inst, ok, nil
}

// Destroy is idempotent: destroying an unknown or already-destroyed instance
// is not an error. Every call is recorded in Destroyed(), including repeats.
func (f *Fake) Destroy(_ context.Context, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.instances, instanceID)
	f.destroyed = append(f.destroyed, instanceID)
	return nil
}

// ListByTag returns instances carrying tag, ordered by instance ID.
func (f *Fake) ListByTag(_ context.Context, tag string) ([]Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Instance
	for _, inst := range f.instances {
		for _, t := range inst.Tags {
			if t == tag {
				out = append(out, inst)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ii, _ := strconv.Atoi(out[i].ID)
		jj, _ := strconv.Atoi(out[j].ID)
		return ii < jj
	})
	return out, nil
}

// Snapshot returns a deterministic image ID derived from instanceID and
// name, and records the call for introspection via Snapshots().
func (f *Fake) Snapshot(_ context.Context, instanceID, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.instances[instanceID]; !ok {
		return "", fmt.Errorf("vmpool: fake snapshot: instance %q not found", instanceID)
	}
	imageID := fmt.Sprintf("snap-%s-%s", instanceID, name)
	f.snapshots = append(f.snapshots, SnapshotCall{InstanceID: instanceID, Name: name, ImageID: imageID})
	return imageID, nil
}

// ResolveImage consults Images first; a ref absent from the map passes
// through unchanged.
func (f *Fake) ResolveImage(_ context.Context, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if resolved, ok := f.Images[ref]; ok {
		return resolved, nil
	}
	return ref, nil
}

// Created returns a copy of every CreateParams passed to Create, in call
// order.
func (f *Fake) Created() []CreateParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]CreateParams, len(f.created))
	for i, p := range f.created {
		out[i] = copyCreateParams(p)
	}
	return out
}

// Destroyed returns every instance ID passed to Destroy, in call order.
func (f *Fake) Destroyed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.destroyed...)
}

// Snapshots returns a copy of every recorded Snapshot call, in call order.
func (f *Fake) Snapshots() []SnapshotCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SnapshotCall(nil), f.snapshots...)
}

func copyCreateParams(p CreateParams) CreateParams {
	out := p
	out.SSHKeyIDs = append([]string(nil), p.SSHKeyIDs...)
	out.Tags = append([]string(nil), p.Tags...)
	return out
}
