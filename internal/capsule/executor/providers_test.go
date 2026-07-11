package executor

import (
	"context"
	"reflect"
	"testing"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
)

func TestHostAndFakeRemoteProduceNormalizedParity(t *testing.T) {
	envelope, err := Seal(Envelope{JobID: "job", ProjectID: "project", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryDigest: "sha256:story", Environment: environment.Lock{Schema: environment.LockSchema, ID: "ci", Digest: "sha256:env"}, Policy: Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	task := func(context.Context, Prepared) (Result, error) {
		return Result{ExitCode: 0, VerdictArtifact: "artifact:verdict", Artifacts: []string{"artifact:b", "artifact:a"}}, nil
	}
	host := NewHostProvider()
	remote := NewRemoteProvider(NewFakeRemoteWorker())
	hp, err := host.Prepare(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	rp, err := remote.Prepare(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	hr, err := host.Run(context.Background(), hp, task, nil)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := remote.Run(context.Background(), rp, task, nil)
	if err != nil {
		t.Fatal(err)
	}
	hr.ExecutionID = ""
	rr.ExecutionID = ""
	if !reflect.DeepEqual(hr, rr) {
		t.Fatalf("host=%#v remote=%#v", hr, rr)
	}
	if err := remote.Cancel(context.Background(), rp.ID); err != nil {
		t.Fatal(err)
	}
}

func TestContainerProviderAdaptsCompletionState(t *testing.T) {
	envelope, err := Seal(Envelope{JobID: "job", ProjectID: "project", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryDigest: "sha256:story", Environment: environment.Lock{Schema: environment.LockSchema, ID: "ci", Digest: "sha256:env"}, Policy: Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	provider := NewContainerProvider(NewFakeContainerBackend())
	prepared, err := provider.Prepare(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Run(context.Background(), prepared, func(context.Context, Prepared) (Result, error) {
		return Result{ExitCode: 0, VerdictArtifact: "artifact:verdict"}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider["completion_state_schema"] != CompletionStateSchema || result.Provider["completion_state_outcome"] != "passed" {
		t.Fatalf("missing completion-state provider facts: %#v", result)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0] != "completion-state:"+prepared.ID {
		t.Fatalf("missing completion-state artifact: %#v", result.Artifacts)
	}
	if err := provider.Cancel(context.Background(), prepared.ID); err != nil {
		t.Fatal(err)
	}
}
