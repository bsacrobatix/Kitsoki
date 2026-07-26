// Package applicationbuild owns the generated Vite workspace and artifact
// lifecycle for story applications.
package applicationbuild

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"kitsoki/internal/app"
)

const (
	ManifestSchema      = "application-bundle/v1"
	CompatibilitySchema = "application-compatibility/v1"
)

type Loader func(string) (*app.AppDef, error)

type Command struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

type Runner interface {
	Run(context.Context, Command) error
	RunGroup(context.Context, []Command) error
}

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type OSRunner struct {
	Stdout io.Writer
	Stderr io.Writer
}

func (r OSRunner) Run(ctx context.Context, command Command) error {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = append(os.Environ(), command.Env...)
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", command.Name, err)
	}
	return nil
}

func (r OSRunner) RunGroup(ctx context.Context, commands []Command) error {
	if len(commands) == 0 {
		return errors.New("application build: command group is empty")
	}
	groupCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, len(commands))
	var wg sync.WaitGroup
	for _, command := range commands {
		command := command
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- r.Run(groupCtx, command)
		}()
	}
	first := <-errs
	cancel()
	wg.Wait()
	return first
}

type Config struct {
	RepoRoot       string
	TempRoot       string
	ArtifactRoot   string
	BackendURL     string
	VitePort       int
	BackendCommand []string
}

type ComponentModule struct {
	ID             string `json:"id"`
	Module         string `json:"module"`
	Export         string `json:"export"`
	ResolvedModule string `json:"-"`
}

type NativeSurface struct {
	Reuse      string   `json:"reuse,omitempty"`
	Projection string   `json:"projection,omitempty"`
	Commands   []string `json:"commands,omitempty"`
}

type Compatibility struct {
	Schema             string `json:"schema"`
	ShapeDigest        string `json:"shape_digest"`
	DefinitionDigest   string `json:"definition_digest"`
	RuntimeDigest      string `json:"runtime_digest"`
	PresentationDigest string `json:"presentation_digest"`
	Status             string `json:"status"`
	Message            string `json:"message"`
}

type Plan struct {
	ApplicationID    string                   `json:"application_id"`
	StoryPath        string                   `json:"story_path"`
	StoryRoot        string                   `json:"story_root"`
	Workspace        string                   `json:"workspace"`
	Entry            string                   `json:"entry"`
	OutDir           string                   `json:"out_dir"`
	BackendURL       string                   `json:"backend_url"`
	VitePort         int                      `json:"vite_port"`
	Components       []ComponentModule        `json:"components,omitempty"`
	Theme            map[string]string        `json:"theme,omitempty"`
	Native           map[string]NativeSurface `json:"native,omitempty"`
	Compatibility    Compatibility            `json:"compatibility"`
	CompatibilityOut string                   `json:"compatibility_out"`
}

type Manifest struct {
	Schema        string                   `json:"schema"`
	ApplicationID string                   `json:"application_id"`
	Digest        string                   `json:"digest"`
	CreatedAt     time.Time                `json:"created_at"`
	Entry         string                   `json:"entry"`
	Files         []string                 `json:"files"`
	Components    []ComponentModule        `json:"components,omitempty"`
	Theme         map[string]string        `json:"theme,omitempty"`
	Native        map[string]NativeSurface `json:"native,omitempty"`
	Compatibility Compatibility            `json:"compatibility"`
	ArtifactDir   string                   `json:"-"`
}

type Manager struct {
	config  Config
	load    Loader
	runner  Runner
	clock   Clock
	watcher SourceWatcher
}

func New(config Config, load Loader, runner Runner, clock Clock) (*Manager, error) {
	return NewWithWatcher(config, load, runner, clock, NewPollingWatcher(300*time.Millisecond))
}

func NewWithWatcher(config Config, load Loader, runner Runner, clock Clock, watcher SourceWatcher) (*Manager, error) {
	if load == nil {
		return nil, errors.New("application build: loader is required")
	}
	if runner == nil {
		return nil, errors.New("application build: runner is required")
	}
	if clock == nil {
		clock = realClock{}
	}
	repoRoot, err := filepath.Abs(config.RepoRoot)
	if err != nil || config.RepoRoot == "" {
		return nil, errors.New("application build: repository root is required")
	}
	config.RepoRoot = repoRoot
	if config.TempRoot == "" {
		config.TempRoot = filepath.Join(repoRoot, ".temp", "application")
	}
	if config.ArtifactRoot == "" {
		config.ArtifactRoot = filepath.Join(repoRoot, ".artifacts", "application-builds")
	}
	if config.BackendURL == "" {
		config.BackendURL = "http://127.0.0.1:7777"
	}
	if config.VitePort == 0 {
		config.VitePort = 5173
	}
	if watcher == nil {
		return nil, errors.New("application build: source watcher is required")
	}
	return &Manager{config: config, load: load, runner: runner, clock: clock, watcher: watcher}, nil
}

func (m *Manager) Prepare(storyPath string) (Plan, error) {
	absoluteStory, err := filepath.Abs(storyPath)
	if err != nil {
		return Plan{}, fmt.Errorf("application build: resolve story path: %w", err)
	}
	def, err := m.load(absoluteStory)
	if err != nil {
		return Plan{}, err
	}
	if def == nil || def.Application == nil {
		return Plan{}, fmt.Errorf("application build: %s has no application contract", storyPath)
	}
	storyRoot := filepath.Dir(absoluteStory)
	appID := def.App.ID
	if strings.TrimSpace(appID) == "" {
		return Plan{}, errors.New("application build: application id is required")
	}

	components, err := componentModules(def, storyRoot)
	if err != nil {
		return Plan{}, err
	}
	theme, err := applicationTheme(def, storyRoot)
	if err != nil {
		return Plan{}, err
	}

	native := make(map[string]NativeSurface)
	for _, surfaceName := range []string{"vscode", "tui"} {
		surface := def.Application.Surfaces[surfaceName]
		if surface == nil {
			continue
		}
		entry := NativeSurface{Reuse: surface.Reuse, Projection: surface.Projection}
		if surface.Native != nil {
			entry.Commands = append([]string(nil), surface.Native.Commands...)
		}
		native[surfaceName] = entry
	}

	compatibility, err := compatibilityForDefinition(def, storyRoot, components, theme)
	if err != nil {
		return Plan{}, err
	}
	planDigest, err := digestValue(struct {
		App          string
		Runtime      string
		Presentation string
	}{App: appID, Runtime: compatibility.RuntimeDigest, Presentation: compatibility.PresentationDigest})
	if err != nil {
		return Plan{}, err
	}
	workspace := filepath.Join(m.config.TempRoot, sanitizePathPart(appID), strings.TrimPrefix(planDigest, "sha256:")[:16])
	outDir := filepath.Join(workspace, "dist")
	plan := Plan{
		ApplicationID: appID, StoryPath: absoluteStory, StoryRoot: storyRoot,
		Workspace: workspace, OutDir: outDir, BackendURL: m.config.BackendURL,
		VitePort: m.config.VitePort, Components: components, Theme: theme, Native: native,
		Compatibility:    compatibility,
		CompatibilityOut: filepath.Join(workspace, "compatibility.json"),
	}
	customEntry := ""
	if surface := def.Application.Surfaces["web"]; surface != nil && surface.Presentation == "custom" && surface.Entry != "" {
		customEntry, err = resolveOwnedPath(storyRoot, surface.Entry)
		if err != nil {
			return Plan{}, fmt.Errorf("application build: web entry: %w", err)
		}
	}
	if err := m.writeWorkspace(&plan, customEntry); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (m *Manager) Build(ctx context.Context, storyPath string) (Manifest, error) {
	plan, err := m.Prepare(storyPath)
	if err != nil {
		return Manifest{}, err
	}
	command := m.viteCommand(plan, "build")
	if err := m.runner.Run(ctx, command); err != nil {
		return Manifest{}, fmt.Errorf("application build: vite build: %w", err)
	}
	outputDigest, files, err := digestDirectory(plan.OutDir)
	if err != nil {
		return Manifest{}, fmt.Errorf("application build: digest output: %w", err)
	}
	digest, err := bundleIdentityDigest(
		outputDigest,
		"index.html",
		plan.Components,
		plan.Theme,
		plan.Native,
		plan.Compatibility,
	)
	if err != nil {
		return Manifest{}, err
	}
	artifactDir := filepath.Join(m.config.ArtifactRoot, sanitizePathPart(plan.ApplicationID), strings.TrimPrefix(digest, "sha256:"))
	manifest := Manifest{
		Schema: ManifestSchema, ApplicationID: plan.ApplicationID, Digest: digest,
		CreatedAt: m.clock.Now().UTC(), Entry: "index.html", Files: files,
		Components: plan.Components, Theme: plan.Theme, Native: plan.Native, Compatibility: plan.Compatibility,
		ArtifactDir: artifactDir,
	}
	published, err := publishBundle(plan.OutDir, artifactDir, outputDigest, manifest)
	if err != nil {
		return Manifest{}, fmt.Errorf("application build: publish artifact: %w", err)
	}
	return published, nil
}

func (m *Manager) Dev(ctx context.Context, storyPath string) (Plan, error) {
	plan, err := m.Prepare(storyPath)
	if err != nil {
		return Plan{}, err
	}
	commands := make([]Command, 0, 2)
	if len(m.config.BackendCommand) > 0 {
		commands = append(commands, Command{
			Name: m.config.BackendCommand[0], Args: append([]string(nil), m.config.BackendCommand[1:]...),
			Dir: m.config.RepoRoot,
		})
	}
	commands = append(commands, m.viteCommand(plan, "dev"))

	devCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	processResult := make(chan error, 1)
	watchResult := make(chan error, 1)
	go func() { processResult <- m.runner.RunGroup(devCtx, commands) }()
	go func() {
		watchResult <- m.watcher.Watch(devCtx, plan.StoryRoot, func(string) error {
			next, err := m.Classify(storyPath, plan.Compatibility)
			if err != nil {
				next = plan.Compatibility
				next.Status = "invalid"
				next.Message = err.Error()
			}
			return writeJSON(plan.CompatibilityOut, next)
		})
	}()
	select {
	case err := <-processResult:
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			return plan, fmt.Errorf("application build: dev services: %w", err)
		}
	case err := <-watchResult:
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			return plan, fmt.Errorf("application build: watch sources: %w", err)
		}
	case <-ctx.Done():
		cancel()
		return plan, ctx.Err()
	}
	return plan, nil
}

func (m *Manager) viteCommand(plan Plan, mode string) Command {
	args := make([]string, 0, 5)
	if mode == "build" {
		args = append(args, "build")
	}
	args = append(args, "--config", "application.vite.config.ts", "--configLoader", "runner")
	return Command{
		Name: filepath.Join(m.config.RepoRoot, "tools", "runstatus", "node_modules", ".bin", "vite"),
		Args: args, Dir: filepath.Join(m.config.RepoRoot, "tools", "runstatus"),
		Env: []string{
			"KITSOKI_APPLICATION_PLAN=" + filepath.Join(plan.Workspace, "plan.json"),
			"KITSOKI_API=" + plan.BackendURL,
			fmt.Sprintf("VITE_PORT=%d", plan.VitePort),
		},
	}
}

func (m *Manager) Classify(storyPath string, previous Compatibility) (Compatibility, error) {
	absoluteStory, err := filepath.Abs(storyPath)
	if err != nil {
		return Compatibility{}, err
	}
	def, err := m.load(absoluteStory)
	if err != nil {
		return Compatibility{}, err
	}
	if def == nil || def.Application == nil {
		return Compatibility{}, errors.New("application build: application contract is required")
	}
	storyRoot := filepath.Dir(absoluteStory)
	components, err := componentModules(def, storyRoot)
	if err != nil {
		return Compatibility{}, err
	}
	theme, err := applicationTheme(def, storyRoot)
	if err != nil {
		return Compatibility{}, err
	}
	next, err := compatibilityForDefinition(def, storyRoot, components, theme)
	if err != nil {
		return Compatibility{}, err
	}
	switch {
	case next.ShapeDigest != previous.ShapeDigest:
		next.Status = "reload-required"
		next.Message = "Story topology, world, handlers, events, or schemas changed. Reload or fork the session explicitly."
	case next.DefinitionDigest != previous.DefinitionDigest:
		next.Status = "refresh-compatible"
		next.Message = "Story presentation or non-structural definition changed. Reload the definition and refresh the frame."
	case next.PresentationDigest != previous.PresentationDigest:
		next.Status = "compatible"
		next.Message = "Presentation modules changed and can be applied through Vite HMR."
	default:
		next.Status = "compatible"
		next.Message = "No application compatibility change detected."
	}
	return next, nil
}

func (m *Manager) writeWorkspace(plan *Plan, customEntry string) error {
	if err := os.MkdirAll(plan.Workspace, 0o755); err != nil {
		return fmt.Errorf("application build: create workspace: %w", err)
	}
	if err := writeJSON(filepath.Join(plan.Workspace, "plan.json"), plan); err != nil {
		return err
	}
	if err := writeJSON(plan.CompatibilityOut, plan.Compatibility); err != nil {
		return err
	}
	entry := customEntry
	if entry == "" {
		entry = filepath.Join(plan.Workspace, "entry.ts")
		if err := os.WriteFile(entry, []byte(generatedEntry(*plan, m.config.RepoRoot)), 0o644); err != nil {
			return fmt.Errorf("application build: write generated entry: %w", err)
		}
	}
	index := "<!doctype html>\n<html><head><meta charset=\"UTF-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>" +
		htmlEscape(plan.ApplicationID) + "</title></head><body><div id=\"app\"></div><script type=\"module\" src=\"" +
		vitePath(entry) + "\"></script></body></html>\n"
	if err := os.WriteFile(filepath.Join(plan.Workspace, "index.html"), []byte(index), 0o644); err != nil {
		return fmt.Errorf("application build: write index: %w", err)
	}
	plan.Entry = entry
	return writeJSON(filepath.Join(plan.Workspace, "plan.json"), plan)
}

func generatedEntry(plan Plan, repoRoot string) string {
	var out strings.Builder
	out.WriteString("import { createApp } from \"vue\";\n")
	out.WriteString("import ApplicationSurface from " + quoteTS(filepath.Join(repoRoot, "tools", "runstatus", "src", "surfaces", "ApplicationSurface.vue")) + ";\n")
	out.WriteString("import { installApplicationComponents } from " + quoteTS(filepath.Join(repoRoot, "tools", "runstatus", "src", "application", "component-loader.ts")) + ";\n")
	out.WriteString("import { installApplicationTheme } from " + quoteTS(filepath.Join(repoRoot, "tools", "runstatus", "src", "application", "theme.ts")) + ";\n")
	for index, component := range plan.Components {
		fmt.Fprintf(&out, "import * as component%d from %s;\n", index, quoteTS(component.ResolvedModule))
	}
	out.WriteString("installApplicationComponents({\n")
	for index, component := range plan.Components {
		fmt.Fprintf(&out, "  %q: component%d[%q],\n", component.ID, index, component.Export)
	}
	theme, _ := json.Marshal(plan.Theme)
	if len(plan.Theme) == 0 {
		theme = []byte("{}")
	}
	fmt.Fprintf(
		&out,
		"});\ninstallApplicationTheme(%s);\ncreateApp(ApplicationSurface, { applicationId: %q }).mount(\"#app\");\n",
		theme,
		plan.ApplicationID,
	)
	return out.String()
}

var applicationThemeKeys = map[string]struct{}{
	"accent": {}, "background": {}, "border": {}, "danger": {}, "foreground": {},
	"muted": {}, "paper": {}, "success": {}, "warning": {},
}

func applicationTheme(def *app.AppDef, storyRoot string) (map[string]string, error) {
	if def == nil || def.Application == nil || len(def.Application.Tokens) == 0 {
		return nil, nil
	}
	ownedRoots := append([]string{storyRoot}, def.ApplicationPackageRoots...)
	tokenIDs := make([]string, 0, len(def.Application.Tokens))
	for id := range def.Application.Tokens {
		tokenIDs = append(tokenIDs, id)
	}
	sort.Strings(tokenIDs)

	merged := make(map[string]string)
	for _, id := range tokenIDs {
		declared := def.Application.Tokens[id]
		path, err := resolveOwnedPathInRoots(ownedRoots, declared)
		if err != nil {
			return nil, fmt.Errorf("application build: token %q: %w", id, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("application build: token %q: read: %w", id, err)
		}
		if first := bytes.TrimSpace(raw); len(first) == 0 || first[0] != '{' {
			return nil, fmt.Errorf("application build: token %q must contain a JSON object", id)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("application build: token %q: parse JSON: %w", id, err)
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if _, ok := applicationThemeKeys[key]; !ok {
				return nil, fmt.Errorf("application build: token %q: unknown scoped theme key %q", id, key)
			}
			var value string
			if err := json.Unmarshal(values[key], &value); err != nil || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("application build: token %q: theme key %q must be a non-empty string", id, key)
			}
			merged[key] = value
		}
	}
	return merged, nil
}

func publishBundle(source, artifactDir, outputDigest string, manifest Manifest) (Manifest, error) {
	if existing, err := loadPublishedBundle(source, artifactDir, outputDigest, manifest); err == nil {
		return existing, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, err
	}

	appRoot := filepath.Dir(artifactDir)
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		return Manifest{}, err
	}
	stageRoot, err := os.MkdirTemp(appRoot, ".publish-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(stageRoot)
	stageDir := filepath.Join(stageRoot, "bundle")
	if err := copyDirectory(source, stageDir); err != nil {
		return Manifest{}, err
	}
	stagedDigest, stagedFiles, err := digestDirectory(stageDir)
	if err != nil {
		return Manifest{}, err
	}
	if stagedDigest != outputDigest || !reflect.DeepEqual(stagedFiles, manifest.Files) {
		return Manifest{}, errors.New("build output changed while staging content-addressed bundle")
	}
	if err := writeJSON(filepath.Join(stageDir, "application-manifest.json"), manifest); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(stageDir, artifactDir); err != nil {
		if existing, loadErr := loadPublishedBundle(source, artifactDir, outputDigest, manifest); loadErr == nil {
			return existing, nil
		}
		return Manifest{}, err
	}
	return manifest, nil
}

func loadPublishedBundle(source, artifactDir, outputDigest string, expected Manifest) (Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(artifactDir, "application-manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	var existing Manifest
	if err := json.Unmarshal(raw, &existing); err != nil {
		return Manifest{}, fmt.Errorf("existing manifest is invalid: %w", err)
	}
	existing.ArtifactDir = artifactDir
	expected.CreatedAt = existing.CreatedAt
	expected.ArtifactDir = artifactDir
	existingJSON, existingErr := json.Marshal(existing)
	expectedJSON, expectedErr := json.Marshal(expected)
	if existing.CreatedAt.IsZero() ||
		existingErr != nil ||
		expectedErr != nil ||
		!bytes.Equal(existingJSON, expectedJSON) {
		return Manifest{}, errors.New("existing content-addressed bundle metadata does not match its digest")
	}
	actualOutputDigest, actualFiles, err := digestDeclaredFiles(artifactDir, existing.Files)
	if err != nil {
		return Manifest{}, err
	}
	if actualOutputDigest != outputDigest || !reflect.DeepEqual(actualFiles, existing.Files) {
		return Manifest{}, errors.New("existing content-addressed bundle assets do not match its digest")
	}
	sourceDigest, sourceFiles, err := digestDirectory(source)
	if err != nil {
		return Manifest{}, err
	}
	if sourceDigest != outputDigest || !reflect.DeepEqual(sourceFiles, existing.Files) {
		return Manifest{}, errors.New("build output changed while publishing content-addressed bundle")
	}
	return existing, nil
}

func digestDeclaredFiles(root string, files []string) (string, []string, error) {
	hash := sha256.New()
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	for _, rel := range sorted {
		path, err := resolveOwnedPath(root, rel)
		if err != nil {
			return "", nil, err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", nil, fmt.Errorf("bundle asset %q is not a regular file", rel)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", nil, err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(rel))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(raw)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), sorted, nil
}

func resolveOwnedPath(root, relative string) (string, error) {
	return resolveOwnedPathInRoots([]string{root}, relative)
}

func resolveOwnedPathInRoots(roots []string, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" {
		return "", errors.New("module path is required")
	}
	candidate := relative
	if !filepath.IsAbs(candidate) {
		if len(roots) == 0 {
			return "", errors.New("no owned roots are configured")
		}
		candidate = filepath.Join(roots[0], filepath.FromSlash(candidate))
	}
	candidate, err := filepath.EvalSymlinks(filepath.Clean(candidate))
	if err != nil {
		return "", fmt.Errorf("read path %q: %w", relative, err)
	}
	owned := false
	for _, root := range roots {
		root, rootErr := filepath.EvalSymlinks(filepath.Clean(root))
		if rootErr != nil {
			continue
		}
		rel, relErr := filepath.Rel(root, candidate)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			owned = true
			break
		}
	}
	if !owned {
		return "", fmt.Errorf("path %q escapes story root and verified package roots", relative)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", fmt.Errorf("read path %q: %w", relative, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("path %q is a directory", relative)
	}
	return candidate, nil
}

func digestValue(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("application build: digest value: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func bundleIdentityDigest(
	outputDigest string,
	entry string,
	components []ComponentModule,
	theme map[string]string,
	native map[string]NativeSurface,
	compatibility Compatibility,
) (string, error) {
	return digestValue(struct {
		Output        string
		Entry         string
		Components    []ComponentModule
		Theme         map[string]string
		Native        map[string]NativeSurface
		Compatibility Compatibility
	}{
		Output: outputDigest, Entry: entry, Components: components,
		Theme: theme, Native: native, Compatibility: compatibility,
	})
}

func digestDirectory(root string) (string, []string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if len(files) == 0 {
		return "", nil, errors.New("vite produced no files")
	}
	sort.Strings(files)
	hash := sha256.New()
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", nil, err
		}
		_, _ = io.WriteString(hash, rel)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(raw)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), files, nil
}

func copyDirectory(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination %q already exists", destination)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("application build: write %s: %w", path, err)
	}
	return nil
}

func quoteTS(path string) string {
	raw, _ := json.Marshal(vitePath(path))
	return string(raw)
}

func vitePath(path string) string {
	return filepath.ToSlash(path)
}

func sanitizePathPart(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, value)
	return strings.Trim(value, "-.")
}

func htmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return replacer.Replace(value)
}
