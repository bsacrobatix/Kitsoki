package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/storydigest"
	"kitsoki/internal/capsule/vmpool"
	"kitsoki/internal/objectstore"
)

// vmpoolRoundtripCmd proves the full bucket-mediated worker mechanism live:
// a throwaway sealed repo's source is published to the object store, an
// ephemeral worker fetches it from there (never from the controller), runs a
// trivial deterministic story, mirrors its run record and trace back to the
// bucket, and is destroyed. The orchestrator's own disk stays untouched —
// which is the point: prove S3 carries both directions before real work
// moves onto the pool.
func vmpoolRoundtripCmd() *cobra.Command {
	var common vmpoolCommonFlags
	var cfgFlags vmpoolConfigFlags
	var bucketURL, keyEnv, secretEnv string
	var rounds int
	cmd := &cobra.Command{
		Use:          "roundtrip",
		Short:        "Live proof of the bucket transport: source up, worker run, outputs back, per round",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := vmpoolRoundtripStore(bucketURL, keyEnv, secretEnv)
			if err != nil {
				return err
			}
			pool, err := vmpoolBuildPool(common, cfgFlags)
			if err != nil {
				return err
			}
			pool.PreserveFailed = true
			out := json.NewEncoder(cmd.OutOrStdout())
			for round := 1; round <= rounds; round++ {
				result, err := vmpoolRunRoundtrip(cmd.Context(), pool, store, bucketURL, keyEnv, secretEnv, round)
				if err != nil {
					return fmt.Errorf("vmpool roundtrip %d/%d: %w", round, rounds, err)
				}
				if err := out.Encode(result); err != nil {
					return err
				}
			}
			return nil
		},
	}
	addVMPoolCommonFlags(cmd, &common)
	addVMPoolConfigFlags(cmd, &cfgFlags)
	cmd.Flags().StringVar(&bucketURL, "bucket-url", "", "virtual-hosted bucket URL (required)")
	cmd.Flags().StringVar(&keyEnv, "bucket-key-env", "DO_SPACES_KEY_ID", "env var holding the bucket access key id")
	cmd.Flags().StringVar(&secretEnv, "bucket-secret-env", "DO_KITSOKI_TEST_API_KEY", "env var holding the bucket secret")
	cmd.Flags().IntVar(&rounds, "rounds", 1, "number of consecutive rounds")
	_ = cmd.MarkFlagRequired("bucket-url")
	return cmd
}

func vmpoolRoundtripStore(bucketURL, keyEnv, secretEnv string) (objectstore.Store, error) {
	cfg, err := objectstore.ParseBucketURL(bucketURL)
	if err != nil {
		return nil, err
	}
	cfg.KeyEnv, cfg.SecretEnv = keyEnv, secretEnv
	return objectstore.NewSpaces(cfg, nil)
}

func vmpoolRunRoundtrip(ctx context.Context, pool *vmpool.Pool, store objectstore.Store, bucketURL, keyEnv, secretEnv string, round int) (map[string]any, error) {
	repo, head, storyRel, envLock, err := vmpoolRoundtripRepo()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(repo)
	closure, err := storydigest.Compute(repo, storyRel)
	if err != nil {
		return nil, err
	}
	envelope, err := executor.Seal(executor.Envelope{
		JobID:            fmt.Sprintf("roundtrip-%d-%s", round, head[:8]),
		ProjectID:        "vmpool-roundtrip",
		DefinitionDigest: "sha256:roundtrip",
		Instance:         control.Handle{ID: "roundtrip", Generation: 1},
		SourceDigest:     head,
		StoryPath:        storyRel,
		StoryDigest:      closure.Digest,
		Environment:      envLock,
		Trigger:          map[string]any{"kind": "local", "requested_pipeline": "change"},
		Policy:           executor.Policy{Network: "live", ExternalWrite: "deny"},
	})
	if err != nil {
		return nil, err
	}
	executionID := fmt.Sprintf("rt-%d-%d", round, time.Now().Unix())

	dispatcher := &vmpool.Dispatcher{Pool: pool}
	started := time.Now()
	lease, err := dispatcher.Lease(ctx, vmpool.LeaseSpec{
		JobID:            envelope.JobID,
		Objects:          store,
		BucketURL:        bucketURL,
		OutputsKeyEnv:    keyEnv,
		OutputsSecretEnv: secretEnv,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = lease.Release(context.WithoutCancel(ctx)) }()

	remote := lease.Remote
	remote.Source = executor.SourceBundlerFunc(func(ctx context.Context, _ executor.Envelope) (executor.SourceBundle, error) {
		return executor.GitBundle(ctx, repo, head, 0)
	})
	prepared := executor.Prepared{ID: executionID, Envelope: envelope, Placement: "remote", Applied: envelope.Policy}
	runResult, runErr := remote.Run(ctx, prepared, nil, nil)

	result := map[string]any{
		"round":       round,
		"worker":      lease.Worker.ID,
		"instance":    lease.Worker.InstanceID,
		"endpoint":    lease.Endpoint,
		"execution":   executionID,
		"source_head": head,
		"elapsed":     time.Since(started).Round(time.Second).String(),
	}
	if runErr != nil {
		result["run_error"] = runErr.Error()
	} else {
		result["exit_code"] = runResult.ExitCode
	}
	// The verdict on the transport itself: the frozen source must exist in
	// the bucket (the worker had no other way to receive it), and the
	// worker's mirrored outputs must have arrived.
	checks := map[string]string{
		"source_bundle": "sources/" + head + "/bundle.git",
		"source_meta":   "sources/" + head + "/meta.json",
		"run_record":    "runs/" + executionID + "/run.json",
		"story_trace":   "runs/" + executionID + "/story-trace.jsonl",
	}
	bucket := map[string]any{}
	allOK := runErr == nil
	for name, key := range checks {
		meta, headErr := store.Head(ctx, key)
		if headErr != nil {
			bucket[name] = "MISSING: " + headErr.Error()
			allOK = false
		} else {
			bucket[name] = fmt.Sprintf("ok (%d bytes)", meta.Size)
		}
	}
	result["bucket"] = bucket
	result["transport_ok"] = allOK
	if !allOK {
		return result, fmt.Errorf("bucket transport incomplete: %v", bucket)
	}
	return result, nil
}

// vmpoolRoundtripRepo builds a single-commit throwaway repo with a trivial
// deterministic CI story and environment lock.
func vmpoolRoundtripRepo() (repo, head, storyRel string, lock environment.Lock, err error) {
	repo, err = os.MkdirTemp("", "kitsoki-roundtrip-*")
	if err != nil {
		return "", "", "", environment.Lock{}, err
	}
	fail := func(e error) (string, string, string, environment.Lock, error) {
		os.RemoveAll(repo)
		return "", "", "", environment.Lock{}, e
	}
	storyRel = filepath.ToSlash(filepath.Join(".kitsoki", "stories", "ci", "app.yaml"))
	envRel := filepath.Join(".kitsoki", "environments", "ci.yaml")
	files := map[string]string{
		storyRel: vmpoolRoundtripStory,
		envRel:   "schema: capsule-environment/v1\nid: ci\nnetwork: live\nsandbox: supervised\n",
	}
	for rel, body := range files {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return fail(err)
		}
	}
	run := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %w: %s", args, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.name=roundtrip", "-c", "user.email=roundtrip@kitsoki.invalid", "commit", "-q", "-m", "roundtrip fixture"},
	} {
		if err := run(args...); err != nil {
			return fail(err)
		}
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		return fail(err)
	}
	head = strings.TrimSpace(string(out))
	lock, err = (environment.Resolver{ProjectRoot: repo}).Resolve(context.Background(), "ci")
	if err != nil {
		return fail(err)
	}
	return repo, head, storyRel, lock, nil
}

const vmpoolRoundtripStory = `app:
  id: vmpool-roundtrip
  version: 0.1.0
  title: Bucket transport roundtrip
  author: kitsoki
  license: CC0
world:
  ci_job_id: { type: string, default: "" }
  ci_pipeline: { type: string, default: "" }
  ci_trigger: { type: object, default: {} }
  ci_source: { type: object, default: {} }
  ci_workspace: { type: object, default: {} }
  ci_environment: { type: object, default: {} }
  ci_policy: { type: object, default: {} }
  ci_verdict: { type: object, default: {} }
intents:
  run: { description: run, examples: [run], priority: 1 }
root: idle
states:
  idle:
    view: [{ prose: ready }]
    on:
      run:
        - target: done
          effects:
            - set:
                ci_verdict:
                  schema: capsule-ci-verdict/v1
                  pipeline: "{{ world.ci_pipeline }}"
                  outcome: passed
                  summary: bucket transport roundtrip
                  checks:
                    - id: deterministic
                      kind: deterministic
                      outcome: passed
                      evidence: [roundtrip:live]
                  promotion_eligible: true
                  source_digest: "{{ world.ci_source.digest }}"
                  story_digest: "{{ world.ci_trigger.story_digest }}"
                  environment_digest: "{{ world.ci_environment.digest }}"
                  envelope_digest: "{{ world.ci_trigger.envelope_digest }}"
  done:
    terminal: true
    view: [{ prose: passed }]
`
