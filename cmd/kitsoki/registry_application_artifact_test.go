package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/applicationartifact"
	"kitsoki/internal/applicationbuild"
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/storydemo"
	"kitsoki/internal/webconfig"
)

func TestRegistryApplicationArtifactDurableReplayAndVerifiedBundle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storiesDir, appPath := writeStory(t, "producer", []byte(artifactProducerStory))
	writeArtifactProducerFiles(t, filepath.Dir(appPath))
	bundleRoot := t.TempDir()
	manifest := publishRegistryApplicationBundle(t, bundleRoot, "artifact-producer")
	dbPath := filepath.Join(t.TempDir(), "daemon.db")
	base := deterministicBase(t)
	base.DBPath = dbPath
	cfg := artifactRegistryConfig("caller")
	request := storydemo.ApplicationArtifactRequest{
		CallerApplicationID: "caller",
		Operation:           "create_mockup",
		Input: storydemo.ApplicationArtifactInput{
			Schema:      "kitsoki/story-application-mockup-input/v1",
			ScenarioRef: "story-demo:scenario", ScenarioDigest: "sha256:scenario",
			ActionIDs: []string{"open"},
		},
	}

	firstRegistry := NewRegistry(cfg, []string{storiesDir}, base)
	if err := firstRegistry.EnableDaemon(dbPath); err != nil {
		t.Fatal(err)
	}
	firstRegistry.applicationBundleRoot = bundleRoot
	if _, err := firstRegistry.Rescan(); err != nil {
		t.Fatal(err)
	}
	first, err := firstRegistry.ExecuteApplicationArtifact(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Primary != "demo-artifact:mockup" ||
		first.Bundle != "application-bundle:"+strings.TrimPrefix(manifest.Digest, "sha256:") ||
		len(first.ReceiptIDs) != 1 ||
		first.JobID == "" ||
		first.SessionID == "" {
		t.Fatalf("first result = %#v", first)
	}
	materialized, err := firstRegistry.ExecuteApplicationArtifact(
		context.Background(),
		storydemo.ApplicationArtifactRequest{
			CallerApplicationID: "caller",
			Operation:           "materialize.subject",
			Input: storydemo.ApplicationArtifactInput{
				Schema:     "kitsoki/story-application-materialize-input/v1",
				CatalogRef: "product", NodeID: "node-one", ContextDigest: "sha256:context",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Primary != "demo-artifact:materialized" ||
		materialized.Bundle != "" ||
		materialized.JobID == first.JobID ||
		len(materialized.ReceiptIDs) != 1 {
		t.Fatalf("materialized result = %#v", materialized)
	}
	job, err := firstRegistry.daemonJobs.Get(context.Background(), artifactjob.JobID(first.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != artifactjob.StatusDone ||
		job.SessionID == "" ||
		job.Origin.Kind != "application-artifact" ||
		job.TerminalArtifactHandle != first.Bundle {
		t.Fatalf("first job = %#v", job)
	}
	running := artifactjob.StatusRunning
	if _, err := firstRegistry.daemonJobs.Update(
		context.Background(),
		job.ID,
		artifactjob.Update{Status: &running},
	); err != nil {
		t.Fatal(err)
	}
	firstRegistry.Close()

	secondRegistry := NewRegistry(cfg, []string{storiesDir}, base)
	if err := secondRegistry.EnableDaemon(dbPath); err != nil {
		t.Fatal(err)
	}
	defer secondRegistry.Close()
	secondRegistry.applicationBundleRoot = bundleRoot
	if _, err := secondRegistry.Rescan(); err != nil {
		t.Fatal(err)
	}
	restored, err := secondRegistry.RestoreDaemonJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if restored != 1 {
		t.Fatalf("restored = %d, want 1", restored)
	}
	replayed, err := secondRegistry.ExecuteApplicationArtifact(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.JobID != first.JobID ||
		replayed.SessionID != first.SessionID ||
		replayed.Primary != first.Primary ||
		replayed.Bundle != first.Bundle ||
		replayed.ReceiptIDs[0] == first.ReceiptIDs[0] {
		t.Fatalf("first = %#v replayed = %#v", first, replayed)
	}
	job, err = secondRegistry.daemonJobs.Get(context.Background(), artifactjob.JobID(first.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != artifactjob.StatusDone || job.InterruptedReason != "" {
		t.Fatalf("replayed job = %#v", job)
	}
	summaries, err := secondRegistry.ListArtifactJobs(context.Background())
	if err != nil || len(summaries) != 2 {
		t.Fatalf("artifact job summaries = %#v, %v", summaries, err)
	}
	summaryRaw, _ := json.Marshal(summaries)
	for _, summary := range summaries {
		if summary.Story != "application:artifact-producer" ||
			summary.SessionID != "" ||
			summary.RunURL != "" ||
			summary.OpenURL != "" {
			t.Fatalf("public artifact job summary leaks route authority: %#v", summary)
		}
	}
	if strings.Contains(string(summaryRaw), appPath) ||
		strings.Contains(string(summaryRaw), bundleRoot) {
		t.Fatalf("public artifact job summary leaks authority: %s", summaryRaw)
	}
	journals, err := filepath.Glob(filepath.Join(filepath.Dir(job.TracePath), "*.application.jsonl"))
	if err != nil || len(journals) < 2 {
		t.Fatalf("application journals = %#v, %v", journals, err)
	}
	replayTruth := false
	for _, journal := range journals {
		raw, readErr := os.ReadFile(journal)
		if readErr != nil {
			t.Fatal(readErr)
		}
		replayTruth = replayTruth || strings.Contains(string(raw), `"replayed":true`)
	}
	if !replayTruth {
		t.Fatalf("application journals do not contain replay truth: %#v", journals)
	}
	for _, forbidden := range []string{appPath, bundleRoot, "index.html", "/application/"} {
		encoded, _ := json.Marshal(replayed)
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("public result leaks %q: %s", forbidden, encoded)
		}
	}
}

func TestRegistryApplicationArtifactMissingConfigAndCallerScope(t *testing.T) {
	registry := NewRegistry(webconfig.WebConfig{}, nil, deterministicBase(t))
	if err := registry.EnableDaemon(filepath.Join(t.TempDir(), "daemon.db")); err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	_, err := registry.ExecuteApplicationArtifact(context.Background(), storydemo.ApplicationArtifactRequest{
		CallerApplicationID: "missing", Operation: "create_mockup",
		Input: storydemo.ApplicationArtifactInput{Schema: "v1"},
	})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing config error = %v", err)
	}

	left := artifactRegistryConfig("left").StoryApplicationArtifacts["left"].CreateMockup
	right := artifactRegistryConfig("right").StoryApplicationArtifacts["right"].CreateMockup
	input := []byte(`{"schema":"v1"}`)
	leftID := artifactApplicationJobID(
		storydemo.ApplicationArtifactRequest{CallerApplicationID: "left", Operation: "create_mockup"},
		*left,
		input,
	)
	rightID := artifactApplicationJobID(
		storydemo.ApplicationArtifactRequest{CallerApplicationID: "right", Operation: "create_mockup"},
		*right,
		input,
	)
	if leftID == rightID {
		t.Fatalf("caller scopes share job identity %q", leftID)
	}
}

func TestRegistryApplicationArtifactRejectsAmbiguousProducer(t *testing.T) {
	leftDir, leftPath := writeStory(t, "left", []byte(artifactProducerStory))
	writeArtifactProducerFiles(t, filepath.Dir(leftPath))
	rightDir, rightPath := writeStory(t, "right", []byte(artifactProducerStory))
	writeArtifactProducerFiles(t, filepath.Dir(rightPath))
	registry := NewRegistry(
		artifactRegistryConfig("caller"),
		[]string{leftDir, rightDir},
		deterministicBase(t),
	)
	if err := registry.EnableDaemon(filepath.Join(t.TempDir(), "daemon.db")); err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, err := registry.Rescan(); err != nil {
		t.Fatal(err)
	}
	_, err := registry.ExecuteApplicationArtifact(context.Background(), storydemo.ApplicationArtifactRequest{
		CallerApplicationID: "caller", Operation: "create_mockup",
		Input: storydemo.ApplicationArtifactInput{
			Schema: "kitsoki/story-application-mockup-input/v1",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "ambiguous (2 exact matches)") {
		t.Fatalf("ambiguous producer error = %v", err)
	}
}

func artifactRegistryConfig(caller string) webconfig.WebConfig {
	create := applicationartifact.Binding{
		ApplicationID: "artifact-producer",
		Phases: []applicationartifact.Phase{{
			ID: "produce", Handler: "artifact-producer.produce",
			ArtifactOutputs: []string{"mockup_ref"},
		}},
		PrimaryOutput: "mockup_ref",
		Bundle:        true,
	}
	materialize := applicationartifact.Binding{
		ApplicationID: "artifact-producer",
		Phases: []applicationartifact.Phase{{
			ID: "materialize", Action: "artifact-producer.materialize",
			ArtifactOutputs: []string{"artifact_ref"},
		}},
		PrimaryOutput: "artifact_ref",
	}
	return webconfig.WebConfig{StoryApplicationArtifacts: map[string]webconfig.StoryApplicationArtifactConfig{
		caller: {
			Catalog: "catalog.yaml", CatalogRef: "product",
			CreateMockup: &create,
			Materialize: map[string]applicationartifact.Binding{
				"dependencies": materialize, "subject": materialize, "verify": materialize,
			},
		},
	}}
}

func writeArtifactProducerFiles(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{"scripts", "schemas"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "produce.star"), []byte(`
def main(ctx):
    return {
        "outcome": "ok",
        "mockup_ref": "demo-artifact:mockup",
        "artifact_ref": "demo-artifact:materialized",
    }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "produce.star.yaml"), []byte(`
inputs:
  schema: {type: string, required: true}
  catalog_ref: {type: string}
  node_id: {type: string}
  context_digest: {type: string}
  scenario_ref: {type: string}
  scenario_digest: {type: string}
  action_ids: {type: list}
outputs:
  outcome: {type: string}
  mockup_ref: {type: string}
  artifact_ref: {type: string}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	inputSchema := `{
  "type":"object",
  "required":["schema"],
  "properties":{
    "schema":{"type":"string"},
    "catalog_ref":{"type":"string"},
    "node_id":{"type":"string"},
    "context_digest":{"type":"string"},
    "scenario_ref":{"type":"string"},
    "scenario_digest":{"type":"string"},
    "action_ids":{"type":"array","items":{"type":"string"}}
  },
  "additionalProperties":false
}`
	outputSchema := `{
  "type":"object",
  "required":["outcome","mockup_ref","artifact_ref"],
  "properties":{
    "outcome":{"const":"ok"},
    "mockup_ref":{"type":"string"},
    "artifact_ref":{"type":"string"}
  },
  "additionalProperties":false
}`
	if err := os.WriteFile(filepath.Join(root, "schemas", "input.json"), []byte(inputSchema), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "schemas", "output.json"), []byte(outputSchema), 0o600); err != nil {
		t.Fatal(err)
	}
}

func publishRegistryApplicationBundle(
	t *testing.T,
	root string,
	applicationID string,
) applicationbuild.Manifest {
	t.Helper()
	appRoot := filepath.Join(root, applicationID)
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	stage, err := os.MkdirTemp(appRoot, ".fixture-")
	if err != nil {
		t.Fatal(err)
	}
	assets := map[string]string{
		"assets/app.js": "export const finite = true",
		"index.html":    "<main data-application=\"artifact-producer\"></main>",
	}
	files := make([]string, 0, len(assets))
	for name, contents := range assets {
		files = append(files, name)
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(files)
	outputHash := sha256.New()
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(stage, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(outputHash, name)
		_, _ = outputHash.Write([]byte{0})
		_, _ = outputHash.Write(raw)
		_, _ = outputHash.Write([]byte{0})
	}
	outputDigest := "sha256:" + hex.EncodeToString(outputHash.Sum(nil))
	manifest := applicationbuild.Manifest{
		Schema: applicationbuild.ManifestSchema, ApplicationID: applicationID,
		CreatedAt: time.Unix(1, 0).UTC(), Entry: "index.html", Files: files,
		Components: []applicationbuild.ComponentModule{{
			ID: "artifact.summary", Module: "locked-package", Export: "ArtifactSummary",
		}},
	}
	identity, err := json.Marshal(struct {
		Output        string
		Entry         string
		Components    []applicationbuild.ComponentModule
		Theme         map[string]string
		Native        map[string]applicationbuild.NativeSurface
		Compatibility applicationbuild.Compatibility
	}{
		Output: outputDigest, Entry: manifest.Entry, Components: manifest.Components,
		Theme: manifest.Theme, Native: manifest.Native, Compatibility: manifest.Compatibility,
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(identity)
	manifest.Digest = "sha256:" + hex.EncodeToString(sum[:])
	dir := filepath.Join(appRoot, strings.TrimPrefix(manifest.Digest, "sha256:"))
	if err := os.Rename(stage, dir); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "application-manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return manifest
}

const artifactProducerStory = `
app:
  id: artifact-producer
  version: 0.1.0
  title: Artifact producer
world: {}
root: ready
states:
  ready:
    view: Ready
application:
  schema: application/v1
  name: Artifact producer
  description: Produce opaque application artifacts.
  semantic_ref: artifact-producer.application
  shell: {entry: home}
  pages:
    home:
      name: Artifacts
      description: Present the produced artifact.
      semantic_ref: artifact-producer.page.home
      regions:
        main:
          name: Main
          description: Present the finite artifact component.
          semantic_ref: artifact-producer.region.main
          items:
            - card:
                id: artifact
                name: Artifact
                description: Present one opaque artifact.
                semantic_ref: artifact-producer.card.artifact
                actions: [artifact-producer.materialize]
  actions:
    artifact-producer.materialize:
      name: Materialize
      description: Materialize an opaque artifact.
      semantic_ref: artifact-producer.action.materialize
      handler: artifact-producer.produce
exports:
  application:
    pages: [home]
    actions: [artifact-producer.materialize]
  handlers:
    artifact-producer.produce:
      name: Produce
      description: Produce an opaque artifact.
      semantic_ref: artifact-producer.handler.produce
      input_schema: schemas/input.json
      output_schema: schemas/output.json
      session: none
      effect: write
      routing_mode: exact
      outcomes: [ok]
      starlark: {script: scripts/produce.star}
      expose: [jsonrpc]
      idempotency: {key: input.scenario_digest, scope: application}
`
