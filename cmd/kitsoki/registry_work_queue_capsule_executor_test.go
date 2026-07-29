package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/webconfig"
)

func TestCapsuleQueueClientProjectsServerOwnedRetainedWIPOverStoryClaims(t *testing.T) {
	data := []byte("canonical retained WIP")
	client := capsuleQueueClient{
		binding:    webconfig.WorkQueueExecutorBinding{BundleRefOutput: "bundle_ref", BundleDigestOutput: "bundle_digest", BundleKindOutput: "bundle_kind"},
		bundleRoot: t.TempDir(),
		readWIP:    func(context.Context, ci.Config, string, string) ([]byte, error) { return data, nil },
	}
	verdict := ci.Verdict{Outcome: "passed", Outputs: map[string]string{"bundle_ref": "story-supplied", "bundle_digest": "sha256:bad", "bundle_kind": "other"}}
	if err := client.projectRetainedWIP(context.Background(), ci.Config{}, "pool", "exec-1", &verdict); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if got, want := verdict.Outputs["bundle_ref"], "capsule-ci/exec-1.bundle"; got != want {
		t.Fatalf("bundle ref = %q, want %q", got, want)
	}
	if got, want := verdict.Outputs["bundle_digest"], "sha256:"+hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("bundle digest = %q, want %q", got, want)
	}
	if verdict.Outputs["bundle_kind"] != "git-bundle" {
		t.Fatalf("bundle kind = %q", verdict.Outputs["bundle_kind"])
	}
}

func TestCapsuleQueueClientRetainedWIPFailsClosed(t *testing.T) {
	client := capsuleQueueClient{
		binding:    webconfig.WorkQueueExecutorBinding{BundleRefOutput: "bundle_ref", BundleDigestOutput: "bundle_digest"},
		bundleRoot: t.TempDir(),
		readWIP:    func(context.Context, ci.Config, string, string) ([]byte, error) { return nil, context.DeadlineExceeded },
	}
	verdict := ci.Verdict{Outcome: "passed"}
	if err := client.projectRetainedWIP(context.Background(), ci.Config{}, "pool", "exec-1", &verdict); err == nil {
		t.Fatal("missing retained WIP unexpectedly projected")
	}
	client.readWIP = func(context.Context, ci.Config, string, string) ([]byte, error) { return []byte("bytes"), nil }
	if err := client.projectRetainedWIP(context.Background(), ci.Config{}, "pool", "../unsafe", &verdict); err == nil {
		t.Fatal("unsafe execution id unexpectedly projected")
	}
}
