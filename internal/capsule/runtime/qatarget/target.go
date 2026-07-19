package qatarget

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"kitsoki/internal/capsule/runtime"
)

const Schema = "capsule-qa-runtime-target/v1"

// FailureKind is the common QA terminal taxonomy. Drivers should preserve this
// value in their terminal evidence instead of flattening infrastructure errors
// into product failures.
type FailureKind string

const (
	FailureProduct         FailureKind = "product"
	FailureAccess          FailureKind = "access"
	FailureWorker          FailureKind = "worker"
	FailureNetwork         FailureKind = "network"
	FailureModel           FailureKind = "model"
	FailureHarness         FailureKind = "harness"
	FailureRuntimeProvider FailureKind = "runtime-provider"
	FailureEndpointBroker  FailureKind = "endpoint-broker"
)

var (
	ErrInvalidTarget   = errors.New("capsule QA target: invalid target")
	ErrRuntime         = errors.New("capsule QA target: runtime unavailable")
	ErrEndpoint        = errors.New("capsule QA target: endpoint unavailable")
	ErrSourceMismatch  = errors.New("capsule QA target: source manifest mismatch")
	ErrProfileMismatch = errors.New("capsule QA target: profile mismatch")
)

// Target is the front-door-neutral envelope handed to a QA driver. Endpoint
// roles, rather than ports or URLs, are stable identity.
type Target struct {
	Schema               string `json:"schema"`
	RuntimeInstanceID    string `json:"runtime_instance_id"`
	SourceManifestDigest string `json:"source_manifest_digest"`
	BrowserEndpointRole  string `json:"browser_endpoint_role"`
	Persona              string `json:"persona"`
	Scenario             string `json:"scenario"`
	Rubric               string `json:"rubric"`
	Provider             string `json:"provider"`
	Profile              string `json:"profile"`
	DataSnapshot         string `json:"data_snapshot"`
	AccessPolicy         string `json:"access_policy"`
	FeedbackSink         string `json:"feedback_sink"`
}

// RuntimeStore returns the current durable runtime record at dispatch time.
type RuntimeStore interface {
	Get(context.Context, string) (runtime.Record, error)
}

// LeaseChecker verifies that an endpoint lease in the runtime receipt remains
// live and still belongs to exactly the same runtime generation.
type LeaseChecker interface {
	Healthy(context.Context, runtime.EndpointLease) error
}

type Clock interface{ Now() time.Time }

type Resolver struct {
	Runtimes RuntimeStore
	Leases   LeaseChecker
	Clock    Clock
}

// DispatchTarget is the resolved target recorded in terminal QA evidence.
// URL can only originate in a healthy runtime receipt lease.
type DispatchTarget struct {
	Schema               string `json:"schema"`
	RuntimeInstanceID    string `json:"runtime_instance_id"`
	RuntimeGeneration    uint64 `json:"runtime_generation"`
	SourceManifestDigest string `json:"source_manifest_digest"`
	BrowserEndpointRole  string `json:"browser_endpoint_role"`
	URL                  string `json:"url"`
	Persona              string `json:"persona"`
	Scenario             string `json:"scenario"`
	Rubric               string `json:"rubric"`
	Provider             string `json:"provider"`
	Profile              string `json:"profile"`
	DataSnapshot         string `json:"data_snapshot"`
	AccessPolicy         string `json:"access_policy"`
	FeedbackSink         string `json:"feedback_sink"`
}

func (r Resolver) Resolve(ctx context.Context, target Target) (DispatchTarget, error) {
	if err := target.Validate(); err != nil {
		return DispatchTarget{}, err
	}
	if r.Runtimes == nil || r.Leases == nil {
		return DispatchTarget{}, fmt.Errorf("%w: runtime store and lease checker are required", ErrInvalidTarget)
	}
	record, err := r.Runtimes.Get(ctx, target.RuntimeInstanceID)
	if err != nil {
		return DispatchTarget{}, fmt.Errorf("%w: %v", ErrRuntime, err)
	}
	if record.State != runtime.StateReady || expired(record.ExpiresAt, r.now()) {
		return DispatchTarget{}, fmt.Errorf("%w: instance %q is not ready", ErrRuntime, target.RuntimeInstanceID)
	}
	if record.SourceManifestDigest != target.SourceManifestDigest {
		return DispatchTarget{}, fmt.Errorf("%w: instance %q", ErrSourceMismatch, target.RuntimeInstanceID)
	}
	if record.Provider != target.Provider || record.Profile != target.Profile {
		return DispatchTarget{}, fmt.Errorf("%w: instance %q", ErrProfileMismatch, target.RuntimeInstanceID)
	}
	endpoint, err := browserEndpoint(record, target.BrowserEndpointRole, r.now())
	if err != nil {
		return DispatchTarget{}, err
	}
	if err := r.Leases.Healthy(ctx, endpoint); err != nil {
		return DispatchTarget{}, fmt.Errorf("%w: %v", ErrEndpoint, err)
	}
	return DispatchTarget{Schema: Schema, RuntimeInstanceID: record.ID, RuntimeGeneration: record.Generation, SourceManifestDigest: record.SourceManifestDigest, BrowserEndpointRole: target.BrowserEndpointRole, URL: endpoint.URL, Persona: target.Persona, Scenario: target.Scenario, Rubric: target.Rubric, Provider: target.Provider, Profile: target.Profile, DataSnapshot: target.DataSnapshot, AccessPolicy: target.AccessPolicy, FeedbackSink: target.FeedbackSink}, nil
}

func (t Target) Validate() error {
	if t.Schema != Schema {
		return fmt.Errorf("%w: schema must be %q", ErrInvalidTarget, Schema)
	}
	for name, value := range map[string]string{"runtime_instance_id": t.RuntimeInstanceID, "source_manifest_digest": t.SourceManifestDigest, "browser_endpoint_role": t.BrowserEndpointRole, "persona": t.Persona, "scenario": t.Scenario, "rubric": t.Rubric, "provider": t.Provider, "profile": t.Profile, "data_snapshot": t.DataSnapshot, "access_policy": t.AccessPolicy, "feedback_sink": t.FeedbackSink} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidTarget, name)
		}
	}
	if !strings.HasPrefix(t.SourceManifestDigest, "sha256:") {
		return fmt.Errorf("%w: source_manifest_digest must be a sha256 digest", ErrInvalidTarget)
	}
	return nil
}

func browserEndpoint(record runtime.Record, role string, now time.Time) (runtime.EndpointLease, error) {
	var found []runtime.EndpointLease
	for _, service := range record.Services {
		if !service.HealthPassed {
			continue
		}
		for _, endpoint := range service.Endpoints {
			if endpoint.Role == role {
				found = append(found, endpoint)
			}
		}
	}
	if len(found) != 1 {
		return runtime.EndpointLease{}, fmt.Errorf("%w: browser endpoint role %q must resolve exactly once", ErrEndpoint, role)
	}
	endpoint := found[0]
	if endpoint.RuntimeID != record.ID || endpoint.Generation != record.Generation || expired(endpoint.ExpiresAt, now) || (endpoint.Exposure != "review" && endpoint.Exposure != "public") {
		return runtime.EndpointLease{}, fmt.Errorf("%w: browser endpoint role %q is not a healthy review lease", ErrEndpoint, role)
	}
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return runtime.EndpointLease{}, fmt.Errorf("%w: browser endpoint role %q has no browser URL", ErrEndpoint, role)
	}
	return endpoint, nil
}

func expired(at time.Time, now time.Time) bool { return !at.IsZero() && !now.Before(at) }
func (r Resolver) now() time.Time {
	if r.Clock == nil {
		return time.Now().UTC()
	}
	return r.Clock.Now().UTC()
}

// FailureOf maps resolver failures into the terminal QA taxonomy.
func FailureOf(err error) FailureKind {
	switch {
	case errors.Is(err, ErrEndpoint):
		return FailureEndpointBroker
	case errors.Is(err, ErrRuntime), errors.Is(err, ErrSourceMismatch), errors.Is(err, ErrProfileMismatch):
		return FailureRuntimeProvider
	default:
		return FailureHarness
	}
}
