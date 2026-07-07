// bug_report.go — the runstatus.bug.report RPC.
//
// A web operator who hits a surprising state clicks "Report a bug". The SPA
// posts a title (+ optional body/severity/repro and a base64 screenshot) to
// /rpc. This handler snapshots the server's HAR ring buffer (the last N /rpc
// request/response pairs recorded at the choke point in handleRPC) and scrubs it
// with the LLM-free harscrub anonymizer. Local filing writes
// <root>/issues/bugs/<id>.md plus a sibling <id>.artifacts/ dir; GitHub filing
// creates the issue, saves a local .artifacts/ copy of the evidence, and uploads
// scrubbed evidence as release assets for review.
//
// Root resolution (least-surprising, deterministic):
//
//	params["target_dir"]      explicit override (tests, escape hatch)
//	s.bugRoot                 the repo `kitsoki web` was launched against (WithBugRoot)
//	story_path's directory    git toplevel of the selected story, when given
//	process cwd               last resort
//
// No LLM, no network. The HAR comes from the server's own recorder — never the
// client — so a malicious client cannot inject fabricated traffic.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/bugfile"
	"kitsoki/internal/bugprivacy"
	"kitsoki/internal/ghagent/bugdeck"
	"kitsoki/internal/host"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/reportmeta"
	"kitsoki/internal/runstatus/harscrub"
)

// BugScrubOptions is the single production redaction config for filed bug
// evidence: $HOME path substitution plus the built-in credential patterns. Used
// for HAR/rrweb/console/error evidence on the web path and for MCP-filed session
// evidence so nothing reaches committed artifacts on a $HOME-only pass.
func BugScrubOptions() harscrub.ScrubOptions {
	return harscrub.ScrubOptions{
		Home:           os.Getenv("HOME"),
		SecretPatterns: harscrub.DefaultSecretPatterns(),
	}
}

func scrubOptions() harscrub.ScrubOptions { return BugScrubOptions() }

// bugPreview handles runstatus.bug.preview. It snapshots + scrubs the server's
// HAR ring buffer right now, HOLDS that exact scrubbed snapshot under a fresh
// capture id, and returns it for the review modal to render. The confirming
// runstatus.bug.report replays the same capture id so the filed HAR is
// identical to what was reviewed. No LLM, no network.
func (s *Server) bugPreview(_ map[string]any) (any, *rpcError) {
	har := s.recorder.Snapshot()
	depth, capacity := s.recorder.Depth()
	harscrub.Scrub(har, scrubOptions())

	id := s.putCapture(har)
	return map[string]any{
		"capture_id": id,
		"har":        har,
		"depth":      depth,
		"capacity":   capacity,
	}, nil
}

// bugStatus handles runstatus.bug.status. It is intentionally a local preflight:
// GitHub mode checks only whether a credential is configured, not whether that
// credential has repo-specific issue permissions.
func (s *Server) bugStatus(ctx context.Context) (any, *rpcError) {
	repo := strings.TrimSpace(s.ticketRepo)
	if repo == "" {
		return map[string]any{
			"mode":     "local",
			"can_file": true,
		}, nil
	}
	auth := host.GitHubWriteAuthStatus(ctx)
	if auth.Configured {
		return map[string]any{
			"mode":                   "github",
			"repo":                   repo,
			"can_file":               true,
			"github_auth_configured": true,
		}, nil
	}
	return map[string]any{
		"mode":                   "github",
		"repo":                   repo,
		"can_file":               false,
		"github_auth_configured": false,
		"warning":                fmt.Sprintf("Report bug cannot file to GitHub repo %s because GitHub auth is missing. Filing bugs is critical; run `kitsoki gh-agent login` or set GH_TOKEN/GITHUB_TOKEN.", repo),
		"setup_hint":             auth.SetupHint,
	}, nil
}

// bugReport handles runstatus.bug.report for tests and legacy in-package
// callers. The HTTP/RPC dispatcher uses bugReportContext so provider-backed
// privacy checks receive the live request context.
func (s *Server) bugReport(params map[string]any) (any, *rpcError) {
	return s.bugReportContext(context.Background(), params)
}

// bugReportContext handles runstatus.bug.report. See the file comment for the
// request/result contract.
func (s *Server) bugReportContext(ctx context.Context, params map[string]any) (any, *rpcError) {
	title := strings.TrimSpace(stringParam(params, "title"))
	if title == "" {
		title = "web: bug report " + time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}

	// Prefer the operator's `description` (the review-modal prose); fall back to
	// the legacy `body` param, then a default pointer at the HAR artifact.
	body := stringParam(params, "description")
	if strings.TrimSpace(body) == "" {
		body = stringParam(params, "body")
	}
	if strings.TrimSpace(body) == "" {
		body = "Filed from the web UI. See the captured request/response trace under ./" +
			"<id>.artifacts/har.json for the interactions leading up to this report."
	}

	severity := stringParam(params, "severity")
	traceRef := stringParam(params, "trace_ref")
	repro := stringSliceParam(params, "repro_steps")

	root := s.resolveBugRoot(params)
	runtime := s.bugRuntimeSnapshot(root, params)
	scrubOpts := scrubOptions()

	// HAR source: if a capture_id from a prior runstatus.bug.preview is supplied
	// and still held, file that EXACT already-scrubbed snapshot (do not re-scrub
	// — it was scrubbed at preview time). Otherwise fall back to a fresh
	// snapshot + scrub so direct callers keep working.
	var har *harscrub.Har
	if capID := strings.TrimSpace(stringParam(params, "capture_id")); capID != "" {
		if held, ok := s.takeCapture(capID); ok {
			har = held
		}
	}
	if har == nil {
		har = s.recorder.Snapshot()
		harscrub.Scrub(har, scrubOpts)
	}
	depth, capacity := s.recorder.Depth()
	harJSON, err := harscrub.Marshal(har)
	if err != nil {
		return nil, serverErr(fmt.Errorf("marshal scrubbed har: %w", err))
	}

	// Decode + scrub the client-supplied evidence payloads. Each is best-effort:
	// a malformed payload is dropped rather than failing the whole report.
	rrwebJSON := decodeRRWebEvents(stringParam(params, "rrweb_events"), scrubOpts)
	consoleJSON, consoleEntries := decodeConsoleLogs(stringParam(params, "console_logs"), scrubOpts)
	errInfo := decodeErrorInfo(stringParam(params, "error_info"), scrubOpts)
	traceJSON := s.decodeTraceEvidence(params, scrubOpts)

	// Enrich the prose body with the captured error + console state.
	body = body + errorStateSection(errInfo) + consoleSection(consoleEntries)

	png := decodeScreenshot(stringParam(params, "screenshot_png_b64"))

	report := bugprivacy.Report{
		Surface:    "web",
		Target:     nonEmpty(stringParam(params, "target"), "kitsoki"),
		Title:      title,
		Body:       body,
		ReproSteps: repro,
		Component:  nonEmpty(stringParam(params, "component"), "web"),
		TraceRef:   traceRef,
		ArtifactNames: bugArtifactNames(map[string][]byte{
			"screenshot.png":       png,
			"har.json":             harJSON,
			"rrweb.json":           rrwebJSON,
			"console.json":         consoleJSON,
			"trace.redacted.jsonl": traceJSON,
		}),
	}
	safeReport, privacy, perr := bugprivacy.Check(ctx, s.bugPrivacyCheckerForReport(params), report, scrubOpts, root, stringParam(params, "filed_by"))
	if perr != nil {
		return nil, serverErr(fmt.Errorf("bug privacy check: %w", perr))
	}
	if privacy.Blocked() {
		return nil, serverErr(fmt.Errorf("%s%s", privacy.Message, privacyFollowUpSuffix(privacy)))
	}
	title = safeReport.Title
	body = safeReport.Body
	repro = safeReport.ReproSteps
	traceRef = safeReport.TraceRef
	params = cloneParams(params)
	params["target"] = safeReport.Target
	params["component"] = safeReport.Component

	// GitHub mode (kitsoki web --ticket-repo): file a real GitHub issue and save
	// evidence under .artifacts for developer-local review, instead of writing a
	// local issues/bugs/<id>.md file.
	if s.ticketRepo != "" {
		return s.fileBugToGitHub(params, title, body, severity, traceRef, repro, harJSON, png, rrwebJSON, consoleJSON, traceJSON, runtime, privacy)
	}

	id, relPath, absPath, err := bugfile.Create(bugfile.CreateRequest{
		Target:     "story",
		Title:      title,
		Body:       body,
		ReproSteps: repro,
		Severity:   severity,
		TraceRef:   traceRef,
		TargetDir:  root,
		FiledBy:    stringParam(params, "filed_by"),
		Runtime:    runtime,
		Warnf:      func(string, ...any) {}, // web caller: drop warnings
	})
	if err != nil {
		return nil, serverErr(err)
	}

	// Artifacts dir is a sibling of the .md: issues/bugs/<id>.artifacts/.
	artifactsDir := strings.TrimSuffix(absPath, ".md") + ".artifacts"
	wroteScreenshot := false
	if mkErr := os.MkdirAll(artifactsDir, 0o755); mkErr == nil {
		if wErr := os.WriteFile(filepath.Join(artifactsDir, "har.json"), harJSON, 0o644); wErr != nil {
			return nil, serverErr(fmt.Errorf("write har.json: %w", wErr))
		}
		if png != nil {
			if wErr := os.WriteFile(filepath.Join(artifactsDir, "screenshot.png"), png, 0o644); wErr == nil {
				wroteScreenshot = true
			}
		}
		if len(rrwebJSON) > 0 {
			if wErr := os.WriteFile(filepath.Join(artifactsDir, "rrweb.json"), rrwebJSON, 0o644); wErr != nil {
				return nil, serverErr(fmt.Errorf("write rrweb.json: %w", wErr))
			}
		}
		if len(consoleJSON) > 0 {
			if wErr := os.WriteFile(filepath.Join(artifactsDir, "console.json"), consoleJSON, 0o644); wErr != nil {
				return nil, serverErr(fmt.Errorf("write console.json: %w", wErr))
			}
		}
		if len(traceJSON) > 0 {
			if wErr := os.WriteFile(filepath.Join(artifactsDir, "trace.redacted.jsonl"), traceJSON, 0o644); wErr != nil {
				return nil, serverErr(fmt.Errorf("write trace.redacted.jsonl: %w", wErr))
			}
		}
	} else {
		return nil, serverErr(fmt.Errorf("mkdir artifacts: %w", mkErr))
	}

	// Append an Artifacts section linking the sidecar files relatively, plus the
	// recorder horizon so a reader knows how much history the HAR covers.
	arts := artifactLinks{
		hasScreenshot: wroteScreenshot,
		hasRRWeb:      len(rrwebJSON) > 0,
		hasConsole:    len(consoleJSON) > 0,
		hasTrace:      len(traceJSON) > 0,
	}
	if appendErr := appendArtifactsSection(absPath, id, arts, depth, capacity); appendErr != nil {
		return nil, serverErr(fmt.Errorf("append artifacts section: %w", appendErr))
	}

	return map[string]any{"id": id, "path": relPath, "privacy": privacy}, nil
}

// fileBugToGitHub files the bug as a real GitHub issue on s.ticketRepo: it
// writes the (already-scrubbed) evidence to .artifacts as a local copy, hands
// those paths to host.GitHubFileBug with UploadArtifacts set so the evidence is
// uploaded as release assets and linked by public URL in the issue, and returns
// the issue url. No local issues/bugs/*.md file is written in this mode.
func (s *Server) fileBugToGitHub(params map[string]any, title, body, severity, traceRef string, repro []string, harJSON, png, rrwebJSON, consoleJSON, traceJSON []byte, runtime reportmeta.Snapshot, privacy bugprivacy.Result) (any, *rpcError) {
	prefix := "bug-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	artifactsRoot, displayRoot, err := s.githubBugArtifactsRoot()
	if err != nil {
		return nil, serverErr(fmt.Errorf("github bug: artifacts dir: %w", err))
	}
	artifactsDir := filepath.Join(artifactsRoot, prefix)
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return nil, serverErr(fmt.Errorf("github bug: mkdir artifacts: %w", err))
	}

	var ev []host.EvidenceFile
	add := func(base string, data []byte, image bool, label string) error {
		if len(data) == 0 {
			return nil
		}
		p := filepath.Join(artifactsDir, base)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
		ev = append(ev, host.EvidenceFile{
			Name:       base,
			Path:       filepath.ToSlash(filepath.Join(displayRoot, prefix, base)), // display-only reference
			SourcePath: p,                                                          // real file to upload
			Image:      image,
			Label:      label,
		})
		return nil
	}
	for _, artifact := range []struct {
		base  string
		data  []byte
		image bool
		label string
	}{
		{base: "screenshot.png", data: png, image: true, label: "Screenshot"},
		{base: "har.json", data: harJSON, label: "HAR capture (scrubbed)"},
		{base: "rrweb.json", data: rrwebJSON, label: "Session replay (rrweb)"},
		{base: "console.json", data: consoleJSON, label: "Console log"},
		{base: "trace.redacted.jsonl", data: traceJSON, label: "Session trace (redacted)"},
	} {
		if err := add(artifact.base, artifact.data, artifact.image, artifact.label); err != nil {
			return nil, serverErr(fmt.Errorf("github bug: write artifact %s: %w", artifact.base, err))
		}
	}

	full := body
	if len(repro) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\n## Steps to reproduce\n\n")
		for i, r := range repro {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, r)
		}
		full += sb.String()
	}

	res, ferr := host.GitHubFileBug(context.Background(), host.GitHubBugFiling{
		Repo:       s.ticketRepo,
		Title:      title,
		Body:       full,
		Severity:   severity,
		Component:  nonEmpty(stringParam(params, "component"), "web"),
		Target:     nonEmpty(stringParam(params, "target"), "kitsoki"),
		TraceRef:   traceRef,
		KitsokiRev: gitShortRev(s.bugRoot),
		FiledBy:    stringParam(params, "filed_by"),
		Evidence:   ev,
		Runtime:    runtime,
		// We are already online and gh-authed to file the issue itself, so upload
		// the scrubbed evidence as release assets and link the public URLs —
		// otherwise the issue body would point at developer-local paths nobody
		// else can open. Upload failures degrade gracefully to those local paths.
		UploadArtifacts: true,
	})
	if ferr != nil {
		return nil, serverErr(fmt.Errorf("file bug to github (%s): %w", s.ticketRepo, ferr))
	}

	// Deposit the scrubbed rrweb + HAR onto the kitsoki github agent, keyed by the
	// same DeckID the agent's issues.opened webhook will look up — so it produces
	// the hosted no-LLM deck without re-downloading anything. Best-effort: a
	// deposit failure must not fail the (already-filed) bug report.
	s.depositAgentEvidence(res.Number, rrwebJSON, harJSON)

	return map[string]any{"id": res.Number, "url": res.URL, "github": true, "privacy": privacy}, nil
}

func (s *Server) bugRuntimeSnapshot(root string, params map[string]any) reportmeta.Snapshot {
	var def *app.AppDef
	storyPath := strings.TrimSpace(stringParam(params, "story_path"))
	if storyPath != "" {
		var storyDir string
		if ep, ok := s.editorProvider(); ok {
			if _, dir, ok := ep.EditorApp(storyPath); ok {
				storyDir = dir
			}
		}
		for _, candidate := range []string{storyPath, filepath.Join(storyDir, "app.yaml")} {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			if loaded, err := app.Load(candidate); err == nil {
				def = loaded
				break
			}
		}
	}
	return reportmeta.Capture(root, def)
}

// depositAgentEvidence writes the scrubbed rrweb + HAR into the configured agent
// evidence store under bugdeck.DeckID(ticketRepo, issueNumber). No-op when no
// agent evidence dir is configured. Best-effort: errors are swallowed (the bug
// is already filed; the agent simply skips deck generation if evidence is
// absent).
func (s *Server) depositAgentEvidence(issueNumber string, rrwebJSON, harJSON []byte) {
	if strings.TrimSpace(s.agentEvidenceDir) == "" || strings.TrimSpace(issueNumber) == "" {
		return
	}
	store, err := bugdeck.NewEvidenceStore(s.agentEvidenceDir)
	if err != nil {
		return
	}
	_ = store.Save(bugdeck.DeckID(s.ticketRepo, issueNumber), rrwebJSON, harJSON)
}

func (s *Server) githubBugArtifactsRoot() (absRoot, displayRoot string, err error) {
	root := s.bugRoot
	if strings.TrimSpace(root) == "" {
		root, err = os.Getwd()
		if err != nil {
			return "", "", err
		}
	}
	absRoot = filepath.Join(root, ".artifacts", "bug-reports")
	return absRoot, filepath.ToSlash(filepath.Join(".artifacts", "bug-reports")), nil
}

// gitShortRev returns the short HEAD sha of the repo containing dir (best-effort;
// "" when dir is empty / not a repo / git is unavailable).
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

// resolveBugRoot picks the repo root for a web-filed bug. Precedence: explicit
// target_dir param, the server's configured bugRoot, the story_path's repo
// (git toplevel of its on-disk directory), then the process cwd.
func (s *Server) resolveBugRoot(params map[string]any) string {
	if td := strings.TrimSpace(stringParam(params, "target_dir")); td != "" {
		return td
	}
	if s.bugRoot != "" {
		return s.bugRoot
	}
	if storyPath := strings.TrimSpace(stringParam(params, "story_path")); storyPath != "" {
		if ep, ok := s.editorProvider(); ok {
			if _, storyDir, ok := ep.EditorApp(storyPath); ok && storyDir != "" {
				if top := gitToplevel(storyDir); top != "" {
					return top
				}
				return storyDir
			}
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

// gitToplevel returns the git working-tree root containing dir, or "" if dir is
// not in a git repo (or git is unavailable).
func gitToplevel(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// decodeScreenshot decodes a base64 PNG payload, tolerating a data-URL prefix.
// Returns nil on any error or empty input (screenshot is best-effort).
func decodeScreenshot(b64 string) []byte {
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		return nil
	}
	if i := strings.Index(b64, ","); strings.HasPrefix(b64, "data:") && i >= 0 {
		b64 = b64[i+1:]
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	return data
}

// artifactLinks records which optional sidecar files were written, so the
// Artifacts markdown section links only the ones that exist.
type artifactLinks struct {
	hasScreenshot bool
	hasRRWeb      bool
	hasConsole    bool
	hasTrace      bool
}

// appendArtifactsSection appends a "## Artifacts" block to the bug markdown,
// linking the sidecar files relatively and noting the HAR recorder horizon.
func appendArtifactsSection(absPath, id string, arts artifactLinks, depth, capacity int) error {
	var sb strings.Builder
	sb.WriteString("\n## Artifacts\n\n")
	if arts.hasScreenshot {
		fmt.Fprintf(&sb, "- Screenshot: ./%s.artifacts/screenshot.png\n", id)
	}
	fmt.Fprintf(&sb, "- HAR capture (scrubbed): ./%s.artifacts/har.json\n", id)
	if arts.hasRRWeb {
		fmt.Fprintf(&sb, "- Session replay (rrweb): ./%s.artifacts/rrweb.json\n", id)
	}
	if arts.hasConsole {
		fmt.Fprintf(&sb, "- Console log: ./%s.artifacts/console.json\n", id)
	}
	if arts.hasTrace {
		fmt.Fprintf(&sb, "- Session trace (redacted): ./%s.artifacts/trace.redacted.jsonl\n", id)
	}
	fmt.Fprintf(&sb, "\nThe HAR retains the %d most-recent /rpc exchange(s) (ring-buffer capacity %d).\n", depth, capacity)

	f, err := os.OpenFile(absPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(sb.String())
	return err
}

// consoleEntry is one captured browser console line.
type consoleEntry struct {
	Level string `json:"level"`
	TS    any    `json:"ts"`
	Text  string `json:"text"`
}

// errorState is the liberally-parsed client error_info payload. last_rpc and
// errors are kept as raw values so unexpected shapes survive into the summary.
type errorState struct {
	Errors  []any          `json:"errors"`
	LastRPC map[string]any `json:"last_rpc"`
}

// decodeRRWebEvents scrubs the serialized rrweb event payload and returns the
// bytes to write as rrweb.json (nil when the input is empty or not JSON). The
// web client always sends raw JSON.stringify output; scrubbing is applied to the
// whole serialized string (the rrweb DOM text masking — maskAllText — happens
// client-side at record time; this is the second, credential-pattern layer).
func decodeRRWebEvents(raw string, opts harscrub.ScrubOptions) []byte {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !json.Valid([]byte(raw)) {
		return nil
	}
	return []byte(harscrub.ScrubString(raw, opts))
}

// decodeConsoleLogs scrubs the console_logs JSON and returns both the bytes to
// write (console.json) and the parsed entries for the body summary. A malformed
// payload yields (nil, nil).
func decodeConsoleLogs(raw string, opts harscrub.ScrubOptions) ([]byte, []consoleEntry) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	scrubbed := harscrub.ScrubString(raw, opts)
	var entries []consoleEntry
	if err := json.Unmarshal([]byte(scrubbed), &entries); err != nil {
		// Not the expected shape; still persist the scrubbed bytes if valid JSON.
		if json.Valid([]byte(scrubbed)) {
			return []byte(scrubbed), nil
		}
		return nil, nil
	}
	return []byte(scrubbed), entries
}

// decodeErrorInfo scrubs and liberally parses the error_info JSON. An
// unparseable payload yields a zero errorState (the body section then notes no
// structured error state).
func decodeErrorInfo(raw string, opts harscrub.ScrubOptions) errorState {
	var es errorState
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return es
	}
	scrubbed := harscrub.ScrubString(raw, opts)
	_ = json.Unmarshal([]byte(scrubbed), &es)
	return es
}

// errorStateSection renders the "## Error state" body block summarizing the
// captured client error count + last RPC. Empty when there is nothing to show.
func errorStateSection(es errorState) string {
	if len(es.Errors) == 0 && es.LastRPC == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n## Error state\n\n")
	fmt.Fprintf(&sb, "- Captured errors: %d\n", len(es.Errors))
	if es.LastRPC != nil {
		method, _ := es.LastRPC["method"].(string)
		msg, _ := es.LastRPC["message"].(string)
		fmt.Fprintf(&sb, "- Last RPC: %v code=%v message=%q\n",
			nonEmpty(method, "(unknown)"), es.LastRPC["code"], msg)
	}
	return sb.String()
}

// consoleSection renders the "## Console (recent)" body block listing the last
// few console entries. Empty when there are none.
func consoleSection(entries []consoleEntry) string {
	if len(entries) == 0 {
		return ""
	}
	const maxN = 10
	start := 0
	if len(entries) > maxN {
		start = len(entries) - maxN
	}
	var sb strings.Builder
	sb.WriteString("\n\n## Console (recent)\n\n")
	for _, e := range entries[start:] {
		fmt.Fprintf(&sb, "- [%s] %s\n", nonEmpty(e.Level, "log"), e.Text)
	}
	return sb.String()
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func privacyFollowUpSuffix(privacy bugprivacy.Result) string {
	if strings.TrimSpace(privacy.FollowUpPath) == "" {
		return ""
	}
	return "; depersonalized follow-up filed at " + privacy.FollowUpPath
}

func (s *Server) bugPrivacyCheckerForReport(params map[string]any) bugprivacy.Checker {
	if s.bugPrivacyCheckerResolver == nil {
		return s.bugPrivacyChecker
	}
	checker := s.bugPrivacyCheckerResolver(BugPrivacyContext{
		Params:    cloneParams(params),
		Selection: s.bugPrivacySelectionForReport(params),
	})
	if checker != nil {
		return checker
	}
	return s.bugPrivacyChecker
}

func (s *Server) bugPrivacySelectionForReport(params map[string]any) orchestrator.ProfileSelection {
	sessionID := strings.TrimSpace(stringParam(params, "session_id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(stringParam(params, "trace_ref"))
	}
	if sessionID == "" {
		return orchestrator.ProfileSelection{}
	}
	entry, rerr := s.resolve(map[string]any{"session_id": sessionID})
	if rerr != nil {
		return orchestrator.ProfileSelection{}
	}
	hc, ok := entry.Driver.(HarnessController)
	if !ok {
		return orchestrator.ProfileSelection{}
	}
	return hc.HarnessSelection()
}

func bugArtifactNames(files map[string][]byte) []string {
	out := make([]string, 0, len(files))
	for name, data := range files {
		if len(data) > 0 {
			out = append(out, name)
		}
	}
	return out
}

func cloneParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}

// stringSliceParam reads a []string param from a JSON array of strings.
func stringSliceParam(params map[string]any, key string) []string {
	raw, ok := params[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
