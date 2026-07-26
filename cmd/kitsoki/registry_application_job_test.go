package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/applicationjob"
	"kitsoki/internal/webconfig"
)

func TestRegistryApplicationJobDispatchesConfiguredBackgroundEvent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	callerRoot, _ := writeStory(t, "caller", []byte(applicationJobCallerStory))
	targetRoot, targetPath := writeStory(t, "producer", []byte(applicationJobProducerStory))
	writeApplicationJobProducerFiles(t, filepath.Dir(targetPath))

	dbPath := filepath.Join(t.TempDir(), "daemon.db")
	base := deterministicBase(t)
	base.DBPath = dbPath
	registry := NewRegistry(webconfig.WebConfig{
		StoryApplicationJobs: map[string]map[string]applicationjob.Template{
			"caller": {
				"publish": {
					ApplicationID:   "producer",
					Event:           "publish",
					ArtifactOutputs: []string{"report_ref"},
					PrimaryOutput:   "report_ref",
					Bounds: applicationjob.Bounds{
						MaxInputBytes: 4096, MaxRuntimeSeconds: 60,
					},
				},
			},
		},
	}, []string{callerRoot, targetRoot}, base)
	if err := registry.EnableDaemon(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registry.Close)
	if _, err := registry.Rescan(); err != nil {
		t.Fatal(err)
	}

	callerID, err := registry.NewRegisteredApplicationSession(context.Background(), "caller")
	if err != nil {
		t.Fatal(err)
	}
	callerHosts, ok := registry.ApplicationHostRegistry(callerID)
	if !ok || callerHosts == nil {
		t.Fatal("caller host registry is unavailable")
	}
	submitted, err := callerHosts.Invoke(
		context.Background(),
		"host.application_job.submit",
		map[string]any{
			"template": "publish",
			"input":    map[string]any{"node_id": "n1"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	jobRef, _ := submitted.Data["job_ref"].(string)
	if !strings.HasPrefix(jobRef, "aj_") || submitted.Data["status"] != "running" {
		t.Fatalf("submit result = %#v", submitted.Data)
	}

	private, err := registry.applicationJobs.Records.Get(context.Background(), jobRef)
	if err != nil {
		t.Fatal(err)
	}
	if private.TargetRouteID == "" || private.TargetSessionID == "" ||
		private.ChildJobID == "" {
		t.Fatalf("private child mapping = %#v", private)
	}
	scheduler, ok := registry.ApplicationEventScheduler(private.TargetRouteID)
	if !ok {
		t.Fatal("target scheduler is unavailable")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := scheduler.WaitIdle(waitCtx); err != nil {
		t.Fatal(err)
	}

	status, err := callerHosts.Invoke(
		context.Background(),
		"host.application_job.status",
		map[string]any{"job_ref": jobRef},
	)
	if err != nil {
		t.Fatal(err)
	}
	if status.Data["status"] != "done" || status.Data["primary"] != "report:complete" {
		t.Fatalf("status result = %#v", status.Data)
	}
	raw, err := json.Marshal(status.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		private.TargetRouteID, private.TargetSessionID, private.ChildJobID, targetPath,
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("public status leaks private authority %q: %s", forbidden, raw)
		}
	}
}

func writeApplicationJobProducerFiles(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "schemas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"schemas/input.json": `{
  "type": "object",
  "required": ["node_id"],
  "properties": {"node_id": {"type": "string"}},
  "additionalProperties": false
}`,
		"schemas/output.json": `{
  "type": "object",
  "required": ["outcome", "report_ref"],
  "properties": {
    "outcome": {"const": "ok"},
    "report_ref": {"type": "string"}
  },
  "additionalProperties": false
}`,
		"scripts/publish.star": `def main(ctx):
    return {"outcome": "ok", "report_ref": "report:complete"}
`,
		"scripts/publish.star.yaml": `inputs:
  node_id: {type: string, required: true}
outputs:
  outcome: {type: string}
  report_ref: {type: string}
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const applicationJobCallerStory = `
app:
  id: caller
  version: 0.1.0
  title: Application job caller
world: {}
root: ready
states:
  ready:
    view: Ready
`

const applicationJobProducerStory = `
app:
  id: producer
  version: 0.1.0
  title: Application job producer
world: {}
root: ready
states:
  ready:
    view: Ready
application:
  schema: application/v1
  name: Application job producer
  description: Produce one opaque report artifact.
  semantic_ref: producer.application
  shell: {entry: home}
  pages:
    home:
      name: Producer
      description: Produce report artifacts.
      semantic_ref: producer.page.home
      regions:
        main:
          name: Main
          description: Produce one report.
          semantic_ref: producer.region.main
          items:
            - card:
                id: report
                name: Report
                description: A report artifact.
                semantic_ref: producer.card.report
                actions: [producer.publish]
  actions:
    producer.publish:
      name: Publish
      description: Publish one report.
      semantic_ref: producer.action.publish
      handler: producer.publish
exports:
  application:
    pages: [home]
    actions: [producer.publish]
  handlers:
    producer.publish:
      name: Publish
      description: Publish one opaque report reference.
      semantic_ref: producer.handler.publish
      input_schema: schemas/input.json
      output_schema: schemas/output.json
      session: required
      effect: write
      routing_mode: exact
      outcomes: [ok]
      starlark: {script: scripts/publish.star}
      expose: [jsonrpc]
      idempotency: {key: input.node_id, scope: application}
events:
  publish:
    source: producer.publish.requested
    input_schema: schemas/input.json
    session: required
    mode: background
    dispatch: {handler: producer.publish}
`
