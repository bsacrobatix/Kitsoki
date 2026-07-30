package environment

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"kitsoki/internal/clock"
)

// Fake is a deterministic, stateful Host for Story and adapter conformance
// tests. Time never advances while a call runs; the test controls it through
// the injected clock. This exposes retry/polling bugs without sleeps or a
// network provider.
type Fake struct {
	mu sync.Mutex

	clock         clock.Clock
	resources     map[string]Resource
	operations    map[string]*fakeOperation
	idempotency   map[string]string
	programs      map[string][][]FakeStep
	nextOperation int
	nextRequest   int
}

type FakeOption func(*Fake)

func WithClock(value clock.Clock) FakeOption {
	return func(f *Fake) { f.clock = value }
}

func WithResource(resource Resource) FakeOption {
	return func(f *Fake) { f.resources[resource.Ref] = cloneResource(resource) }
}

func NewFake(options ...FakeOption) *Fake {
	fake := &Fake{
		clock: clock.Real(), resources: make(map[string]Resource),
		operations: make(map[string]*fakeOperation), idempotency: make(map[string]string), programs: make(map[string][][]FakeStep),
	}
	for _, option := range options {
		option(fake)
	}
	return fake
}

// FakeStep is a deterministic operation transition measured from Submit's
// accepted time. The last due step wins. Applying Resource writes the visible
// resource state, which lets a test model stale-but-healthy postconditions.
type FakeStep struct {
	After    time.Duration
	State    OperationState
	Outcome  Outcome
	Resource *Resource
}

// Program configures the next Submit of an action kind. Multiple programs for
// the same kind are consumed in FIFO order; absent programs complete instantly.
type Program struct {
	ActionKind string
	Steps      []FakeStep
}

// Enqueue appends a deterministic program for a future action kind.
func (f *Fake) Enqueue(program Program) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if program.ActionKind == "" {
		panic("environment fake: program action kind is required")
	}
	f.programs[program.ActionKind] = append(f.programs[program.ActionKind], cloneSteps(program.Steps))
}

// Journal returns a stable copy of accepted operations for assertions.
func (f *Fake) Journal() []Operation {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Operation, 0, len(f.operations))
	for _, operation := range f.operations {
		f.advanceLocked(operation)
		out = append(out, cloneOperation(operation.Operation))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

func (f *Fake) Read(_ context.Context, request ReadRequest) (ReadResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resource, ok := f.resources[request.ResourceRef]
	if !ok {
		return ReadResult{Outcome: Blocked(ReasonNotFound, "environment resource was not found", "check the profile topology and resource reference", Evidence{Kind: "resource", Ref: request.ResourceRef})}, nil
	}
	return ReadResult{Outcome: Passed(Evidence{Kind: "resource", Ref: resource.Ref}), Resource: cloneResource(resource)}, nil
}

func (f *Fake) Submit(_ context.Context, request SubmitRequest) (SubmitResult, error) {
	if request.Action.Kind == "" || request.Action.TargetRef == "" || request.IdempotencyKey == "" {
		return SubmitResult{Outcome: Blocked(ReasonInvalidRequest, "action kind, target reference, and idempotency key are required", "supply the stable action identity from the Story planner")}, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.idempotency[request.IdempotencyKey]; ok {
		op := f.operations[existing]
		f.advanceLocked(op)
		return SubmitResult{Outcome: Passed(Evidence{Kind: "operation", Ref: existing, Detail: "idempotent replay"}), Receipt: Receipt{RequestRef: fmt.Sprintf("request-%d", f.nextRequest), OperationRef: existing, IdempotencyKey: request.IdempotencyKey, AcceptedAt: op.Submitted, Replayed: true}}, nil
	}
	f.nextRequest++
	f.nextOperation++
	ref := fmt.Sprintf("operation-%d", f.nextOperation)
	now := f.clock.Now()
	operation := &fakeOperation{Operation: Operation{Ref: ref, Action: cloneAction(request.Action), State: OperationPending, Submitted: now, Updated: now, Outcome: Blocked(ReasonOperationPending, "environment operation is pending", "poll the operation receipt")}, steps: f.takeProgramLocked(request.Action.Kind)}
	f.operations[ref] = operation
	f.idempotency[request.IdempotencyKey] = ref
	f.advanceLocked(operation)
	return SubmitResult{Outcome: Passed(Evidence{Kind: "operation", Ref: ref}), Receipt: Receipt{RequestRef: fmt.Sprintf("request-%d", f.nextRequest), OperationRef: ref, IdempotencyKey: request.IdempotencyKey, AcceptedAt: now}}, nil
}

func (f *Fake) Poll(_ context.Context, request PollRequest) (PollResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	operation, ok := f.operations[request.OperationRef]
	if !ok {
		return PollResult{Outcome: Blocked(ReasonNotFound, "environment operation was not found", "retain the submit receipt and poll its operation reference", Evidence{Kind: "operation", Ref: request.OperationRef})}, nil
	}
	f.advanceLocked(operation)
	return PollResult{Outcome: operation.Outcome, Operation: cloneOperation(operation.Operation)}, nil
}

func (f *Fake) Verify(_ context.Context, request VerifyRequest) (VerifyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	condition := request.Postcondition
	resource, ok := f.resources[condition.ResourceRef]
	if !ok {
		return VerifyResult{Outcome: Blocked(ReasonNotFound, "environment resource was not found", "check the profile topology and resource reference", Evidence{Kind: "resource", Ref: condition.ResourceRef})}, nil
	}
	evidence := []Evidence{{Kind: "resource", Ref: resource.Ref}}
	if condition.RequireHealthy && !resource.Healthy {
		return VerifyResult{Outcome: Blocked(ReasonNotHealthy, "environment resource is not healthy", "repair the resource and verify health before deployment", evidence...), Resource: cloneResource(resource)}, nil
	}
	if condition.RequireCurrent && !resource.Current {
		return VerifyResult{Outcome: Blocked(ReasonNotCurrent, "environment resource is healthy but not current", "reconcile the requested deployment and verify the current version", evidence...), Resource: cloneResource(resource)}, nil
	}
	for key, want := range condition.Attributes {
		if got := resource.Attributes[key]; got != want {
			attributeEvidence := append(evidence, Evidence{Kind: "attribute", Ref: key, Detail: got})
			return VerifyResult{
				Outcome: Blocked(
					ReasonAttributeMismatch,
					fmt.Sprintf("environment postcondition attribute %q is %q, want %q", key, got, want),
					"reconcile the topology and verify the expected attribute",
					attributeEvidence...,
				),
				Resource: cloneResource(resource),
			}, nil
		}
	}
	return VerifyResult{Outcome: Passed(evidence...), Resource: cloneResource(resource)}, nil
}

type fakeOperation struct {
	Operation
	steps []FakeStep
}

func (f *Fake) takeProgramLocked(kind string) []FakeStep {
	all := f.programs[kind]
	if len(all) == 0 {
		return []FakeStep{{State: OperationSucceeded, Outcome: Passed()}}
	}
	f.programs[kind] = all[1:]
	return all[0]
}

func (f *Fake) advanceLocked(operation *fakeOperation) {
	now := f.clock.Now()
	for _, step := range operation.steps {
		if now.Before(operation.Submitted.Add(step.After)) {
			continue
		}
		if step.State != "" {
			operation.State = step.State
		}
		if step.Outcome.Status != "" {
			operation.Outcome = step.Outcome
		}
		operation.Updated = now
		if step.Resource != nil {
			f.resources[step.Resource.Ref] = cloneResource(*step.Resource)
		}
	}
}

func cloneAction(value Action) Action {
	return Action{Kind: value.Kind, TargetRef: value.TargetRef, Input: cloneStrings(value.Input)}
}
func cloneResource(value Resource) Resource {
	return Resource{Ref: value.Ref, Kind: value.Kind, Healthy: value.Healthy, Current: value.Current, Attributes: cloneStrings(value.Attributes)}
}
func cloneOperation(value Operation) Operation {
	return Operation{Ref: value.Ref, Action: cloneAction(value.Action), State: value.State, Submitted: value.Submitted, Updated: value.Updated, Outcome: value.Outcome}
}
func cloneStrings(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}
func cloneSteps(value []FakeStep) []FakeStep {
	out := make([]FakeStep, len(value))
	for i, step := range value {
		out[i] = step
		if step.Resource != nil {
			copy := cloneResource(*step.Resource)
			out[i].Resource = &copy
		}
	}
	return out
}
