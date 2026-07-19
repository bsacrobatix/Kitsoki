package ci

import (
	"context"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/capsule/bucketsource"
	"kitsoki/internal/capsule/executor"
)

const testBucketURL = "https://kitsoki-test.sgp1.digitaloceanspaces.com"

// TestSourceBucketConfigRoundTripsWithDefaults documents the block's parse
// shape: prefix/presign_ttl are optional in YAML, and the zero-value Go
// fields are exactly what newSourceObjects later treats as "use the
// bucketsource default" (see TestConfiguredExecutorsAppliesSourceBucketDefaults).
func TestSourceBucketConfigRoundTripsWithDefaults(t *testing.T) {
	raw := []byte(`
endpoint: https://worker.invalid
credential_env: KITSOKI_TEST_REMOTE_TOKEN
source_bucket:
  url: https://kitsoki-test.sgp1.digitaloceanspaces.com
  key_env: DO_SPACES_KEY_ID
  secret_env: DO_KITSOKI_TEST_API_KEY
`)
	var remote Remote
	if err := yaml.Unmarshal(raw, &remote); err != nil {
		t.Fatal(err)
	}
	if remote.SourceBucket == nil {
		t.Fatal("expected source_bucket to parse into a non-nil block")
	}
	if remote.SourceBucket.URL != testBucketURL || remote.SourceBucket.KeyEnv != "DO_SPACES_KEY_ID" || remote.SourceBucket.SecretEnv != "DO_KITSOKI_TEST_API_KEY" {
		t.Fatalf("source_bucket %#v", remote.SourceBucket)
	}
	if remote.SourceBucket.Prefix != "" || remote.SourceBucket.PresignTTL != "" {
		t.Fatalf("expected omitted prefix/presign_ttl to decode as empty (default), got %#v", remote.SourceBucket)
	}

	// Round-trip through Marshal/Unmarshal to confirm the yaml tags are
	// symmetric and the block survives a checked-in-file rewrite.
	out, err := yaml.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	var reparsed Remote
	if err := yaml.Unmarshal(out, &reparsed); err != nil {
		t.Fatal(err)
	}
	if reparsed.SourceBucket == nil || *reparsed.SourceBucket != *remote.SourceBucket {
		t.Fatalf("round-trip mismatch: got %#v, want %#v", reparsed.SourceBucket, remote.SourceBucket)
	}

	if err := validateSourceBucket("remote", *remote.SourceBucket); err != nil {
		t.Fatalf("valid source_bucket rejected: %v", err)
	}
}

func TestSourceBucketConfigRoundTripsWithExplicitOverrides(t *testing.T) {
	remote := Remote{
		Endpoint: "https://worker.invalid",
		SourceBucket: &SourceBucket{
			URL:        testBucketURL,
			KeyEnv:     "DO_SPACES_KEY_ID",
			SecretEnv:  "DO_KITSOKI_TEST_API_KEY",
			Prefix:     "custom-sources",
			PresignTTL: "30m",
		},
	}
	if err := validateSourceBucket("remote", *remote.SourceBucket); err != nil {
		t.Fatalf("valid source_bucket rejected: %v", err)
	}
	out, err := yaml.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	var reparsed Remote
	if err := yaml.Unmarshal(out, &reparsed); err != nil {
		t.Fatal(err)
	}
	if reparsed.SourceBucket == nil || *reparsed.SourceBucket != *remote.SourceBucket {
		t.Fatalf("round-trip mismatch: got %#v, want %#v", reparsed.SourceBucket, remote.SourceBucket)
	}
}

// TestValidateSourceBucketNeverReadsCredentialsFromEnvironment is the
// no-spend contract: doctor and other config-only preflights call
// Validate/Load without the bucket's key_env/secret_env ever being set in the
// process environment. Deliberately not calling t.Setenv for either name here
// and still expecting success is the point of the test.
func TestValidateSourceBucketNeverReadsCredentialsFromEnvironment(t *testing.T) {
	sb := SourceBucket{URL: testBucketURL, KeyEnv: "KITSOKI_NEVER_SET_KEY_PROBE", SecretEnv: "KITSOKI_NEVER_SET_SECRET_PROBE"}
	if err := validateSourceBucket("remote", sb); err != nil {
		t.Fatalf("source_bucket validation must not require credentials to be set: %v", err)
	}
}

func TestValidateRejectsSourceBucketBadURL(t *testing.T) {
	err := validateSourceBucket("remote", SourceBucket{URL: "not-a-bucket-url", KeyEnv: "K", SecretEnv: "S"})
	if err == nil || !strings.Contains(err.Error(), "source_bucket url") {
		t.Fatalf("expected source_bucket url rejection, got %v", err)
	}
}

func TestValidateRejectsSourceBucketBadEnvNames(t *testing.T) {
	if err := validateSourceBucket("remote", SourceBucket{URL: testBucketURL, KeyEnv: "not-an-env-name", SecretEnv: "S"}); err == nil || !strings.Contains(err.Error(), "key_env") {
		t.Fatalf("expected key_env rejection, got %v", err)
	}
	if err := validateSourceBucket("remote", SourceBucket{URL: testBucketURL, KeyEnv: "K", SecretEnv: "not-an-env-name"}); err == nil || !strings.Contains(err.Error(), "secret_env") {
		t.Fatalf("expected secret_env rejection, got %v", err)
	}
}

func TestValidateRejectsSourceBucketTraversalPrefix(t *testing.T) {
	cases := []string{"../escape", "/absolute", ".."}
	for _, prefix := range cases {
		err := validateSourceBucket("remote", SourceBucket{URL: testBucketURL, KeyEnv: "K", SecretEnv: "S", Prefix: prefix})
		if err == nil || !strings.Contains(err.Error(), "prefix") {
			t.Fatalf("prefix %q: expected traversal rejection, got %v", prefix, err)
		}
	}
}

func TestValidateRejectsSourceBucketNegativePresignTTL(t *testing.T) {
	err := validateSourceBucket("remote", SourceBucket{URL: testBucketURL, KeyEnv: "K", SecretEnv: "S", PresignTTL: "-1h"})
	if err == nil || !strings.Contains(err.Error(), "presign_ttl") {
		t.Fatalf("expected presign_ttl rejection, got %v", err)
	}
}

func TestValidateAcceptsSourceBucketZeroPresignTTL(t *testing.T) {
	if err := validateSourceBucket("remote", SourceBucket{URL: testBucketURL, KeyEnv: "K", SecretEnv: "S", PresignTTL: "0s"}); err != nil {
		t.Fatalf("zero presign_ttl should be accepted (falls back to the default at construction time): %v", err)
	}
}

// TestConfiguredExecutorsWiresSourceBucketPublisher is the construction-wiring
// test: with fake credentials present, selecting a remote whose source_bucket
// is configured must produce an HTTPRemoteWorker whose SourceObjects is a live
// bucketsource.Publisher carrying the configured prefix/TTL.
func TestConfiguredExecutorsWiresSourceBucketPublisher(t *testing.T) {
	t.Setenv("KITSOKI_TEST_BUCKET_KEY", "fake-key-id")
	t.Setenv("KITSOKI_TEST_BUCKET_SECRET", "fake-secret")
	executors := ConfiguredExecutors{
		Builtins: NewBuiltinExecutors(),
		Remotes: map[string]Remote{
			"remote": {
				Endpoint: "https://worker.invalid",
				SourceBucket: &SourceBucket{
					URL:        testBucketURL,
					KeyEnv:     "KITSOKI_TEST_BUCKET_KEY",
					SecretEnv:  "KITSOKI_TEST_BUCKET_SECRET",
					Prefix:     "custom-sources",
					PresignTTL: "30m",
				},
			},
		},
	}
	provider, err := executors.Select(context.Background(), "remote")
	if err != nil {
		t.Fatal(err)
	}
	remoteProvider, ok := provider.(*executor.RemoteProvider)
	if !ok {
		t.Fatalf("provider type %T", provider)
	}
	worker, ok := remoteProvider.Worker.(executor.HTTPRemoteWorker)
	if !ok {
		t.Fatalf("worker type %T", remoteProvider.Worker)
	}
	if worker.SourceObjects == nil {
		t.Fatal("expected SourceObjects to be wired when source_bucket is configured")
	}
	publisher, ok := worker.SourceObjects.(bucketsource.Publisher)
	if !ok {
		t.Fatalf("SourceObjects type %T", worker.SourceObjects)
	}
	if publisher.Store == nil {
		t.Fatal("expected the publisher's object store to be constructed")
	}
	if publisher.Prefix != "custom-sources" {
		t.Fatalf("prefix = %q, want %q", publisher.Prefix, "custom-sources")
	}
	if publisher.PresignTTL != 30*time.Minute {
		t.Fatalf("presign ttl = %s, want %s", publisher.PresignTTL, 30*time.Minute)
	}
}

// TestConfiguredExecutorsAppliesSourceBucketDefaults exercises the config
// defaults documented in the source_bucket YAML block: an omitted prefix
// defaults to "sources" and an omitted presign_ttl defaults to 1h.
func TestConfiguredExecutorsAppliesSourceBucketDefaults(t *testing.T) {
	t.Setenv("KITSOKI_TEST_BUCKET_KEY", "fake-key-id")
	t.Setenv("KITSOKI_TEST_BUCKET_SECRET", "fake-secret")
	executors := ConfiguredExecutors{
		Builtins: NewBuiltinExecutors(),
		Remotes: map[string]Remote{
			"remote": {
				Endpoint: "https://worker.invalid",
				SourceBucket: &SourceBucket{
					URL:       testBucketURL,
					KeyEnv:    "KITSOKI_TEST_BUCKET_KEY",
					SecretEnv: "KITSOKI_TEST_BUCKET_SECRET",
				},
			},
		},
	}
	provider, err := executors.Select(context.Background(), "remote")
	if err != nil {
		t.Fatal(err)
	}
	worker := provider.(*executor.RemoteProvider).Worker.(executor.HTTPRemoteWorker)
	publisher := worker.SourceObjects.(bucketsource.Publisher)
	if publisher.Prefix != bucketsource.DefaultSourcePrefix {
		t.Fatalf("prefix = %q, want default %q", publisher.Prefix, bucketsource.DefaultSourcePrefix)
	}
	if publisher.PresignTTL != bucketsource.DefaultPresignTTL {
		t.Fatalf("presign ttl = %s, want default %s", publisher.PresignTTL, bucketsource.DefaultPresignTTL)
	}
}

// TestConfiguredExecutorsSelectWithoutSourceBucketIsUnchanged pins existing
// behavior: a remote with no source_bucket block never touches SourceObjects,
// so plain credential_env-only remotes are unaffected by this feature.
func TestConfiguredExecutorsSelectWithoutSourceBucketIsUnchanged(t *testing.T) {
	t.Setenv("KITSOKI_TEST_REMOTE_TOKEN", "secret-token")
	executors := ConfiguredExecutors{
		Builtins: NewBuiltinExecutors(),
		Remotes: map[string]Remote{
			"remote": {Endpoint: "https://worker.invalid", CredentialEnv: "KITSOKI_TEST_REMOTE_TOKEN"},
		},
	}
	provider, err := executors.Select(context.Background(), "remote")
	if err != nil {
		t.Fatal(err)
	}
	worker := provider.(*executor.RemoteProvider).Worker.(executor.HTTPRemoteWorker)
	if worker.SourceObjects != nil {
		t.Fatalf("expected nil SourceObjects when source_bucket is absent, got %#v", worker.SourceObjects)
	}
}

// TestConfiguredExecutorsSourceBucketMissingCredentialsNamesTheEnvVars is the
// construction-time failure path: with the bucket's env vars unset, selecting
// the remote must fail with a clear error naming both configured variable
// names, so an operator can fix the deployment without reading source.
func TestConfiguredExecutorsSourceBucketMissingCredentialsNamesTheEnvVars(t *testing.T) {
	executors := ConfiguredExecutors{
		Builtins: NewBuiltinExecutors(),
		Remotes: map[string]Remote{
			"remote": {
				Endpoint: "https://worker.invalid",
				SourceBucket: &SourceBucket{
					URL:       testBucketURL,
					KeyEnv:    "KITSOKI_MISSING_BUCKET_KEY",
					SecretEnv: "KITSOKI_MISSING_BUCKET_SECRET",
				},
			},
		},
	}
	_, err := executors.Select(context.Background(), "remote")
	if err == nil {
		t.Fatal("expected an error when bucket credentials are unset")
	}
	if !strings.Contains(err.Error(), "KITSOKI_MISSING_BUCKET_KEY") || !strings.Contains(err.Error(), "KITSOKI_MISSING_BUCKET_SECRET") {
		t.Fatalf("error should name both missing env vars, got: %v", err)
	}
	if !strings.Contains(err.Error(), "remote") {
		t.Fatalf("error should name the remote, got: %v", err)
	}
}
