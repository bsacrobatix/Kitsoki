package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/queue"
)

func TestCapsulePromoteExistingExposesNoWaiverOrDirectFinalizationFlags(t *testing.T) {
	command := capsulePromoteExistingCmd()
	for _, forbidden := range []string{"skip-tests", "wait", "override"} {
		if command.Flags().Lookup(forbidden) != nil {
			t.Fatalf("promote-existing unexpectedly exposes --%s", forbidden)
		}
	}
	for _, required := range []string{"project", "source-target", "sha", "target", "pipeline", "gate", "definition"} {
		if command.Flags().Lookup(required) == nil {
			t.Fatalf("promote-existing is missing --%s", required)
		}
	}
}

func TestKitsokiExactSourceCertificationUsesPreparedBoundedGate(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".kitsoki", "project-profile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		Commands struct {
			Test    string `yaml:"test"`
			Full    string `yaml:"full"`
			Release string `yaml:"release"`
		} `yaml:"commands"`
		DevStoryProfile struct {
			Bugfix struct {
				Test string `yaml:"test_cmd"`
			} `yaml:"bugfix"`
		} `yaml:"dev_story_profile"`
	}
	if err := yaml.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Commands.Test != "make test" || profile.DevStoryProfile.Bugfix.Test != "make test" {
		t.Fatalf("exact-source test contracts = commands:%q bugfix:%q, want make test", profile.Commands.Test, profile.DevStoryProfile.Bugfix.Test)
	}
	if profile.Commands.Full != "make test-full" {
		t.Fatalf("full Capsule-CI contract=%q, want make test-full", profile.Commands.Full)
	}
	if profile.Commands.Release != "make test-full && go build ./..." {
		t.Fatalf("release Capsule-CI contract=%q, want exhaustive tests plus repository build", profile.Commands.Release)
	}
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(makefile), "test: embed-stories embed-skills") {
		t.Fatal("make test no longer prepares the embedded story and skill libraries")
	}
	if !strings.Contains(string(makefile), `KITSOKI_GO_TEST_FLAGS="$${KITSOKI_GO_TEST_FLAGS:--short -p 4}"`) {
		t.Fatal("make test no longer declares the bounded local Go-test lane")
	}
}

func TestRunPromoteExistingCIPersistsFailedCheckEvidenceAfterSourceTeardown(t *testing.T) {
	project := t.TempDir()
	writePromoteExistingCIProject(t, project)
	sha := promoteExistingGit(t, project, "rev-parse", "HEAD")
	in := queue.ExistingSHACertification{
		Key:   "sha256:test-promote-existing-evidence",
		JobID: "promote-existing-test-evidence",
		Request: queue.PromoteExistingRequest{
			SourceTarget: "staging/local", LandedSHA: sha, DestinationTarget: "main", Pipeline: "change", GateCommand: "make test",
		},
		SourceCandidate:        queue.Candidate{ID: "queue-source"},
		SourceLandingReceiptID: "sha256:source-receipt",
	}
	stored, err := runPromoteExistingCI(context.Background(), project, "development", in)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Receipt.Verdict.PromotionEligible || stored.Receipt.Verdict.Outcome != "failed" {
		t.Fatalf("receipt verdict = %#v", stored.Receipt.Verdict)
	}

	path := filepath.Join(project, ".capsules", "ci", "evidence", in.JobID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("retained evidence missing after exact source teardown: %v", err)
	}
	var artifact struct {
		Checks []struct {
			ID  string `json:"id"`
			Log string `json:"log"`
		}
	}
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	if len(artifact.Checks) != 2 || artifact.Checks[0].ID != "tests" || !strings.Contains(artifact.Checks[0].Log, "intentional-test-failure") {
		t.Fatalf("retained check artifact = %#v", artifact)
	}
	if !strings.Contains(string(raw), `"command": "make test"`) {
		t.Fatalf("exact-source evidence did not retain the project-declared prepared gate: %s", raw)
	}

	run, err := (ci.FileRunStore{ProjectRoot: project}).Get(in.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got := run.Result.Verdict.Checks[0].Evidence; len(got) != 1 || got[0] != "file:.capsules/ci/evidence/"+in.JobID+".json#tests" {
		t.Fatalf("test evidence reference = %#v", got)
	}
	diagnosis, err := (ci.FileRunStore{ProjectRoot: project}).Diagnose(in.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsDiagnosticArtifact(diagnosis.Artifacts, "check_evidence", ".capsules/ci/evidence/"+in.JobID+".json") {
		t.Fatalf("diagnostic artifacts = %#v", diagnosis.Artifacts)
	}
}

func writePromoteExistingCIProject(t *testing.T, root string) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	story, err := os.ReadFile(filepath.Join(repoRoot, "stories", "capsule-ci", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".kitsoki/capsules/development.yaml": `schema: capsule-definition/v1
id: development
source:
  kind: dev-workspace-script
  development:
    base: staging/local
    target: staging/local
    branch_prefix: agent/
policy:
  network: none
`,
		".kitsoki/ci.yaml": `schema: capsule-ci/v1
default_environment: ci
pipelines:
  change:
    story: stories/capsule-ci/app.yaml
    triggers: [local]
    environment: ci
    executor: host
    mode: one-shot
    required: true
    permissions:
      network: live
      external_write: allow
    agents:
      policy: deny
    result:
      schema: capsule-ci-verdict/v1
      pass_exits: [passed]
      fail_exits: [failed]
      park_exits: [needs_input]
`,
		".kitsoki/environments/ci.yaml": "schema: capsule-environment/v1\nid: ci\nnetwork: live\nsandbox: supervised\n",
		".kitsoki/project-profile.yaml": "schema: project-profile/v1\nid: fixture\ncommands:\n  test: \"make test\"\n  build: \"true\"\n",
		"Makefile":                      "prepare:\n\t@printf prepared > .prepared\n\ntest: prepare\n\t@test -f .prepared\n\t@echo intentional-test-failure\n\t@exit 17\n",
		"stories/capsule-ci/app.yaml":   string(story),
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "test"}, {"config", "user.email", "test@example.invalid"}, {"add", "."}, {"commit", "-qm", "fixture"}} {
		promoteExistingGit(t, root, args...)
	}
}

func promoteExistingGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func containsDiagnosticArtifact(artifacts []ci.DiagnosticArtifact, kind, path string) bool {
	for _, artifact := range artifacts {
		if artifact.Kind == kind && artifact.Path == path {
			return true
		}
	}
	return false
}
