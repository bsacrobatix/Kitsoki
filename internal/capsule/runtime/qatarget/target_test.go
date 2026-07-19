package qatarget

import (
	"context"
	"errors"
	"testing"
	"time"

	"kitsoki/internal/capsule/runtime"
)

func TestResolveBindsEvidenceToHealthyReceipt(t *testing.T) {
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	record := readyRecord(now)
	resolver := Resolver{Runtimes: records{"rt-1": record}, Leases: leaseCheck{}, Clock: fixedClock{now}}
	got, err := resolver.Resolve(context.Background(), target(record))
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "http://127.0.0.1:43101" || got.SourceManifestDigest != record.SourceManifestDigest || got.RuntimeGeneration != 4 {
		t.Fatalf("dispatch target was not receipt-bound: %#v", got)
	}
}

func TestResolveRejectsFallbacksAndStaleRuntimeState(t *testing.T) {
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	record := readyRecord(now)
	cases := []struct {
		name   string
		mutate func(*runtime.Record, *Target)
		want   FailureKind
	}{
		{"missing-url", func(r *runtime.Record, _ *Target) { r.Services[0].Endpoints[0].URL = "" }, FailureEndpointBroker},
		{"expired-lease", func(r *runtime.Record, _ *Target) { r.Services[0].Endpoints[0].ExpiresAt = now }, FailureEndpointBroker},
		{"unhealthy-service", func(r *runtime.Record, _ *Target) { r.Services[0].HealthPassed = false }, FailureEndpointBroker},
		{"source-mismatch", func(_ *runtime.Record, t *Target) { t.SourceManifestDigest = "sha256:other" }, FailureRuntimeProvider},
		{"stopped", func(r *runtime.Record, _ *Target) { r.State = runtime.StateStopped }, FailureRuntimeProvider},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, in := record, target(record)
			tc.mutate(&r, &in)
			_, err := (Resolver{Runtimes: records{"rt-1": r}, Leases: leaseCheck{}, Clock: fixedClock{now}}).Resolve(context.Background(), in)
			if err == nil || FailureOf(err) != tc.want {
				t.Fatalf("Resolve() error=%v failure=%s want %s", err, FailureOf(err), tc.want)
			}
		})
	}
}

func TestResolveRequiresLiveLeaseAndNoAmbiguousRole(t *testing.T) {
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	record := readyRecord(now)
	_, err := (Resolver{Runtimes: records{"rt-1": record}, Leases: leaseCheck{err: errors.New("released")}, Clock: fixedClock{now}}).Resolve(context.Background(), target(record))
	if FailureOf(err) != FailureEndpointBroker {
		t.Fatalf("released lease error=%v failure=%s", err, FailureOf(err))
	}
	record.Services = append(record.Services, record.Services[0])
	_, err = (Resolver{Runtimes: records{"rt-1": record}, Leases: leaseCheck{}, Clock: fixedClock{now}}).Resolve(context.Background(), target(record))
	if FailureOf(err) != FailureEndpointBroker {
		t.Fatalf("ambiguous endpoint error=%v failure=%s", err, FailureOf(err))
	}
}

func TestTargetValidateRequiresCompleteEnvelope(t *testing.T) {
	record := readyRecord(time.Now().UTC())
	in := target(record)
	in.FeedbackSink = ""
	if err := in.Validate(); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("Validate() = %v", err)
	}
}

func readyRecord(now time.Time) runtime.Record {
	return runtime.Record{ID: "rt-1", Generation: 4, State: runtime.StateReady, SourceSHA: "abc", SourceRef: "refs/heads/wave", SourceManifestDigest: runtime.SourceManifestDigest("refs/heads/wave", "abc"), Profile: "local", Provider: "host", Services: []runtime.ServiceReceipt{{Name: "portal", HealthPassed: true, Endpoints: []runtime.EndpointLease{{ID: "lease-web", RuntimeID: "rt-1", Generation: 4, Role: "browser", Exposure: "review", URL: "http://127.0.0.1:43101", ExpiresAt: now.Add(time.Minute)}}}}}
}
func target(record runtime.Record) Target {
	return Target{Schema: Schema, RuntimeInstanceID: record.ID, SourceManifestDigest: record.SourceManifestDigest, BrowserEndpointRole: "browser", Persona: "maintainer", Scenario: "review-wave", Rubric: "qa/v1", Provider: "host", Profile: "local", DataSnapshot: "clean/v1", AccessPolicy: "authenticated", FeedbackSink: "local"}
}

type records map[string]runtime.Record

func (r records) Get(_ context.Context, id string) (runtime.Record, error) { return r[id], nil }

type leaseCheck struct{ err error }

func (l leaseCheck) Healthy(context.Context, runtime.EndpointLease) error { return l.err }

type fixedClock struct{ time.Time }

func (f fixedClock) Now() time.Time { return f.Time }
