package dynamicworkflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	goyaml "github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"
)

func TestServiceCreateValidateExport(t *testing.T) {
	repoRoot, err := os.Getwd()
	require.NoError(t, err)
	repoRoot, err = filepath.Abs(filepath.Join(repoRoot, "..", ".."))
	require.NoError(t, err)

	outDir := t.TempDir()
	svc := NewService(repoRoot)
	svc.OutputDir = outDir
	svc.TemplateStoryDir = filepath.Join(repoRoot, DefaultTemplateStoryDir)
	fixedNow := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return fixedNow }

	receipt, err := svc.Create(context.Background(), CreateRequest{
		Goal: "implement dynamic workflows",
		Slug: "dynamic-workflows",
	})
	require.NoError(t, err)
	require.True(t, receipt.Validation.OK, "receipt should validate")
	require.True(t, strings.Contains(receipt.WorkflowID, "dynamic-workflows"))
	require.FileExists(t, receipt.ManifestPath)
	require.FileExists(t, receipt.EventsPath)
	require.FileExists(t, filepath.Join(receipt.AppPath, "app.yaml"))
	require.Contains(t, receipt.LaunchCommand, "kitsoki run")
	events, err := os.ReadFile(receipt.EventsPath)
	require.NoError(t, err)
	require.Contains(t, string(events), "dynamic.workflow.generated")
	require.Contains(t, string(events), "dynamic.workflow.validated")

	loaded, err := svc.ReadReceipt(receipt.WorkflowID)
	require.NoError(t, err)
	require.Equal(t, receipt.WorkflowID, loaded.WorkflowID)
	require.True(t, loaded.Validation.OK)

	exportDir := filepath.Join(t.TempDir(), "exported", "dynamic-workflows")
	receipt, err = svc.Export(context.Background(), receipt.WorkflowID, ExportRequest{TargetDir: exportDir})
	require.NoError(t, err)
	require.Equal(t, exportDir, receipt.ExportPath)
	require.FileExists(t, filepath.Join(exportDir, "app", "app.yaml"))
	require.FileExists(t, filepath.Join(exportDir, "manifest.yaml"))
	require.FileExists(t, filepath.Join(exportDir, "launch.yaml"))
	require.FileExists(t, filepath.Join(exportDir, "README.md"))
	require.FileExists(t, filepath.Join(exportDir, "export-report.json"))
	require.FileExists(t, filepath.Join(exportDir, "flows", "generated.yaml"))
	events, err = os.ReadFile(receipt.EventsPath)
	require.NoError(t, err)
	require.Contains(t, string(events), "dynamic.workflow.exported")

	manifest, err := readManifest(filepath.Join(exportDir, "manifest.yaml"))
	require.NoError(t, err)
	for _, item := range manifest.Items {
		require.True(t, strings.HasPrefix(item.Story, filepath.ToSlash(filepath.Join(exportDir, "app"))))
		require.True(t, strings.HasPrefix(item.ImplementationStory, filepath.ToSlash(filepath.Join(exportDir, "app"))))
	}

	var launch map[string]any
	b, err := os.ReadFile(filepath.Join(exportDir, "launch.yaml"))
	require.NoError(t, err)
	require.NoError(t, goyaml.Unmarshal(b, &launch))
	world := launch["world"].(map[string]any)
	require.Equal(t, filepath.ToSlash(filepath.Join(exportDir, "manifest.yaml")), world["manifest_path"])
}

func TestServiceCreatePreservesSyntheticGLMCoverageFanout(t *testing.T) {
	repoRoot, err := os.Getwd()
	require.NoError(t, err)
	repoRoot, err = filepath.Abs(filepath.Join(repoRoot, "..", ".."))
	require.NoError(t, err)

	outDir := filepath.Join(repoRoot, ".artifacts", "dynamic-workflows-test", strings.ReplaceAll(t.Name(), "/", "-"))
	t.Cleanup(func() { _ = os.RemoveAll(outDir) })
	svc := NewService(repoRoot)
	svc.OutputDir = outDir
	svc.TemplateStoryDir = filepath.Join(repoRoot, DefaultTemplateStoryDir)
	svc.Now = func() time.Time { return time.Date(2026, 7, 6, 5, 30, 0, 0, time.UTC) }

	receipt, err := svc.Create(context.Background(), CreateRequest{
		Goal: "fan out glm-5.2 agents with the claude-synthetic harness to get Go, TypeScript/JavaScript, stories, and e2e coverage toward 80%",
		Slug: "glm-coverage",
	})
	require.NoError(t, err)
	require.True(t, receipt.Validation.OK)

	manifest, err := readManifest(receipt.ManifestPath)
	require.NoError(t, err)
	require.Equal(t, "synthetic-claude", manifest.Defaults.Profile)
	require.Equal(t, "hf:zai-org/GLM-5.2", manifest.Defaults.Model)
	require.Equal(t, "ladder", manifest.Defaults.Harness)
	require.False(t, manifest.Defaults.RequireTraceModel)
	require.NotNil(t, manifest.Defaults.HarnessLadder)
	require.Equal(t, []HarnessLadderModel{
		{Backend: "claude", Provider: "synthetic-claude", Model: "hf:zai-org/GLM-5.2"},
		{Backend: "codex", Provider: "codex-native", Model: "gpt-5.5"},
	}, manifest.Defaults.HarnessLadder.Models)
	require.Equal(t, ".artifacts/dynamic-workflows-test/TestServiceCreatePreservesSyntheticGLMCoverageFanout/dwf_20260706T053000Z_glm-coverage/traces", manifest.Defaults.TraceRoot)

	ids := make([]string, 0, len(manifest.Items))
	for _, item := range manifest.Items {
		ids = append(ids, item.ID)
		require.Contains(t, item.Prompt, "coverage")
	}
	require.Equal(t, []string{
		"measure-coverage",
		"go-coverage",
		"story-workflow-coverage",
		"js-ts-coverage",
		"e2e-flow-coverage",
		"supervisor-verify",
	}, ids)

	var launch map[string]any
	b, err := os.ReadFile(receipt.LaunchBasisPath)
	require.NoError(t, err)
	require.NoError(t, goyaml.Unmarshal(b, &launch))
	world := launch["world"].(map[string]any)
	require.Equal(t, ".artifacts/dynamic-workflows-test/TestServiceCreatePreservesSyntheticGLMCoverageFanout/dwf_20260706T053000Z_glm-coverage/manifest.yaml", world["manifest_path"])
}

func TestServiceCreateResearchTestingApproachesFanout(t *testing.T) {
	repoRoot, err := os.Getwd()
	require.NoError(t, err)
	repoRoot, err = filepath.Abs(filepath.Join(repoRoot, "..", ".."))
	require.NoError(t, err)

	outDir := filepath.Join(repoRoot, ".artifacts", "dynamic-workflows-test", strings.ReplaceAll(t.Name(), "/", "-"))
	t.Cleanup(func() { _ = os.RemoveAll(outDir) })
	svc := NewService(repoRoot)
	svc.OutputDir = outDir
	svc.TemplateStoryDir = filepath.Join(repoRoot, DefaultTemplateStoryDir)
	svc.Now = func() time.Time { return time.Date(2026, 7, 6, 5, 45, 0, 0, time.UTC) }

	receipt, err := svc.Create(context.Background(), CreateRequest{
		Goal: "research the different testing approaches in the repo with a dynamic workflow; inspect Go tests, TypeScript/JavaScript tests, story flow fixtures, Playwright/e2e tests, coverage gates, cassettes, and no-LLM policies; write a concise research report under .context and propose follow-up validation gates",
		Slug: "testing-approaches-research",
	})
	require.NoError(t, err)
	require.True(t, receipt.Validation.OK)

	manifest, err := readManifest(receipt.ManifestPath)
	require.NoError(t, err)
	require.Equal(t, "codex-native", manifest.Defaults.Profile)
	require.Equal(t, "gpt-5.5", manifest.Defaults.Model)

	ids := make([]string, 0, len(manifest.Items))
	for _, item := range manifest.Items {
		ids = append(ids, item.ID)
		require.Contains(t, item.Prompt, ".context")
		require.Contains(t, item.Prompt, "Goal: research")
		require.NotContains(t, item.Title, "Add Go coverage")
		require.NotContains(t, item.Prompt, "Add focused deterministic Go tests")
	}
	require.Equal(t, []string{
		"research-scope",
		"go-testing-research",
		"js-e2e-testing-research",
		"story-flow-research",
		"research-synthesis",
	}, ids)
}

func TestValidateManifestRejectsRuntimeLiveModelPolicyMismatch(t *testing.T) {
	repoRoot, err := os.Getwd()
	require.NoError(t, err)
	repoRoot, err = filepath.Abs(filepath.Join(repoRoot, "..", ".."))
	require.NoError(t, err)

	errs := validateManifest(repoRoot, Manifest{
		Version: ManifestVersion,
		Defaults: ManifestDefaults{
			Harness:   "live",
			Profile:   "synthetic-claude",
			Model:     "hf:zai-org/GLM-5.2",
			TraceRoot: ".artifacts/test/traces",
		},
		Items: []ManifestItem{
			{
				ID:    "bad-live-model",
				Title: "Bad live model",
				Story: "stories/punch-list/app.yaml",
				Mode:  "drive",
				Verify: []ManifestVerify{
					{Kind: "story_validate", Story: "stories/punch-list/app.yaml"},
				},
			},
		},
	}, filepath.Join(repoRoot, DefaultTemplateStoryDir))
	require.Contains(t, strings.Join(errs, "\n"), "live work must use profile codex-native or harness: ladder")
	require.Contains(t, strings.Join(errs, "\n"), "live work must use model gpt-5.5 or harness: ladder")
}

func TestServiceExportBlocksBaseStoryWithoutApproval(t *testing.T) {
	repoRoot, err := os.Getwd()
	require.NoError(t, err)
	repoRoot, err = filepath.Abs(filepath.Join(repoRoot, "..", ".."))
	require.NoError(t, err)

	outDir := t.TempDir()
	svc := NewService(repoRoot)
	svc.OutputDir = outDir
	svc.TemplateStoryDir = filepath.Join(repoRoot, DefaultTemplateStoryDir)
	svc.Now = func() time.Time { return time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC) }

	receipt, err := svc.Create(context.Background(), CreateRequest{
		Goal: "implement dynamic workflows",
		Slug: "dynamic-workflows",
	})
	require.NoError(t, err)

	_, err = svc.Export(context.Background(), receipt.WorkflowID, ExportRequest{
		TargetDir: filepath.Join(repoRoot, "internal", "basestories", "stories", "dynamic-workflows"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "base-story export")
}
