//go:build onboardsmoke || onboardsisters

package onboardsmoke

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type onboardReportPaths struct {
	Summary string
	Report  string
	Deck    string
}

type onboardSisterReportItem struct {
	ProjectID      string
	ProjectTitle   string
	Stack          string
	BaselineCommit string
	ConfigPath     string
	ProfilePath    string
	InstancePath   string
}

func writeOnboardSistersReport(kitsokiRoot, runID string, items []onboardSisterReportItem) (onboardReportPaths, error) {
	if len(items) == 0 {
		return onboardReportPaths{}, fmt.Errorf("onboard sisters report needs at least one item")
	}
	summaryItems := make([]map[string]any, 0, len(items))
	for _, item := range items {
		summaryItems = append(summaryItems, map[string]any{
			"id":       item.ProjectID,
			"status":   "succeeded",
			"attempts": 1,
			"owner":    item.ProjectTitle,
			"artifact": item.InstancePath,
			"detail":   item.Stack,
		})
	}
	return writeReportDeck(kitsokiRoot, "fanout", filepath.Join("onboard-smoke", cleanRunID(runID)), map[string]any{
		"title": "Onboarding Sister Replay",
		"items": summaryItems,
		"next_steps": []map[string]any{
			{"label": "Review generated instances", "detail": "Inspect .kitsoki project profiles and generated story instances for each replay."},
			{"label": "Refresh baselines deliberately", "detail": "Only move pinned commits after the generated app load checks pass."},
		},
	})
}

func writePinnedOnboardReport(kitsokiRoot, runID string, data map[string]any) (onboardReportPaths, error) {
	return writeReportDeck(kitsokiRoot, "onboarding", filepath.Join("onboard-smoke", cleanRunID(runID)), data)
}

func writeReportDeck(kitsokiRoot, kind, artifactSubdir string, summary map[string]any) (onboardReportPaths, error) {
	outDir, err := allocateArtifactDir(filepath.Join(kitsokiRoot, ".artifacts"), artifactSubdir)
	if err != nil {
		return onboardReportPaths{}, err
	}
	paths := onboardReportPaths{
		Summary: filepath.Join(outDir, "summary.json"),
		Report:  filepath.Join(outDir, "report.md"),
		Deck:    filepath.Join(outDir, "deck.slidey.json"),
	}
	b, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return onboardReportPaths{}, err
	}
	b = append(b, '\n')
	if err := os.WriteFile(paths.Summary, b, 0o644); err != nil {
		return onboardReportPaths{}, err
	}
	deckTool := filepath.Join(kitsokiRoot, "tools", "report-deck", "deterministic_deck.py")
	cmd := exec.Command("python3", deckTool, "--kind", kind, "--input", paths.Summary, "--out", paths.Deck, "--markdown", paths.Report)
	cmd.Dir = kitsokiRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if out, err := cmd.Output(); err != nil {
		return onboardReportPaths{}, fmt.Errorf("render %s report deck: %w\n%s%s", kind, err, out, stderr.String())
	}
	return paths, nil
}

func allocateArtifactDir(root, subdir string) (string, error) {
	base := filepath.Join(root, subdir)
	parent := filepath.Dir(base)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	for i := 1; i <= 999; i++ {
		candidate := base
		if i > 1 {
			candidate = fmt.Sprintf("%s-%03d", base, i)
		}
		err := os.Mkdir(candidate, 0o755)
		if err == nil {
			return candidate, nil
		}
		if os.IsExist(err) {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("no free artifact directory for %s", base)
}

func reportRepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("resolve kitsoki repo root: %w", err)
	}
	return root, nil
}

func cleanRunID(runID string) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "latest"
	}
	var b strings.Builder
	for _, r := range runID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	clean := strings.Trim(b.String(), "-.")
	if clean == "" {
		return "latest"
	}
	return clean
}
