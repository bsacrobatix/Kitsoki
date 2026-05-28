package host_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/host"
)

func TestCypilotArtifacts_RegisteredAsBuiltin(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)
	for _, name := range []string{
		"host.cypilot_artifacts",
		"host.cypilot_artifacts.list",
		"host.cypilot_artifacts.get",
		"host.cypilot_artifacts.create",
		"host.cypilot_artifacts.validate",
		"host.cypilot_artifacts.decompose",
	} {
		if _, ok := r.Get(name); !ok {
			t.Fatalf("registry: %s missing (prefix-fallback should resolve)", name)
		}
	}
}

func TestCypilotArtifacts_MissingOp(t *testing.T) {
	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error for missing op")
	}
}

func TestCypilotArtifacts_CptMissing(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{err: fmt.Errorf("cpt not on PATH")}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op": "list",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when cpt missing")
	}
	if !strings.Contains(res.Error, "cpt CLI not available") {
		t.Fatalf("error should mention cpt: %s", res.Error)
	}
}

func TestCypilotArtifacts_List_HappyJSONArray(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	fr.responses["cpt artifact list --json --kind prd"] = fakeResp{
		stdout: `[{"id":"prd-001","kind":"prd","title":"Auth flow PRD","path":"cypilot/artifacts/prd/auth.md","status":"validated"}]`,
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op":   "list",
		"kind": "prd",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	list, _ := res.Data["artifacts"].([]map[string]any)
	if len(list) != 1 {
		t.Fatalf("expected 1, got %d (%v)", len(list), res.Data)
	}
	if list[0]["title"] != "Auth flow PRD" {
		t.Fatalf("title: %v", list[0]["title"])
	}
}

func TestCypilotArtifacts_List_PlainTextFallback(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	fr.defaultResp = fakeResp{
		stdout: "cypilot/artifacts/prd/auth.md\ncypilot/artifacts/prd/billing.md\n",
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op": "list",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	list, _ := res.Data["artifacts"].([]map[string]any)
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d (%v)", len(list), res.Data)
	}
	if list[0]["path"] != "cypilot/artifacts/prd/auth.md" {
		t.Fatalf("path: %v", list[0]["path"])
	}
}

func TestCypilotArtifacts_List_ErrorPropagates(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	fr.defaultResp = fakeResp{stderr: "cpt: invalid kind", code: 2}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op":   "list",
		"kind": "xxxx",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when cpt exit != 0")
	}
	if !strings.Contains(res.Error, "invalid kind") {
		t.Fatalf("error should propagate stderr: %s", res.Error)
	}
}

func TestCypilotArtifacts_Get_FromPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prd-auth.md")
	body := `---
title: Auth PRD
kind: prd
depends_on:
  - kit-001
---

# Auth PRD

The PRD body.
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op":   "get",
		"id":   "prd-auth",
		"path": path,
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["title"] != "Auth PRD" {
		t.Fatalf("title: %v", res.Data["title"])
	}
	if res.Data["kind"] != "prd" {
		t.Fatalf("kind: %v", res.Data["kind"])
	}
	deps, _ := res.Data["depends_on"].([]any)
	if len(deps) != 1 {
		t.Fatalf("depends_on: %v", deps)
	}
	bodyOut, _ := res.Data["body"].(string)
	if !strings.Contains(bodyOut, "The PRD body.") {
		t.Fatalf("body missing: %q", bodyOut)
	}
}

func TestCypilotArtifacts_Get_NotFound(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op":   "get",
		"id":   "nope",
		"path": "/nonexistent/path.md",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error for missing file")
	}
}

func TestCypilotArtifacts_Get_RequiresIDOrPath(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op": "get",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error without id or path")
	}
}

func TestCypilotArtifacts_Create_Happy(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	fr.responses["cpt generate --json --kind prd --title Auth flow PRD --slug auth-flow"] = fakeResp{
		stdout: `{"id":"prd-auth-flow","path":"cypilot/artifacts/prd/auth-flow.md"}`,
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op":    "create",
		"kind":  "prd",
		"title": "Auth flow PRD",
		"slug":  "auth-flow",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["id"] != "prd-auth-flow" {
		t.Fatalf("id: %v", res.Data["id"])
	}
	if res.Data["path"] != "cypilot/artifacts/prd/auth-flow.md" {
		t.Fatalf("path: %v", res.Data["path"])
	}
}

func TestCypilotArtifacts_Create_RequiresKind(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op":    "create",
		"title": "x",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when kind missing")
	}
}

func TestCypilotArtifacts_Create_RequiresTitle(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op":   "create",
		"kind": "prd",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when title missing")
	}
}

func TestCypilotArtifacts_Validate_PassEnvelope(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	fr.responses["cpt analyze --json --target prd-auth-flow --mode deterministic"] = fakeResp{
		stdout: `{"status":"PASS","findings":[]}`,
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op":   "validate",
		"id":   "prd-auth-flow",
		"mode": "deterministic",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["ok"] != true {
		t.Fatalf("ok=true expected: %v", res.Data["ok"])
	}
}

func TestCypilotArtifacts_Validate_FailEnvelopeStillReturnsReport(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	// cpt analyze exits non-zero when there are findings; the handler
	// surfaces ok=false but DOES NOT error — the LLM-judge needs the
	// findings + report to reason on the failure.
	fr.responses["cpt analyze --json --target prd-auth-flow"] = fakeResp{
		stdout: `{"status":"FAIL","findings":[{"id":"AP-005","severity":"high","message":"missing checklist"}]}`,
		code:   2,
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op": "validate",
		"id": "prd-auth-flow",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("validate-fail should NOT produce Result.Error (judge reads ok=false): %s", res.Error)
	}
	if res.Data["ok"] != false {
		t.Fatalf("ok=false expected on non-zero exit: %v", res.Data["ok"])
	}
	findings, _ := res.Data["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("findings: %v", findings)
	}
	report, _ := res.Data["report"].(string)
	if !strings.Contains(report, "missing checklist") {
		t.Fatalf("report should contain stderr/stdout: %q", report)
	}
}

func TestCypilotArtifacts_Validate_RequiresID(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op": "validate",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when id missing")
	}
}

func TestCypilotArtifacts_Decompose_Happy(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	fr.responses["cpt plan --json --task prd-auth-flow"] = fakeResp{
		stdout: `{"plan_path":".plans/auth-flow","phase_count":7}`,
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op": "decompose",
		"id": "prd-auth-flow",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["plan_path"] != ".plans/auth-flow" {
		t.Fatalf("plan_path: %v", res.Data["plan_path"])
	}
	if res.Data["phase_count"] != 7 {
		t.Fatalf("phase_count: %v", res.Data["phase_count"])
	}
}

func TestCypilotArtifacts_Decompose_PlainTextFallback(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	fr.defaultResp = fakeResp{
		stdout: "Generated phases under .plans/auth-flow/\n",
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op": "decompose",
		"id": "prd-auth-flow",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	path, _ := res.Data["plan_path"].(string)
	if !strings.Contains(path, ".plans/auth-flow") {
		t.Fatalf("plan_path should be scraped from stdout: %v", path)
	}
}

func TestCypilotArtifacts_Decompose_RequiresID(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op": "decompose",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when id missing")
	}
}

func TestCypilotArtifacts_UnknownOpRejected(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["cpt --version"] = fakeResp{stdout: "cpt 0.x\n"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.CypilotArtifactsHandler(context.Background(), map[string]any{
		"op": "smoke",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error for unknown op")
	}
}
