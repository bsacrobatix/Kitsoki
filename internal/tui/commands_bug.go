package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kitsoki/internal/app"
	"kitsoki/internal/bugfile"
	"kitsoki/internal/host"
	"kitsoki/internal/reportmeta"
	"kitsoki/internal/runstatus/harscrub"
	"kitsoki/internal/tui/blocks"
)

// BugCommand implements `/bug [description]` for the TUI.
//
// With no ticket repo configured it preserves the historical local
// issues/bugs/ behavior. With a ticket repo configured it files through
// host.GitHubFileBug with UploadArtifacts=true, so the TUI transcript and
// context evidence follow the same uploaded-evidence path as web bug reports.
type BugCommand struct{}

func (BugCommand) Name() string { return "/bug" }

func (BugCommand) Run(m RootModel, args []string) (string, RootModel, tea.Cmd) {
	desc := strings.TrimSpace(strings.Join(args, " "))
	scrubbedDesc := scrubBugText(desc)
	title := tuiBugTitle(scrubbedDesc)
	body := tuiBugBody(m, scrubbedDesc)

	root, err := m.resolveBugRoot()
	if err != nil {
		return m.bugBlock(fmt.Sprintf("could not resolve target root: %v", err)), m, nil
	}

	if strings.TrimSpace(m.bugTicketRepo) != "" {
		msg := m.fileGitHubBug(root, title, body)
		return m.bugBlock(msg), m, nil
	}
	runtime := reportmeta.Capture(root, m.appDefForBug())

	id, relPath, absPath, err := bugfile.Create(bugfile.CreateRequest{
		Target:    "story",
		Title:     title,
		Body:      body,
		AppID:     m.appIDForBug(),
		StatePath: string(m.currentState),
		Severity:  "med",
		TraceRef:  scrubBugText(m.traceFilePath),
		TargetDir: root,
		FiledBy:   os.Getenv("USER"),
		Runtime:   runtime,
		Warnf:     func(string, ...any) {},
	})
	if err != nil {
		return m.bugBlock(fmt.Sprintf("could not file bug: %v", err)), m, nil
	}

	artifactsDir := strings.TrimSuffix(absPath, ".md") + ".artifacts"
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return m.bugBlock(fmt.Sprintf("filed %s, but could not create artifacts: %v", relPath, err)), m, nil
	}
	if err := m.writeBugArtifacts(artifactsDir, runtime); err != nil {
		return m.bugBlock(fmt.Sprintf("filed %s, but could not write artifacts: %v", relPath, err)), m, nil
	}
	if err := appendTUIArtifactsSection(absPath, id); err != nil {
		return m.bugBlock(fmt.Sprintf("filed %s, but could not append artifact links: %v", relPath, err)), m, nil
	}

	return m.bugBlock(fmt.Sprintf("filed %s", filepath.ToSlash(relPath))), m, nil
}

func (m RootModel) fileGitHubBug(root, title, body string) string {
	runtime := reportmeta.Capture(root, m.appDefForBug())
	prefix := "tui-bug-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	artifactsDir := filepath.Join(root, ".artifacts", "bug-reports", prefix)
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return fmt.Sprintf("could not create artifacts: %v", err)
	}
	if err := m.writeBugArtifacts(artifactsDir, runtime); err != nil {
		return fmt.Sprintf("could not write artifacts: %v", err)
	}

	displayRoot := filepath.ToSlash(filepath.Join(".artifacts", "bug-reports", prefix))
	evidence := []host.EvidenceFile{
		{
			Name:       "transcript.md",
			Path:       filepath.ToSlash(filepath.Join(displayRoot, "transcript.md")),
			SourcePath: filepath.Join(artifactsDir, "transcript.md"),
			Label:      "TUI transcript (scrubbed)",
		},
		{
			Name:       "context.json",
			Path:       filepath.ToSlash(filepath.Join(displayRoot, "context.json")),
			SourcePath: filepath.Join(artifactsDir, "context.json"),
			Label:      "TUI session context",
		},
	}
	res, err := host.GitHubFileBug(context.Background(), host.GitHubBugFiling{
		Repo:            strings.TrimSpace(m.bugTicketRepo),
		Title:           title,
		Body:            body,
		Severity:        "med",
		Component:       "tui",
		Target:          "kitsoki",
		TraceRef:        scrubBugText(m.traceFilePath),
		KitsokiRev:      gitShortRev(root),
		FiledBy:         os.Getenv("USER"),
		Evidence:        evidence,
		Runtime:         runtime,
		UploadArtifacts: true,
	})
	if err != nil {
		return fmt.Sprintf("could not file GitHub bug: %v", err)
	}
	return fmt.Sprintf("filed %s", res.URL)
}

func tuiBugTitle(desc string) string {
	if desc == "" {
		return "tui: bug report " + time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}
	title := strings.TrimSpace(strings.Split(desc, "\n")[0])
	if len(title) > 80 {
		title = strings.TrimSpace(title[:80])
	}
	return "tui: " + title
}

func tuiBugBody(m RootModel, desc string) string {
	if desc == "" {
		desc = "Filed from the TUI with no operator description."
	}
	var sb strings.Builder
	sb.WriteString(desc)
	sb.WriteString("\n\n## TUI context\n\n")
	fmt.Fprintf(&sb, "- App: `%s`\n", m.appIDForBug())
	fmt.Fprintf(&sb, "- State: `%s`\n", m.currentState)
	fmt.Fprintf(&sb, "- Session: `%s`\n", m.sid)
	if m.appPath != "" {
		fmt.Fprintf(&sb, "- App path: `%s`\n", filepath.ToSlash(m.appPath))
	}
	if m.traceFilePath != "" {
		fmt.Fprintf(&sb, "- Trace: `%s`\n", filepath.ToSlash(m.traceFilePath))
	}
	sb.WriteString("\nSee the attached TUI transcript and context evidence captured at filing time.\n")
	return scrubBugText(sb.String())
}

func (m RootModel) appIDForBug() string {
	if m.orch == nil || m.orch.AppDef() == nil {
		return ""
	}
	return m.orch.AppDef().App.ID
}

func (m RootModel) appDefForBug() *app.AppDef {
	if m.orch == nil {
		return nil
	}
	return m.orch.AppDef()
}

func (m RootModel) resolveBugRoot() (string, error) {
	if strings.TrimSpace(m.bugRoot) != "" {
		return m.bugRoot, nil
	}
	if m.appPath != "" {
		start := m.appPath
		if info, err := os.Stat(start); err == nil && !info.IsDir() {
			start = filepath.Dir(start)
		}
		if root := nearestGitRoot(start); root != "" {
			return root, nil
		}
		return start, nil
	}
	return os.Getwd()
}

func nearestGitRoot(start string) string {
	dir := filepath.Clean(start)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func (m RootModel) writeBugArtifacts(dir string, runtime reportmeta.Snapshot) error {
	if err := os.WriteFile(filepath.Join(dir, "transcript.md"), []byte(scrubBugText(m.transcript.AllContent())), 0o644); err != nil {
		return fmt.Errorf("write transcript.md: %w", err)
	}
	meta := map[string]any{
		"app_id":     m.appIDForBug(),
		"app_path":   filepath.ToSlash(m.appPath),
		"state_path": string(m.currentState),
		"session_id": string(m.sid),
		"trace_ref":  filepath.ToSlash(m.traceFilePath),
		"filed_at":   time.Now().UTC().Format(time.RFC3339),
		"surface":    "tui",
	}
	if !runtime.Empty() {
		meta["runtime"] = runtime
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context.json: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "context.json"), []byte(scrubBugText(string(data))), 0o644); err != nil {
		return fmt.Errorf("write context.json: %w", err)
	}
	return nil
}

func appendTUIArtifactsSection(absPath, id string) error {
	section := fmt.Sprintf("\n## Artifacts\n\n- `./%s.artifacts/transcript.md` - scrubbed TUI transcript at filing time\n- `./%s.artifacts/context.json` - TUI session metadata\n", id, id)
	f, err := os.OpenFile(absPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(section)
	return err
}

func scrubBugText(s string) string {
	return harscrub.ScrubString(s, harscrub.ScrubOptions{
		Home:           os.Getenv("HOME"),
		SecretPatterns: harscrub.DefaultSecretPatterns(),
	})
}

func (m RootModel) bugBlock(line string) string {
	return blocks.New(m.transcript.width, m.currentTheme()).SlashOutput("bug: " + line)
}

func gitShortRev(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
