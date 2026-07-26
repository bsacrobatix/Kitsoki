package applicationbuild

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/app"
)

type fakeRunner struct {
	commands []Command
}

func (r *fakeRunner) Run(_ context.Context, command Command) error {
	r.commands = append(r.commands, command)
	var planPath string
	for _, env := range command.Env {
		if strings.HasPrefix(env, "KITSOKI_APPLICATION_PLAN=") {
			planPath = strings.TrimPrefix(env, "KITSOKI_APPLICATION_PLAN=")
		}
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	var plan Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return err
	}
	if err := os.MkdirAll(plan.OutDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(plan.OutDir, "index.html"), []byte("built"), 0o644)
}

func (r *fakeRunner) RunGroup(_ context.Context, commands []Command) error {
	r.commands = append(r.commands, commands...)
	return nil
}

type fixedClock struct{}

func (fixedClock) Now() time.Time {
	return time.Date(2026, 7, 26, 4, 0, 0, 0, time.UTC)
}

type stepClock struct {
	next time.Time
}

func (c *stepClock) Now() time.Time {
	current := c.next
	c.next = c.next.Add(time.Minute)
	return current
}

func TestBuildCreatesContentAddressedManifestAndGeneratedComponentEntry(t *testing.T) {
	root := t.TempDir()
	storyDir := filepath.Join(root, "stories", "demo")
	if err := os.MkdirAll(filepath.Join(storyDir, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	storyPath := filepath.Join(storyDir, "app.yaml")
	if err := os.WriteFile(storyPath, []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storyDir, "ui", "List.vue"), []byte("<template/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	manager, err := New(Config{RepoRoot: root}, func(string) (*app.AppDef, error) {
		return applicationDefinition("ui/List.vue"), nil
	}, runner, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.Build(context.Background(), storyPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != ManifestSchema || !strings.HasPrefix(manifest.Digest, "sha256:") {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.CreatedAt != (fixedClock{}).Now() {
		t.Fatalf("created at = %s", manifest.CreatedAt)
	}
	if _, err := os.Stat(filepath.Join(manifest.ArtifactDir, "application-manifest.json")); err != nil {
		t.Fatal(err)
	}
	rawManifest, err := os.ReadFile(filepath.Join(manifest.ArtifactDir, "application-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawManifest), root) ||
		strings.Contains(string(rawManifest), filepath.ToSlash(root)) ||
		strings.Contains(string(rawManifest), "artifact_dir") {
		t.Fatalf("bundle manifest leaked local build paths: %s", rawManifest)
	}
	entry, err := os.ReadFile(filepath.Join(filepath.Dir(runnerPlanPath(t, runner.commands[0])), "entry.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entry), `installApplicationComponents`) || !strings.Contains(string(entry), `"demo.list"`) {
		t.Fatalf("generated entry:\n%s", entry)
	}
	if !strings.Contains(string(entry), `installApplicationTheme({})`) {
		t.Fatalf("generated entry does not install its scoped theme:\n%s", entry)
	}
	if !strings.Contains(string(entry), `applicationId: "demo"`) {
		t.Fatalf("generated entry does not bootstrap its application session:\n%s", entry)
	}
}

func TestPrepareLoadsAndDeterministicallyMergesApplicationThemeTokens(t *testing.T) {
	root := t.TempDir()
	storyDir := filepath.Join(root, "story")
	if err := os.MkdirAll(filepath.Join(storyDir, "tokens"), 0o755); err != nil {
		t.Fatal(err)
	}
	storyPath := filepath.Join(storyDir, "app.yaml")
	if err := os.WriteFile(storyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(storyDir, "tokens", "base.json"),
		[]byte(`{"accent":"#06c","background":"#fff","foreground":"#111"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(storyDir, "tokens", "override.json"),
		[]byte(`{"accent":"#176b5b","paper":"#f8f8f8"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	def := applicationDefinition("")
	delete(def.Application.Components, "demo.list")
	// Map iteration order must not affect composition. Sorted token IDs make
	// the lexically later override layer win.
	def.Application.Tokens = map[string]string{
		"demo.override": "tokens/override.json",
		"demo.base":     "tokens/base.json",
	}
	manager, err := New(Config{RepoRoot: root}, func(string) (*app.AppDef, error) {
		return def, nil
	}, &fakeRunner{}, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := manager.Prepare(storyPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"accent": "#176b5b", "background": "#fff", "foreground": "#111", "paper": "#f8f8f8",
	}
	if !reflect.DeepEqual(plan.Theme, want) {
		t.Fatalf("theme = %#v, want %#v", plan.Theme, want)
	}
	entry, err := os.ReadFile(plan.Entry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		string(entry),
		`installApplicationTheme({"accent":"#176b5b","background":"#fff","foreground":"#111","paper":"#f8f8f8"})`,
	) {
		t.Fatalf("generated entry theme is not stable:\n%s", entry)
	}
}

func TestPrepareRejectsInvalidApplicationThemeTokens(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "array", raw: `[]`, want: "must contain a JSON object"},
		{name: "unknown", raw: `{"globalBody":"red"}`, want: `unknown scoped theme key "globalBody"`},
		{name: "non-string", raw: `{"accent":12}`, want: `theme key "accent" must be a non-empty string`},
		{name: "empty", raw: `{"accent":" "}`, want: `theme key "accent" must be a non-empty string`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			storyPath := filepath.Join(root, "app.yaml")
			if err := os.WriteFile(storyPath, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "tokens.json"), []byte(test.raw), 0o644); err != nil {
				t.Fatal(err)
			}
			def := applicationDefinition("")
			delete(def.Application.Components, "demo.list")
			def.Application.Tokens = map[string]string{"demo.bad": "tokens.json"}
			manager, err := New(Config{RepoRoot: root}, func(string) (*app.AppDef, error) {
				return def, nil
			}, &fakeRunner{}, fixedClock{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Prepare(storyPath); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Prepare() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBuildReusesImmutablePublishedDigest(t *testing.T) {
	root := t.TempDir()
	storyPath := filepath.Join(root, "app.yaml")
	if err := os.WriteFile(storyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	def := applicationDefinition("")
	delete(def.Application.Components, "demo.list")
	clock := &stepClock{next: time.Date(2026, 7, 26, 4, 0, 0, 0, time.UTC)}
	manager, err := New(Config{RepoRoot: root}, func(string) (*app.AppDef, error) {
		return def, nil
	}, &fakeRunner{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Build(context.Background(), storyPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(first.ArtifactDir, "application-manifest.json")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Build(context.Background(), storyPath)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedAt != first.CreatedAt {
		t.Fatalf("rebuild changed immutable creation time: first=%s second=%s", first.CreatedAt, second.CreatedAt)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("rebuild rewrote the manifest inside an existing digest directory")
	}

	if err := os.WriteFile(filepath.Join(first.ArtifactDir, "index.html"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Build(context.Background(), storyPath); err == nil ||
		!strings.Contains(err.Error(), "assets do not match its digest") {
		t.Fatalf("corrupt immutable bundle rebuild error = %v", err)
	}
}

func TestPrepareRejectsComponentOutsideStoryRoot(t *testing.T) {
	root := t.TempDir()
	storyDir := filepath.Join(root, "story")
	if err := os.MkdirAll(storyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	storyPath := filepath.Join(storyDir, "app.yaml")
	if err := os.WriteFile(storyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.vue"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := New(Config{RepoRoot: root}, func(string) (*app.AppDef, error) {
		return applicationDefinition("../outside.vue"), nil
	}, &fakeRunner{}, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(storyPath); err == nil || !strings.Contains(err.Error(), "escapes story root") {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestPrepareAllowsComponentUnderVerifiedExternalPackageRoot(t *testing.T) {
	root := t.TempDir()
	storyDir := filepath.Join(root, "story")
	packageDir := filepath.Join(root, "resolved-package")
	if err := os.MkdirAll(storyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(packageDir, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	storyPath := filepath.Join(storyDir, "app.yaml")
	modulePath := filepath.Join(packageDir, "ui", "Panel.vue")
	if err := os.WriteFile(storyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modulePath, []byte("<template/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	def := applicationDefinition(modulePath)
	def.ApplicationPackageRoots = []string{packageDir}
	manager, err := New(Config{RepoRoot: root}, func(string) (*app.AppDef, error) {
		return def, nil
	}, &fakeRunner{}, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := manager.Prepare(storyPath)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	canonicalModule, err := filepath.EvalSymlinks(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Components) != 1 ||
		plan.Components[0].Module != "ui/Panel.vue" ||
		plan.Components[0].ResolvedModule != canonicalModule {
		t.Fatalf("components = %#v", plan.Components)
	}
}

func TestPrepareRejectsPackageModuleSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	storyDir := filepath.Join(root, "story")
	packageDir := filepath.Join(root, "resolved-package")
	if err := os.MkdirAll(storyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(packageDir, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	storyPath := filepath.Join(storyDir, "app.yaml")
	outside := filepath.Join(root, "outside.vue")
	link := filepath.Join(packageDir, "ui", "Panel.vue")
	if err := os.WriteFile(storyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("<template/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	def := applicationDefinition(link)
	def.ApplicationPackageRoots = []string{packageDir}
	manager, err := New(Config{RepoRoot: root}, func(string) (*app.AppDef, error) {
		return def, nil
	}, &fakeRunner{}, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(storyPath); err == nil || !strings.Contains(err.Error(), "escapes story root") {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestDevSupervisesBackendAndViteWithCompatibilityStatus(t *testing.T) {
	root := t.TempDir()
	storyDir := filepath.Join(root, "story")
	if err := os.MkdirAll(filepath.Join(storyDir, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	storyPath := filepath.Join(storyDir, "app.yaml")
	if err := os.WriteFile(storyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storyDir, "ui", "List.vue"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	manager, err := New(Config{
		RepoRoot: root, BackendCommand: []string{"kitsoki", "web", "--addr", "127.0.0.1:7777"},
	}, func(string) (*app.AppDef, error) {
		return applicationDefinition("ui/List.vue"), nil
	}, runner, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := manager.Dev(context.Background(), storyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 2 ||
		runner.commands[0].Name != "kitsoki" ||
		!strings.HasSuffix(runner.commands[1].Name, filepath.Join("node_modules", ".bin", "vite")) {
		t.Fatalf("commands = %#v", runner.commands)
	}
	if raw, err := os.ReadFile(plan.CompatibilityOut); err != nil || !strings.Contains(string(raw), `"status": "compatible"`) {
		t.Fatalf("compatibility = %q, %v", raw, err)
	}
}

func TestClassifyCompatibilityChanges(t *testing.T) {
	root := t.TempDir()
	storyDir := filepath.Join(root, "story")
	if err := os.MkdirAll(filepath.Join(storyDir, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	storyPath := filepath.Join(storyDir, "app.yaml")
	componentPath := filepath.Join(storyDir, "ui", "List.vue")
	if err := os.WriteFile(storyPath, []byte("story"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(componentPath, []byte("<template>A</template>"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := applicationDefinition("ui/List.vue")
	manager, err := New(Config{RepoRoot: root}, func(string) (*app.AppDef, error) {
		return definition, nil
	}, &fakeRunner{}, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := manager.Prepare(storyPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(componentPath, []byte("<template>B</template>"), 0o644); err != nil {
		t.Fatal(err)
	}
	presentation, err := manager.Classify(storyPath, plan.Compatibility)
	if err != nil || presentation.Status != "compatible" {
		t.Fatalf("presentation classification = %#v, %v", presentation, err)
	}

	definition.States["ready"].View = app.View{Elements: []app.ViewElement{{Kind: "prose", Source: "updated"}}}
	refresh, err := manager.Classify(storyPath, presentation)
	if err != nil || refresh.Status != "refresh-compatible" {
		t.Fatalf("definition classification = %#v, %v", refresh, err)
	}

	definition.States["done"] = &app.State{Terminal: true}
	reload, err := manager.Classify(storyPath, refresh)
	if err != nil || reload.Status != "reload-required" {
		t.Fatalf("shape classification = %#v, %v", reload, err)
	}
}

func applicationDefinition(componentModule string) *app.AppDef {
	return &app.AppDef{
		App:    app.AppMeta{ID: "demo", Version: "1.0.0"},
		Root:   "ready",
		States: map[string]*app.State{"ready": {}},
		Application: &app.ApplicationContract{
			Schema: app.ApplicationSchemaV1, Name: "Demo", Description: "Test application.",
			SemanticRef: "demo.application", Shell: app.ApplicationShell{Entry: "home"},
			Pages: map[string]*app.ApplicationPage{
				"home": {Name: "Home", Description: "Home page.", SemanticRef: "demo.page.home"},
			},
			Components: map[string]*app.ApplicationComponent{
				"demo.list": {
					Name: "List", Description: "List items.", SemanticRef: "demo.component.list",
					Web: &app.ApplicationWebComponent{Module: componentModule},
				},
			},
			Surfaces: map[string]*app.ApplicationSurface{
				"web": {Presentation: "default"},
				"vscode": {Reuse: "web", Native: &app.ApplicationNativeSurface{
					Commands: []string{"demo.open"},
				}},
				"tui": {Projection: "cards"},
			},
		},
	}
}

func runnerPlanPath(t *testing.T, command Command) string {
	t.Helper()
	for _, env := range command.Env {
		if strings.HasPrefix(env, "KITSOKI_APPLICATION_PLAN=") {
			return strings.TrimPrefix(env, "KITSOKI_APPLICATION_PLAN=")
		}
	}
	t.Fatal("plan environment missing")
	return ""
}
