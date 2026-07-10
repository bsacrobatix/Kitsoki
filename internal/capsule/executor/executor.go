package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
)

const EnvelopeSchema = "capsule-execution-envelope/v1"

type Capabilities struct {
	ID          string   `json:"id"`
	Placements  []string `json:"placements"`
	Isolation   string   `json:"isolation"`
	Networks    []string `json:"networks"`
	Cancellable bool     `json:"cancellable"`
}
type Policy struct {
	Network        string `json:"network"`
	MinimumSandbox string `json:"minimum_sandbox"`
	ExternalWrite  string `json:"external_write"`
}
type Envelope struct {
	Schema           string           `json:"schema"`
	JobID            string           `json:"job_id"`
	ProjectID        string           `json:"project_id"`
	DefinitionDigest string           `json:"definition_digest"`
	Instance         control.Handle   `json:"instance"`
	SourceDigest     string           `json:"source_digest"`
	StoryPath        string           `json:"story_path"`
	StoryDigest      string           `json:"story_digest"`
	Environment      environment.Lock `json:"environment"`
	Trigger          map[string]any   `json:"trigger"`
	Policy           Policy           `json:"policy"`
	Digest           string           `json:"digest"`
}
type Event struct {
	Kind           string         `json:"kind"`
	EnvelopeDigest string         `json:"envelope_digest"`
	ExecutionID    string         `json:"execution_id,omitempty"`
	Fields         map[string]any `json:"fields,omitempty"`
}
type EventSink interface {
	Emit(context.Context, Event) error
}
type EventSinkFunc func(context.Context, Event) error

func (f EventSinkFunc) Emit(ctx context.Context, e Event) error { return f(ctx, e) }

type Prepared struct {
	ID        string   `json:"id"`
	Envelope  Envelope `json:"envelope"`
	Placement string   `json:"placement"`
	Applied   Policy   `json:"applied_policy"`
}
type Result struct {
	ExecutionID     string            `json:"execution_id"`
	ExitCode        int               `json:"exit_code"`
	VerdictArtifact string            `json:"verdict_artifact,omitempty"`
	Artifacts       []string          `json:"artifacts,omitempty"`
	Provider        map[string]string `json:"provider,omitempty"`
}
type Task func(context.Context, Prepared) (Result, error)
type Provider interface {
	Describe(context.Context) (Capabilities, error)
	Prepare(context.Context, Envelope) (Prepared, error)
	Run(context.Context, Prepared, Task, EventSink) (Result, error)
	Cancel(context.Context, string) error
}

func Seal(e Envelope) (Envelope, error) {
	if e.Schema == "" {
		e.Schema = EnvelopeSchema
	}
	if e.Schema != EnvelopeSchema {
		return Envelope{}, fmt.Errorf("capsule executor: schema %q", e.Schema)
	}
	if e.JobID == "" || e.ProjectID == "" || e.StoryDigest == "" || e.Environment.Digest == "" {
		return Envelope{}, fmt.Errorf("capsule executor: job, project, story, and environment lock are required")
	}
	e.Digest = ""
	raw, err := json.Marshal(e)
	if err != nil {
		return Envelope{}, err
	}
	sum := sha256.Sum256(raw)
	e.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return e, nil
}

// FakeProvider proves local/remote parity without a network, container, or
// model. It records the exact immutable envelope supplied to Prepare.
type FakeProvider struct {
	Cap       Capabilities
	Placement string
	mu        sync.Mutex
	prepared  map[string]Prepared
	Cancelled map[string]bool
}

func NewFakeProvider(id string) *FakeProvider {
	return &FakeProvider{Cap: Capabilities{ID: id, Placements: []string{"fake"}, Isolation: "supervised", Networks: []string{"none", "replay"}, Cancellable: true}, Placement: "fake", prepared: map[string]Prepared{}, Cancelled: map[string]bool{}}
}
func (p *FakeProvider) Describe(context.Context) (Capabilities, error) { return p.Cap, nil }
func (p *FakeProvider) Prepare(ctx context.Context, e Envelope) (Prepared, error) {
	sealed, err := Seal(e)
	if err != nil {
		return Prepared{}, err
	}
	if !supports(p.Cap.Networks, sealed.Policy.Network) {
		return Prepared{}, fmt.Errorf("capsule executor: provider %s cannot satisfy network %s", p.Cap.ID, sealed.Policy.Network)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing := p.prepared[sealed.Digest]; existing.ID != "" {
		return existing, nil
	}
	out := Prepared{ID: "fake-" + sealed.Digest[len(sealed.Digest)-12:], Envelope: sealed, Placement: p.Placement, Applied: sealed.Policy}
	p.prepared[sealed.Digest] = out
	return out, nil
}
func (p *FakeProvider) Run(ctx context.Context, prepared Prepared, task Task, sink EventSink) (Result, error) {
	if sink != nil {
		_ = sink.Emit(ctx, Event{Kind: "capsule.executor.started", EnvelopeDigest: prepared.Envelope.Digest, ExecutionID: prepared.ID})
	}
	if task == nil {
		return Result{}, errors.New("capsule executor: task is required")
	}
	result, err := task(ctx, prepared)
	result.ExecutionID = prepared.ID
	sort.Strings(result.Artifacts)
	if sink != nil {
		kind := "capsule.executor.finished"
		if err != nil {
			kind = "capsule.executor.failed"
		}
		_ = sink.Emit(ctx, Event{Kind: kind, EnvelopeDigest: prepared.Envelope.Digest, ExecutionID: prepared.ID})
	}
	return result, err
}
func (p *FakeProvider) Cancel(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Cancelled[id] = true
	return nil
}
func supports(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

var _ = time.Now // kept available for provider implementations that report attempt timing.
