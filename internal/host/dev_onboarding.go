package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/projectprofile"
)

// DevOnboardingHandler owns the deterministic parts of dev-story onboarding.
// The story invokes it through init_onboarding.star; discovery and apply are
// deliberately one host boundary so no project setup phase needs an embedded
// Python interpreter or a shell-generated script.
func DevOnboardingHandler(ctx context.Context, args map[string]any) (Result, error) {
	switch stringValue(args, "op") {
	case "discover":
		return Result{Data: discoverDevOnboarding(args)}, nil
	case "apply":
		data := onboardingMap(args["data"])
		result, err := applyDevOnboarding(data, stringValue(args, "profile_json"))
		if err != nil {
			return Result{Data: map[string]any{
				"status":             "apply-failed",
				"target_path":        stringValue(data, "target_path"),
				"profile_validation": map[string]any{"ok": false, "schema": []any{err.Error()}, "semantic": []any{}, "warnings": []any{}},
				"error":              err.Error(),
			}}, nil
		}
		return Result{Data: result}, nil
	case "customizations":
		return Result{Data: onboardingCustomizations(args)}, nil
	case "readiness":
		return Result{Data: onboardingReadiness(ctx, args)}, nil
	default:
		return Result{Error: fmt.Sprintf("host.dev.onboarding: unknown op %q", stringValue(args, "op"))}, nil
	}
}

func onboardingReadiness(ctx context.Context, args map[string]any) map[string]any {
	data := onboardingMap(args["data"])
	root := stringValue(data, "target_path")
	result := map[string]any{
		"schema": "kitsoki-readiness/v1",
		"status": "pass",
		"ok":     true,
		"out":    ".artifacts/kitsoki-readiness.json",
		"checks": []any{},
	}
	if root == "" {
		return onboardingReadinessError(result, "target_path is required")
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return onboardingReadinessError(result, "target directory does not exist: "+root)
	}
	profilePath := filepath.Join(root, ".kitsoki", "project-profile.yaml")
	profileRaw, err := os.ReadFile(profilePath)
	if err != nil {
		return onboardingReadinessError(result, "project profile was not found: "+profilePath)
	}
	profile := map[string]any{}
	if err := yaml.Unmarshal(profileRaw, &profile); err != nil {
		return onboardingReadinessError(result, "project profile is invalid: "+err.Error())
	}
	checks := []any{}
	projectID := stringValue(data, "project_id")
	if projectID == "" {
		projectID = stringValue(profile, "id")
	}
	instance := filepath.Join(root, ".kitsoki", "stories", onboardingSlug(projectID)+"-dev", "app.yaml")
	checks = append(checks, onboardingStoryCheck(instance))
	commands := onboardingMap(profile["commands"])
	for _, check := range []struct {
		id      string
		command string
	}{
		{id: "tests", command: stringValue(commands, "test")},
		{id: "build", command: stringValue(commands, "build")},
	} {
		if strings.TrimSpace(check.command) == "" {
			continue
		}
		checks = append(checks, onboardingCommandCheck(ctx, root, check.id, check.command))
	}
	status := "pass"
	for _, raw := range checks {
		if stringValue(onboardingMap(raw), "status") == "fail" {
			status = "fail"
			break
		}
	}
	result["checks"] = checks
	result["status"] = status
	result["ok"] = status == "pass"
	reportPath := filepath.Join(root, ".artifacts", "kitsoki-readiness.json")
	reportBytes, _ := json.MarshalIndent(result, "", "  ")
	if _, err := writeOnboardingFile(reportPath, string(reportBytes)+"\n"); err != nil {
		result["status"] = "error"
		result["ok"] = false
		result["error"] = err.Error()
		return result
	}
	profile["readiness"] = map[string]any{"status": status, "report": ".artifacts/kitsoki-readiness.json"}
	if updated, err := yaml.Marshal(profile); err == nil {
		if err := os.WriteFile(profilePath, updated, 0o644); err != nil {
			result["status"] = "error"
			result["ok"] = false
			result["error"] = err.Error()
		}
	}
	return result
}

func onboardingReadinessError(result map[string]any, detail string) map[string]any {
	result["status"] = "error"
	result["ok"] = false
	result["error"] = detail
	result["checks"] = []any{map[string]any{"id": "readiness", "status": "fail", "detail": detail}}
	return result
}

func onboardingStoryCheck(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{"id": "story-load", "status": "fail", "detail": err.Error()}
	}
	var document any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return map[string]any{"id": "story-load", "status": "fail", "detail": err.Error()}
	}
	return map[string]any{"id": "story-load", "status": "pass", "detail": "validated " + path}
}

func onboardingCommandCheck(ctx context.Context, root, id, command string) map[string]any {
	check := map[string]any{"id": id, "status": "pass", "detail": command}
	commandCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "sh", "-c", command)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		check["status"] = "fail"
		check["detail"] = strings.TrimSpace(string(out))
		if check["detail"] == "" {
			check["detail"] = err.Error()
		}
	}
	return check
}

func onboardingCustomizations(args map[string]any) map[string]any {
	data := onboardingMap(args["data"])
	root := stringValue(data, "target_path")
	action := defaultString(stringValue(data, "action"), "promote")
	profilePath := filepath.Join(root, ".kitsoki", "project-profile.yaml")
	profile := map[string]any{}
	if raw, err := os.ReadFile(profilePath); err == nil {
		_ = yaml.Unmarshal(raw, &profile)
	}
	entries := onboardingCustomizationEntries(profile, root)
	changed := []any{}
	if action == "accept" || action == "refine" {
		status := "accepted"
		if action == "refine" {
			status = "needs-refinement"
		}
		for _, raw := range entries {
			entry := onboardingMap(raw)
			if stringValue(entry, "status") == "pending" {
				entry["status"] = status
				if feedback := stringValue(data, "feedback"); feedback != "" {
					entry["feedback"] = feedback
				}
				changed = append(changed, entry)
			}
		}
		if len(changed) > 0 {
			profile["onboarding"] = onboardingMap(profile["onboarding"])
			profile["onboarding"].(map[string]any)["story_customizations"] = entries
			if raw, err := yaml.Marshal(profile); err == nil {
				_ = os.WriteFile(profilePath, raw, 0o644)
			}
		}
	}
	counts := map[string]any{"pending": 0, "accepted": 0, "needs_refinement": 0}
	for _, raw := range entries {
		switch stringValue(onboardingMap(raw), "status") {
		case "pending":
			counts["pending"] = counts["pending"].(int) + 1
		case "accepted":
			counts["accepted"] = counts["accepted"].(int) + 1
		case "needs-refinement", "needs_refinement":
			counts["needs_refinement"] = counts["needs_refinement"].(int) + 1
		}
	}
	return map[string]any{"action": action, "updated": len(changed) > 0, "counts": counts, "entries": entries, "changed": changed, "ok": true}
}

func onboardingCustomizationEntries(profile map[string]any, root string) []any {
	onboarding := onboardingMap(profile["onboarding"])
	if configured, ok := onboarding["story_customizations"].([]any); ok {
		return configured
	}
	entries := []any{}
	jobsRoot := filepath.Join(root, ".artifacts", "mining", "jobs")
	_ = filepath.WalkDir(jobsRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || entry.Name() != "analysis.json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var document map[string]any
		if json.Unmarshal(raw, &document) != nil {
			return nil
		}
		for _, key := range []string{"actions", "recipes", "proposals"} {
			items, ok := document[key].([]any)
			if !ok {
				continue
			}
			for index, item := range items {
				candidate := onboardingMap(item)
				id := stringValue(candidate, "id")
				if id == "" {
					id = fmt.Sprintf("mined-%d", index+1)
				}
				summary := stringValue(candidate, "summary")
				if summary == "" {
					summary = stringValue(candidate, "title")
				}
				entries = append(entries, map[string]any{"id": id, "status": defaultString(stringValue(candidate, "status"), "pending"), "summary": summary, "evidence": filepath.ToSlash(path)})
			}
		}
		return nil
	})
	return entries
}

func onboardingMap(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func discoverDevOnboarding(args map[string]any) map[string]any {
	target := onboardingTarget(stringValue(args, "request"), stringValue(args, "workdir"), stringValue(args, "repo_root"))
	pack := map[string]any{
		"id": "core-engineering", "title": "Core engineering starter",
		"summary": "Onboard the checkout and use the core engineering workflow.",
		"stories": []any{"setup", "bugfix", "design", "implementation", "review"},
	}
	base := map[string]any{
		"target_path": target, "project_id": "", "project_title": "", "stack": "",
		"dev_command": "", "test_command": "", "build_command": "", "conventions": "local defaults",
		"repo_vcs": "none", "repo_default_branch": "", "repo_remote": "", "tracker": "none", "ticket_repo": "",
		"starter_stories": []any{pack["stories"]}, "story_pack": pack["id"], "story_pack_title": pack["title"],
		"story_pack_summary": pack["summary"], "story_packs": []any{pack}, "transcript_count": 0,
		"transcript_sources": []any{}, "mining_recommendation": map[string]any{
			"status": "no-transcripts-found", "sample": "recency", "first_pass_sample": 0,
			"transcript_count": 0, "sources": []any{},
			"note": "No associated Claude/Codex transcripts were found during deterministic discovery.",
		},
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		base["error"] = "target path does not exist or is not a directory"
		return base
	}
	projectID, projectTitle := discoverProjectIdentity(target)
	if projectID == "" {
		projectID = onboardingSlug(filepath.Base(target))
	}
	if projectTitle == "" {
		projectTitle = onboardingTitle(projectID)
	}
	stack, dev, test, build := discoverProjectCommands(target, projectID)
	meta := findOnboardingMetadata(target, projectID)
	if meta != nil {
		if value := meta["title"]; value != "" {
			projectTitle = value
		}
		if value := meta["test_command"]; value != "" {
			test = value
		}
		if value := meta["build_command"]; value != "" {
			build = value
		}
		if value := meta["start_command"]; value != "" {
			dev = value
		}
	}
	vcs, branch, remote := onboardingGitInfo(target)
	base["project_id"], base["project_title"], base["stack"] = projectID, projectTitle, stack
	base["dev_command"], base["test_command"], base["build_command"] = dev, test, build
	base["conventions"] = "local defaults"
	if meta != nil {
		base["conventions"] = "project"
	}
	base["repo_vcs"], base["repo_default_branch"], base["repo_remote"] = vcs, branch, remote
	if repo := onboardingGitHubRepo(remote); repo != "" {
		base["tracker"], base["ticket_repo"] = "github", repo
	}
	return base
}

func onboardingTarget(request, workdir, repoRoot string) string {
	request = strings.TrimSpace(request)
	if strings.HasPrefix(request, "onboard ") {
		request = strings.TrimSpace(strings.TrimPrefix(request, "onboard "))
	}
	if request == "" {
		request = repoRoot
		if request == "" {
			request = workdir
		}
	}
	if request == "" {
		request = "."
	}
	if strings.HasPrefix(request, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			request = filepath.Join(home, strings.TrimPrefix(request, "~/"))
		}
	}
	if !filepath.IsAbs(request) {
		base := workdir
		if base == "" {
			base = repoRoot
		}
		if base == "" {
			base, _ = os.Getwd()
		}
		request = filepath.Join(base, request)
	}
	abs, err := filepath.Abs(request)
	if err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(request)
}

func onboardingSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "project"
	}
	return value
}
func onboardingTitle(value string) string {
	parts := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(value))
	for i, part := range parts {
		if part != "" {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func discoverProjectIdentity(root string) (string, string) {
	if raw, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var data map[string]any
		if json.Unmarshal(raw, &data) == nil {
			name, _ := data["name"].(string)
			if name != "" {
				return onboardingSlug(name), onboardingTitle(name)
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
		if match := regexp.MustCompile(`(?m)^name\s*=\s*[\"']([^\"']+)[\"']`).FindStringSubmatch(string(raw)); len(match) == 2 {
			return onboardingSlug(match[1]), onboardingTitle(match[1])
		}
	}
	return onboardingSlug(filepath.Base(root)), onboardingTitle(filepath.Base(root))
}

func discoverProjectCommands(root, projectID string) (stack, dev, test, build string) {
	packageData := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		_ = json.Unmarshal(raw, &packageData)
	}
	if len(packageData) > 0 {
		stack = "node project"
		scripts := onboardingMap(packageData["scripts"])
		manager := onboardingNodeManager(root)
		dev = onboardingScript(scripts, "dev", manager)
		test = onboardingScript(scripts, "test", manager)
		build = onboardingScript(scripts, "build", manager)
		return
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		stack, build, test = "go project", "go build ./...", "go test ./..."
	}
	if _, err := os.Stat(filepath.Join(root, "Cargo.toml")); err == nil {
		stack, build, test = "rust project", "cargo build", "cargo test"
	}
	if _, err := os.Stat(filepath.Join(root, "pyproject.toml")); err == nil {
		stack, test = "python project", "python -m pytest"
	}
	if _, err := os.Stat(filepath.Join(root, "requirements.txt")); err == nil && stack == "" {
		stack, test = "python project", "python -m pytest"
	}
	if stack == "" {
		stack = "local project"
	}
	targets := onboardingMakeTargets(root)
	if targets["build"] {
		build = "make build"
	}
	if targets["test"] {
		test = "make test"
	}
	if targets["dev"] {
		dev = "make dev"
	}
	if targets["run"] && dev == "" {
		dev = "make run"
	}
	return
}

func onboardingNodeManager(root string) string {
	for _, item := range []struct {
		name  string
		files []string
	}{{"pnpm", []string{"pnpm-lock.yaml"}}, {"yarn", []string{"yarn.lock"}}, {"bun", []string{"bun.lock", "bun.lockb"}}} {
		for _, file := range item.files {
			if _, err := os.Stat(filepath.Join(root, file)); err == nil {
				return item.name
			}
		}
	}
	return "npm"
}
func onboardingScript(scripts map[string]any, name, manager string) string {
	value, _ := scripts[name].(string)
	if value == "" {
		return ""
	}
	return manager + " run " + name
}
func onboardingMakeTargets(root string) map[string]bool {
	out := map[string]bool{}
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return out
	}
	re := regexp.MustCompile(`(?m)^([A-Za-z][A-Za-z0-9_-]*):`)
	for _, match := range re.FindAllStringSubmatch(string(raw), -1) {
		out[match[1]] = true
	}
	return out
}

func findOnboardingMetadata(target, projectID string) map[string]string {
	for current := target; current != filepath.Dir(current); current = filepath.Dir(current) {
		path := filepath.Join(current, "projects", projectID, "project.toml")
		if raw, err := os.ReadFile(path); err == nil {
			text := string(raw)
			out := map[string]string{}
			for key, pattern := range map[string]string{"title": `(?m)^title\s*=\s*[\"']([^\"']+)[\"']`, "test_command": `(?m)^test_command\s*=\s*[\"']([^\"']+)[\"']`, "build_command": `(?m)^build\s*=\s*[\"']([^\"']+)[\"']`, "start_command": `(?m)^start\s*=\s*[\"']([^\"']+)[\"']`} {
				if match := regexp.MustCompile(pattern).FindStringSubmatch(text); len(match) == 2 {
					out[key] = match[1]
				}
			}
			return out
		}
	}
	return nil
}

func onboardingGitInfo(root string) (string, string, string) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "none", "", ""
	}
	branch := onboardingGit(root, "symbolic-ref", "--short", "HEAD")
	if branch == "" {
		branch = "main"
	}
	return "git", branch, onboardingGit(root, "remote", "get-url", "origin")
}
func onboardingGit(root string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
func onboardingGitHubRepo(remote string) string {
	match := regexp.MustCompile(`(?i)(?:github\.com[:/])([^/ :]+/[^/ ]+?)(?:\.git)?$`).FindStringSubmatch(strings.TrimSpace(remote))
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func applyDevOnboarding(data map[string]any, profileJSON string) (map[string]any, error) {
	root := stringValue(data, "target_path")
	if root == "" {
		return nil, fmt.Errorf("target_path is required")
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("target directory does not exist: %s", root)
	}
	projectID := onboardingSlug(stringValue(data, "project_id"))
	if projectID == "" {
		projectID = "project"
	}
	data["project_id"] = projectID
	if stringValue(data, "project_title") == "" {
		data["project_title"] = onboardingTitle(projectID)
	}
	if profileJSON != "" {
		var profile map[string]any
		if err := json.Unmarshal([]byte(profileJSON), &profile); err != nil {
			return nil, fmt.Errorf("profile_json: %w", err)
		}
		data["profile"] = profile
	}
	profile := onboardingProfileDocument(data)
	profileBytes, err := yaml.Marshal(profile)
	if err != nil {
		return nil, fmt.Errorf("marshal project profile: %w", err)
	}
	validation, err := projectprofile.Validate(profileBytes, root)
	if err != nil {
		return nil, fmt.Errorf("validate project profile: %w", err)
	}
	validationData := map[string]any{"ok": validation.OK, "schema": stringsToAny(validation.Schema), "semantic": stringsToAny(validation.Semantic), "warnings": stringsToAny(validation.Warnings)}
	if !validation.OK {
		return map[string]any{"status": "profile-validation-failed", "target_path": root, "profile_validation": validationData}, nil
	}
	instanceRel := filepath.Join(".kitsoki", "stories", projectID+"-dev", "app.yaml")
	ciEnvRel := filepath.Join(".kitsoki", "environments", "ci.yaml")
	ciConfigRel := filepath.Join(".kitsoki", "ci.yaml")
	ciStoryRel := filepath.Join(".kitsoki", "stories", "capsule-ci", "app.yaml")
	developmentCapsuleRel := filepath.Join(".kitsoki", "capsules", "development.yaml")
	files := map[string]string{
		filepath.Join(root, ".gitignore"):                                         onboardingGitignore(root),
		filepath.Join(root, ".kitsoki.yaml"):                                      onboardingConfigYAML(),
		filepath.Join(root, developmentCapsuleRel):                                onboardingDevelopmentCapsuleYAML(),
		filepath.Join(root, ciEnvRel):                                             onboardingCapsuleEnvironmentYAML(root),
		filepath.Join(root, ciConfigRel):                                          onboardingCapsuleCIYAML(),
		filepath.Join(root, ciStoryRel):                                           onboardingCapsuleCIStoryYAML(),
		filepath.Join(root, ".kitsoki", "project-profile.yaml"):                   string(profileBytes),
		filepath.Join(root, instanceRel):                                          onboardingInstanceYAML(data),
		filepath.Join(root, ".kitsoki", "stories", projectID+"-dev", "README.md"): "# " + projectID + " dev-story\n\nGenerated by Kitsoki project onboarding.\n",
	}
	writes := []any{}
	for path, content := range files {
		changed, err := writeOnboardingFile(path, content)
		if err != nil {
			return nil, err
		}
		if changed {
			writes = append(writes, path)
		}
	}
	return map[string]any{"status": "applied", "target_path": root, "config_path": filepath.Join(root, ".kitsoki.yaml"), "development_capsule_path": filepath.Join(root, developmentCapsuleRel), "ci_environment_path": filepath.Join(root, ciEnvRel), "ci_config_path": filepath.Join(root, ciConfigRel), "ci_story_path": filepath.Join(root, ciStoryRel), "profile_path": filepath.Join(root, ".kitsoki", "project-profile.yaml"), "instance_path": filepath.Join(root, instanceRel), "gitignore_path": filepath.Join(root, ".gitignore"), "profile_validation": validationData, "writes": writes}, nil
}

func stringsToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
func writeOnboardingFile(path, content string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	old, err := os.ReadFile(path)
	if err == nil && string(old) == content {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func onboardingGitignore(root string) string {
	existing := ""
	path := filepath.Join(root, ".gitignore")
	if raw, err := os.ReadFile(path); err == nil {
		existing = string(raw)
	}
	out := strings.TrimRight(existing, "\n")
	additions := []string{".kitsoki.local.yaml", ".kitsoki/sessions/", ".capsules/", ".artifacts/", ".context/", ".worktrees/"}
	for _, item := range additions {
		if gitignoreContains(existing, item) {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += item
	}
	return out + "\n"
}

func gitignoreContains(raw, entry string) bool {
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == entry {
			return true
		}
	}
	return false
}

func onboardingCapsuleEnvironmentYAML(root string) string {
	lockfiles := []string{}
	for _, rel := range []string{"go.sum", "Cargo.lock", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "uv.lock", "poetry.lock", "requirements.txt"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			lockfiles = append(lockfiles, rel)
		}
	}
	var b strings.Builder
	b.WriteString("schema: capsule-environment/v1\nid: ci\nsource:\n  host_probe: true\n")
	if len(lockfiles) > 0 {
		b.WriteString("lockfiles:\n")
		for _, rel := range lockfiles {
			fmt.Fprintf(&b, "  - %s\n", rel)
		}
	}
	b.WriteString("network: none\ncaches:\n  - id: project-build\n    scope: project\n    mode: read_write\nsandbox: supervised\n")
	return b.String()
}

func onboardingDevelopmentCapsuleYAML() string {
	return "schema: capsule-definition/v1\nid: development\ndescription: Project development workspace managed by Kitsoki Capsule.\nsource:\n  kind: self\npolicy:\n  network: none\n"
}

func onboardingCapsuleCIYAML() string {
	return "schema: capsule-ci/v1\nproject_profile: .kitsoki/project-profile.yaml\ndefault_environment: ci\n\npipelines:\n  change:\n    story: .kitsoki/stories/capsule-ci/app.yaml\n    triggers: [local]\n    environment: ci\n    executor: host\n    mode: one-shot\n    required: true\n    permissions:\n      network: none\n      external_write: deny\n    agents:\n      policy: deny\n    cleanup:\n      keep_runs: 20\n      require_hygiene_check: true\n    result:\n      schema: capsule-ci-verdict/v1\n      pass_exits: [passed]\n      fail_exits: [failed]\n      park_exits: [needs_input]\n"
}

func onboardingCapsuleCIStoryYAML() string {
	return `app:
  id: capsule-ci
  version: 0.1.0
  title: "Project Capsule CI"
  author: "Kitsoki"
  license: "CC0"

world:
  ci_job_id: { type: string, default: "" }
  ci_pipeline: { type: string, default: "" }
  ci_trigger: { type: object, default: {} }
  ci_source: { type: object, default: {} }
  ci_workspace: { type: object, default: {} }
  ci_environment: { type: object, default: {} }
  ci_policy: { type: object, default: {} }
  ci_verdict: { type: object, default: {} }
  ci_outcome: { type: string, default: "" }

intents:
  run:
    description: "Validate the supplied Capsule CI envelope."
    examples: [run, start]
    priority: 90
  look:
    description: "Re-render the current CI phase."
    examples: [look, status]
    priority: 5

root: idle

states:
  idle:
    relevant_world: [ci_pipeline, ci_job_id]
    view:
      - heading: "Capsule CI"
      - prose: "This generated CI story parks until the project composes deterministic checks."
    on:
      run:
        - target: parked
          effects:
            - set:
                ci_outcome: "needs_input"
                ci_verdict:
                  schema: capsule-ci-verdict/v1
                  pipeline: "{{ world.ci_pipeline }}"
                  outcome: needs_input
                  summary: "Generated Capsule CI needs project-specific checks before it can pass."
                  checks: []
                  promotion_eligible: false
                  source_digest: "{{ world.ci_source.digest }}"
                  story_digest: "{{ world.ci_trigger.story_digest }}"
                  environment_digest: "{{ world.ci_environment.digest }}"
                  envelope_digest: "{{ world.ci_trigger.envelope_digest }}"
      look:
        - target: .
  parked:
    view:
      - heading: "Needs input"
      - prose: "Add deterministic checks or review rooms, then rerun Capsule CI."
`
}

func onboardingProfileDocument(data map[string]any) map[string]any {
	kind := "generic"
	stack := strings.ToLower(stringValue(data, "stack"))
	languages := []any{}
	switch {
	case strings.Contains(stack, "go"):
		kind, languages = "go", []any{"go"}
	case strings.Contains(stack, "rust"):
		kind, languages = "rust", []any{"rust"}
	case strings.Contains(stack, "node"):
		kind, languages = "node", []any{"javascript"}
	case strings.Contains(stack, "python"):
		kind, languages = "python", []any{"python"}
	}
	return map[string]any{
		"schema": "project-profile/v1", "id": stringValue(data, "project_id"), "title": stringValue(data, "project_title"),
		"summary":     "Project-local Kitsoki dev-story binding.",
		"commands":    map[string]any{"dev": stringValue(data, "dev_command"), "test": stringValue(data, "test_command"), "build": stringValue(data, "build_command")},
		"repo":        map[string]any{"root": ".", "vcs": defaultString(stringValue(data, "repo_vcs"), "none"), "default_branch": stringValue(data, "repo_default_branch"), "remote": stringValue(data, "repo_remote")},
		"stack":       map[string]any{"kind": kind, "languages": languages},
		"testing":     map[string]any{"mechanisms": []any{map[string]any{"kind": "unit", "runner": "command", "command": stringValue(data, "test_command")}, map[string]any{"kind": "build", "runner": "command", "command": stringValue(data, "build_command")}}},
		"conventions": map[string]any{"source": "project"},
		"kitsoki":     map[string]any{"story": "dev-story", "story_pack": "core-engineering", "enabled_stories": []any{"setup", "bugfix", "design", "implementation", "review"}, "instance": map[string]any{"id": stringValue(data, "project_id") + "-dev", "path": ".kitsoki/stories/" + stringValue(data, "project_id") + "-dev/app.yaml", "bindings": map[string]any{"ticket": "host.local_files.ticket", "vcs": "host.git", "ci": "host.local", "workspace": "host.capsule_workspace", "transport": "host.append_to_file"}}},
		"onboarding":  map[string]any{"base_story": "dev-story", "story_pack": "core-engineering", "recording_policy": "no-llm-only"},
		"setup_plan":  map[string]any{"writes": []any{}, "verifications": []any{}}, "readiness": map[string]any{"status": "not-run"},
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
func onboardingConfigYAML() string {
	return "story_dirs:\n  - ./.kitsoki/stories\n\nproject_profile: .kitsoki/project-profile.yaml\n\nroot:\n  import: dev-story\n"
}

var onboardingInstanceHosts = []string{
	"host.local_files.ticket",
	"host.local_github.ticket",
	"host.gh.ticket",
	"host.gh.ticket.get",
	"host.gh.ticket.comment",
	"host.gh.ticket.transition",
	"host.git",
	"host.git_worktree",
	"host.local",
	"host.capsule_workspace",
	"host.append_to_file",
	"host.inbox.add",
	"host.agent.ask",
	"host.agent.decide",
	"host.agent.task",
	"host.agent.codeact",
	"host.agent.search",
	"host.agent.converse",
	"host.chat.resolve",
	"host.chat.transcript",
	"host.artifacts_dir",
	"host.slidey.render",
	"host.fs.writable_dir",
	"host.ide.get_diagnostics",
	"host.ide.open_file",
	"host.ide.open_diff",
	"host.diff.open",
	"host.run",
	"host.starlark.run",
	"host.session_mining.run",
	"host.ui_qa.run",
	"host.proposal.publish",
	"host.dev.profile_setup",
	"host.dev.onboarding",
	"host.decomposition.update",
}

func onboardingInstanceYAML(data map[string]any) string {
	id := stringValue(data, "project_id")
	title := stringValue(data, "project_title")
	var b strings.Builder
	fmt.Fprintf(&b, "app:\n  id: %s-dev\n  version: 0.1.0\n  title: %q\n  author: Kitsoki\n  license: CC0\n\nhosts:\n", id, id+"-dev - work on "+title+" with Kitsoki")
	for _, host := range onboardingInstanceHosts {
		fmt.Fprintf(&b, "  - %s\n", host)
	}
	b.WriteString("\nimports:\n  core:\n    source: \"@kitsoki/dev-story\"\n    entry: landing\n    hosts: inherit\n    host_bindings:\n      ticket: host.local_files.ticket\n      vcs: host.git\n      ci: host.local\n      workspace: host.capsule_workspace\n      transport: host.append_to_file\n    world_in:\n      workdir: \"{{ world.workdir }}\"\n      repo_root: \"{{ world.repo_root }}\"\n      build_cmd: \"{{ world.build_cmd }}\"\n      test_cmd: \"{{ world.test_cmd }}\"\n\nworld:\n  workdir: { type: string, default: \".\" }\n  repo_root: { type: string, default: \".\" }\n  build_cmd: { type: string, default: \"\" }\n  test_cmd: { type: string, default: \"\" }\n\nroot: core\n")
	return b.String()
}
