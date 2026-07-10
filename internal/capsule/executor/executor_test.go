package executor

import (
	"context"
	"testing"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
)

func TestFakeProviderSealsEnvelopeAndRejectsUnsupportedNetwork(t *testing.T) {
	env := environment.Lock{Schema: environment.LockSchema, ID: "ci", Digest: "sha256:env"}
	base := Envelope{JobID: "job", ProjectID: "project", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, StoryPath: "story", StoryDigest: "sha256:story", Environment: env, Policy: Policy{Network: "none", MinimumSandbox: "supervised", ExternalWrite: "deny"}}
	provider := NewFakeProvider("remote")
	prepared, err := provider.Prepare(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	again, err := provider.Prepare(context.Background(), base)
	if err != nil || again.ID != prepared.ID {
		t.Fatalf("prepare not idempotent %#v %v", again, err)
	}
	got, err := provider.Run(context.Background(), prepared, func(context.Context, Prepared) (Result, error) { return Result{Artifacts: []string{"b", "a"}}, nil }, nil)
	if err != nil || got.ExecutionID != prepared.ID || got.Artifacts[0] != "a" {
		t.Fatalf("run %#v %v", got, err)
	}
	base.Policy.Network = "live"
	if _, err := provider.Prepare(context.Background(), base); err == nil {
		t.Fatal("unsupported network accepted")
	}
}
