package agentroot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"kitsoki/internal/agents"
	"kitsoki/internal/app"
	"kitsoki/internal/baseskills"
	"kitsoki/internal/effect"
	"kitsoki/internal/host"
)

// fakeRegistry is a minimal agents.Registry for injected-builtin tests.
type fakeRegistry struct{ m map[string]agents.Agent }

func (f fakeRegistry) Get(name string) (agents.Agent, bool) {
	a, ok := f.m[name]
	return a, ok
}

func (f fakeRegistry) List() []string {
	names := make([]string, 0, len(f.m))
	for name := range f.m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (f fakeRegistry) Register(agents.Agent) {}

// noLibrary is a materializer for tests that must not touch the embedded
// library — it reports the same "not staged" condition a source-tree test
// binary would.
func noLibrary(context.Context) (string, error) { return "", baseskills.ErrNotStaged }

// writeLibraryAgent writes one embedded-library-shaped agent markdown under
// root/agents and returns root for use as a fake materializer target.
func writeLibraryAgent(t *testing.T, root, name, frontmatter, instructions string) {
	t.Helper()
	dir := filepath.Join(root, "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\n" + frontmatter + "\n---\n" + instructions + "\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeProjectAgent(t *testing.T, dir, file, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIsAgentPath(t *testing.T) {
	cases := []struct {
		path string
		name string
		ok   bool
	}{
		{"agent:demo", "demo", true},
		{"agent:kitsoki-engineer", "kitsoki-engineer", true},
		{"stories/dev-story/app.yaml", "", false},
		{"agent:", "", false},
		{"agent:.", "", false},
		{"agent:..", "", false},
		{"agent:foo/bar", "", false},
		{`agent:foo\bar`, "", false},
		{"Agent:demo", "", false},
	}
	for _, tc := range cases {
		name, ok := IsAgentPath(tc.path)
		if ok != tc.ok || name != tc.name {
			t.Errorf("IsAgentPath(%q) = (%q, %v), want (%q, %v)", tc.path, name, ok, tc.name, tc.ok)
		}
	}
}

func TestResolve_SearchOrderAndExtends(t *testing.T) {
	projDir := filepath.Join(t.TempDir(), "agents")
	libRoot := t.TempDir()

	// "everywhere" is defined by all three sources; project must win.
	writeProjectAgent(t, projDir, "everywhere.toml",
		"name = \"everywhere\"\ndescription = \"project wins\"\ndeveloper_instructions = \"Project instructions.\"\nsandbox_mode = \"read-only\"\n")
	writeLibraryAgent(t, libRoot, "everywhere", "description: library loses\ntools: Read, Grep", "Library instructions.")
	// "libbuilt" is library + builtin; library must win.
	writeLibraryAgent(t, libRoot, "libbuilt", "description: from library\ntools: Read, Edit, Bash", "Library-built instructions.")
	// extends chain: child extends base within the project dir.
	writeProjectAgent(t, projDir, "base.toml",
		"developer_instructions = \"Base instructions.\"\nmodel = \"base-model\"\n")
	writeProjectAgent(t, projDir, "child.toml",
		"extends = \"base.toml\"\ndeveloper_instructions_append = \"Child extras.\"\n")
	// local shadow: shadowed.local.toml beats shadowed.toml.
	writeProjectAgent(t, projDir, "shadowed.toml",
		"developer_instructions = \"Plain file.\"\n")
	writeProjectAgent(t, projDir, "shadowed.local.toml",
		"developer_instructions = \"Local override.\"\n")

	src := Sources{
		ProjectDirs: []string{projDir},
		Materialize: func(context.Context) (string, error) { return libRoot, nil },
		Builtins: fakeRegistry{m: map[string]agents.Agent{
			"everywhere": {Name: "everywhere", SystemPrompt: "Builtin loses."},
			"libbuilt":   {Name: "libbuilt", SystemPrompt: "Builtin loses."},
			"builtonly":  {Name: "builtonly", SystemPrompt: "Answer questions concisely.", Tools: []string{"Read"}},
		}},
	}

	def, err := Resolve("everywhere", src)
	if err != nil {
		t.Fatalf("Resolve(everywhere): %v", err)
	}
	if def.Source != SourceProject || def.SystemPrompt != "Project instructions." {
		t.Fatalf("everywhere resolved to %q from %s, want project", def.SystemPrompt, def.Source)
	}
	if def.Effect != effect.Read {
		t.Fatalf("everywhere (sandbox_mode read-only) effect = %s, want read", def.Effect)
	}

	def, err = Resolve("libbuilt", src)
	if err != nil {
		t.Fatalf("Resolve(libbuilt): %v", err)
	}
	if def.Source != SourceLibrary || def.SystemPrompt != "Library-built instructions." {
		t.Fatalf("libbuilt resolved to %q from %s, want library", def.SystemPrompt, def.Source)
	}
	if def.Effect != effect.Write {
		t.Fatalf("libbuilt (tools incl. Edit/Bash) effect = %s, want write", def.Effect)
	}

	def, err = Resolve("builtonly", src)
	if err != nil {
		t.Fatalf("Resolve(builtonly): %v", err)
	}
	if def.Source != SourceBuiltin || def.Effect != effect.Read {
		t.Fatalf("builtonly = source %s effect %s, want builtin/read", def.Source, def.Effect)
	}

	def, err = Resolve("child", src)
	if err != nil {
		t.Fatalf("Resolve(child): %v", err)
	}
	want := "Base instructions.\n\nChild extras."
	if def.SystemPrompt != want {
		t.Fatalf("extends merge: got %q, want %q", def.SystemPrompt, want)
	}
	if def.Model != "base-model" {
		t.Fatalf("extends merge: model = %q, want base-model", def.Model)
	}

	def, err = Resolve("shadowed", src)
	if err != nil {
		t.Fatalf("Resolve(shadowed): %v", err)
	}
	if def.SystemPrompt != "Local override." {
		t.Fatalf("local shadow: got %q, want the .local.toml layer", def.SystemPrompt)
	}

	if _, err := Resolve("missing", src); err == nil {
		t.Fatal("Resolve(missing) succeeded, want catalog error")
	} else if !strings.Contains(err.Error(), "builtonly") {
		t.Fatalf("Resolve(missing) error should name the catalog, got: %v", err)
	}

	if _, err := Resolve("bad/name", src); err == nil {
		t.Fatal("Resolve with a path separator in the name must fail")
	}
}

func TestList_MergesBuiltinsLibraryProject(t *testing.T) {
	projDir := filepath.Join(t.TempDir(), "agents")
	libRoot := t.TempDir()

	writeProjectAgent(t, projDir, "everywhere.toml",
		"description = \"project wins\"\ndeveloper_instructions = \"Project instructions.\"\n")
	writeLibraryAgent(t, libRoot, "everywhere", "description: library loses\ntools: Read", "Library instructions.")
	writeLibraryAgent(t, libRoot, "libonly", "description: only in library\ntools: Read, Write", "Lib instructions.")

	src := Sources{
		ProjectDirs: []string{projDir},
		Materialize: func(context.Context) (string, error) { return libRoot, nil },
		Builtins: fakeRegistry{m: map[string]agents.Agent{
			"everywhere": {Name: "everywhere", SystemPrompt: "Builtin loses."},
			"builtonly":  {Name: "builtonly", SystemPrompt: "First line description.\nSecond line.", Tools: []string{"Read"}},
		}},
	}

	infos, err := List(src)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byName := map[string]Info{}
	var names []string
	for _, info := range infos {
		byName[info.Name] = info
		names = append(names, info.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("List order not deterministic/sorted: %v", names)
	}
	wantNames := []string{"builtonly", "everywhere", "libonly"}
	if len(infos) != len(wantNames) {
		t.Fatalf("List returned %v, want names %v", names, wantNames)
	}

	ev := byName["everywhere"]
	if ev.Source != SourceProject || ev.Description != "project wins" {
		t.Fatalf("everywhere = %+v, want project-sourced winner", ev)
	}
	if len(ev.Shadows) != 2 || ev.Shadows[0] != SourceLibrary || ev.Shadows[1] != SourceBuiltin {
		t.Fatalf("everywhere.Shadows = %v, want [library builtin]", ev.Shadows)
	}
	// A project TOML with no sandbox_mode defaults to the full write surface.
	if ev.Effect != effect.Write {
		t.Fatalf("everywhere effect = %s, want write", ev.Effect)
	}

	lib := byName["libonly"]
	if lib.Source != SourceLibrary || lib.Effect != effect.Write || len(lib.Shadows) != 0 {
		t.Fatalf("libonly = %+v, want unshadowed library write agent", lib)
	}

	bo := byName["builtonly"]
	if bo.Source != SourceBuiltin || bo.Effect != effect.Read {
		t.Fatalf("builtonly = %+v, want builtin read agent", bo)
	}
	if bo.Description != "First line description." {
		t.Fatalf("builtonly description = %q, want the prompt's first line", bo.Description)
	}

	// A not-staged library is skipped, not fatal.
	infos, err = List(Sources{ProjectDirs: []string{projDir}, Materialize: noLibrary, Builtins: fakeRegistry{m: map[string]agents.Agent{}}})
	if err != nil {
		t.Fatalf("List without library: %v", err)
	}
	if len(infos) != 1 || infos[0].Name != "everywhere" {
		t.Fatalf("List without library = %+v, want just the project agent", infos)
	}
}

func writeAgentDef() Def {
	return Def{
		Name:         "demo",
		Description:  "Demo write agent",
		SystemPrompt: "You are a demo engineering agent.",
		Model:        "claude-sonnet-4-6",
		Tools:        []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash"},
		Effect:       effect.Write,
		Source:       SourceProject,
	}
}

func TestSynthesize_WriteAgentDesugarsToWorkbench(t *testing.T) {
	def := writeAgentDef()
	appDef, err := Synthesize(def, Options{WorkingDir: t.TempDir(), SchemaDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if appDef.App.ID != "agent:demo" {
		t.Fatalf("app id = %q, want agent:demo", appDef.App.ID)
	}
	room := appDef.States[RoomName]
	if room == nil {
		t.Fatalf("synthesized root has no %q room (states: %v)", RoomName, len(appDef.States))
	}
	// The workbench macro desugared: write_mode read_only, an off-ramp, a
	// synthesized on_enter host.agent.task, and the <room>_capture intent as
	// default_intent.
	if room.WriteMode != app.WriteModeReadOnly {
		t.Fatalf("room write_mode = %q, want %q", room.WriteMode, app.WriteModeReadOnly)
	}
	if room.AgentOffRamp == nil || room.AgentOffRamp.Agent != "demo" {
		t.Fatalf("room off-ramp = %+v, want agent demo", room.AgentOffRamp)
	}
	foundTask := false
	for _, eff := range room.OnEnter {
		if eff.Invoke == "host.agent.task" {
			foundTask = true
			if got := eff.With["agent"]; got != "demo" {
				t.Fatalf("on_enter dispatch agent = %v, want demo", got)
			}
		}
	}
	if !foundTask {
		t.Fatal("no synthesized on_enter host.agent.task dispatch")
	}
	captureIntent := RoomName + "_capture"
	if room.DefaultIntent != captureIntent {
		t.Fatalf("default_intent = %q, want %q", room.DefaultIntent, captureIntent)
	}
	if _, ok := appDef.Intents[captureIntent]; !ok {
		t.Fatalf("synthesized capture intent %q not registered", captureIntent)
	}
	decl := appDef.Agents["demo"]
	if decl == nil {
		t.Fatal("agents: block lost the resolved agent")
	}
	if decl.Effect != effect.Write {
		t.Fatalf("agent effect = %s, want write", decl.Effect)
	}
	if decl.Toolbox == "" {
		t.Fatal("workbench agent must carry the WS toolbox: vocabulary")
	}
}

func TestSynthesize_ReadOnlyAgentGetsOffRamp(t *testing.T) {
	def := Def{
		Name:         "explainer",
		Description:  "Read-only Q&A",
		SystemPrompt: "You answer questions.",
		Tools:        []string{"Read", "Grep", "Glob"},
		Effect:       effect.Read,
		Source:       SourceBuiltin,
	}
	policyCalled := false
	appDef, err := Synthesize(def, Options{
		SchemaDir: t.TempDir(),
		CheckPolicy: func(context.Context, string, string, string) (host.AgentLaunchDecision, error) {
			policyCalled = true
			return host.AgentLaunchDecision{}, errors.New("must not be called for read-only agents")
		},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if policyCalled {
		t.Fatal("launch-policy preflight ran for a read-only agent")
	}
	room := appDef.States[RoomName]
	if room == nil {
		t.Fatal("synthesized root has no agent room")
	}
	if room.WriteMode != "" {
		t.Fatalf("read-only room write_mode = %q, want unset (no workbench)", room.WriteMode)
	}
	if room.AgentOffRamp == nil || room.AgentOffRamp.Agent != "explainer" || !room.AgentOffRamp.CaptureFreeText {
		t.Fatalf("off-ramp = %+v, want capture_free_text conversational shape", room.AgentOffRamp)
	}
	for _, eff := range room.OnEnter {
		if eff.Invoke == "host.agent.task" {
			t.Fatal("read-only agent room must not dispatch host.agent.task on enter")
		}
	}
	discussIntent := RoomName + app.OffRampCaptureIntentSuffix
	if room.DefaultIntent != discussIntent {
		t.Fatalf("default_intent = %q, want the synthesized %q sink", room.DefaultIntent, discussIntent)
	}
}

func TestSynthesize_AppIDAndVersionHash(t *testing.T) {
	def := writeAgentDef()
	opts := Options{WorkingDir: t.TempDir(), SchemaDir: t.TempDir()}
	first, err := Synthesize(def, opts)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if first.App.ID != Scheme+def.Name {
		t.Fatalf("app id = %q, want %q", first.App.ID, Scheme+def.Name)
	}
	if first.App.Version == "" {
		t.Fatal("version hash is empty")
	}
	again, err := Synthesize(def, opts)
	if err != nil {
		t.Fatalf("Synthesize (again): %v", err)
	}
	if again.App.Version != first.App.Version {
		t.Fatalf("version not deterministic: %q vs %q", again.App.Version, first.App.Version)
	}
	changed := def
	changed.SystemPrompt = "You are a different agent now."
	drifted, err := Synthesize(changed, opts)
	if err != nil {
		t.Fatalf("Synthesize (drifted): %v", err)
	}
	if drifted.App.Version == first.App.Version {
		t.Fatal("definition drift did not change the version hash")
	}
}

// TestSynthesize_ExternalAgentGetsOffRampWithPreflight pins the deviation
// from the design's "write|external → workbench" shape: validateWriteMode
// hard-rejects write_mode: read_only over an external agent, so external
// agents get the conversational off-ramp shape — but keep the launch-policy
// preflight.
func TestSynthesize_ExternalAgentGetsOffRampWithPreflight(t *testing.T) {
	def := Def{
		Name:         "filer",
		SystemPrompt: "You file bugs.",
		Tools:        []string{"Read", "Grep", "mcp__bug__create"},
		Effect:       effect.External,
		Source:       SourceBuiltin,
	}
	policyCalled := false
	appDef, err := Synthesize(def, Options{
		WorkingDir: t.TempDir(),
		SchemaDir:  t.TempDir(),
		CheckPolicy: func(context.Context, string, string, string) (host.AgentLaunchDecision, error) {
			policyCalled = true
			return host.AgentLaunchDecision{Enabled: true, Allowed: true, Reason: "allowed"}, nil
		},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if !policyCalled {
		t.Fatal("launch-policy preflight must run for external agents")
	}
	room := appDef.States[RoomName]
	if room == nil {
		t.Fatal("synthesized root has no agent room")
	}
	if room.WriteMode != "" {
		t.Fatalf("external room write_mode = %q, want unset (conversational shape)", room.WriteMode)
	}
	if room.AgentOffRamp == nil || !room.AgentOffRamp.CaptureFreeText {
		t.Fatalf("off-ramp = %+v, want capture_free_text conversational shape", room.AgentOffRamp)
	}
	for _, eff := range room.OnEnter {
		if eff.Invoke == "host.agent.task" {
			t.Fatal("external agent room must not synthesize a workbench dispatch")
		}
	}
}

// TestSynthesize_ExternalAgentCwdIsEffectiveWorkingDir pins the
// conversational-lane working-dir threading: an external agent's converse
// dispatch reads the synthesized decl's cwd (there is no world.workdir in the
// off-ramp shape), so the decl cwd must be the preflight-checked effective
// working dir — including when the definition declares its own cwd. Otherwise
// a session whose working dir was auto-provisioned into a capsule would
// converse back in the protected root the policy steered away from.
func TestSynthesize_ExternalAgentCwdIsEffectiveWorkingDir(t *testing.T) {
	allow := func(context.Context, string, string, string) (host.AgentLaunchDecision, error) {
		return host.AgentLaunchDecision{Enabled: true, Allowed: true, Reason: "allowed"}, nil
	}
	for _, tc := range []struct {
		name   string
		defCwd string
	}{
		{name: "no declared cwd", defCwd: ""},
		{name: "declared cwd is overridden by the checked working dir", defCwd: "/somewhere/else"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workingDir := t.TempDir()
			def := Def{
				Name:         "filer",
				SystemPrompt: "You file bugs.",
				Tools:        []string{"Read", "mcp__bug__create"},
				Effect:       effect.External,
				Cwd:          tc.defCwd,
				Source:       SourceBuiltin,
			}
			appDef, err := Synthesize(def, Options{
				WorkingDir:  workingDir,
				SchemaDir:   t.TempDir(),
				CheckPolicy: allow,
			})
			if err != nil {
				t.Fatalf("Synthesize: %v", err)
			}
			decl := appDef.Agents["filer"]
			if decl == nil {
				t.Fatal("synthesized root lost the agent decl")
			}
			if decl.Cwd != workingDir {
				t.Fatalf("synthesized decl cwd = %q, want effective working dir %q", decl.Cwd, workingDir)
			}
		})
	}
}

// TestSynthesize_DeclaredBackendPinsProvider pins the backend half of the
// agent's voice: a definition that declares backend: (e.g. pog-driver's codex
// + gpt-5.5) must synthesize a providers: entry the decl references, so
// applyProvider forces that backend on every dispatch instead of pairing the
// definition's model with the session's ambient backend or active harness
// profile.
func TestSynthesize_DeclaredBackendPinsProvider(t *testing.T) {
	allow := func(context.Context, string, string, string) (host.AgentLaunchDecision, error) {
		return host.AgentLaunchDecision{Enabled: true, Allowed: true, Reason: "allowed"}, nil
	}
	def := Def{
		Name:         "driver",
		SystemPrompt: "You drive.",
		Backend:      "codex",
		Model:        "gpt-5.5",
		Tools:        []string{"Read", "mcp__graph__propose"},
		Effect:       effect.External,
		Source:       SourceLibrary,
	}
	appDef, err := Synthesize(def, Options{
		WorkingDir:  t.TempDir(),
		SchemaDir:   t.TempDir(),
		CheckPolicy: allow,
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	prov := appDef.Providers[backendProviderName]
	if prov == nil || prov.Backend != "codex" {
		t.Fatalf("synthesized provider %q = %+v, want backend codex", backendProviderName, prov)
	}
	decl := appDef.Agents["driver"]
	if decl == nil {
		t.Fatal("synthesized root lost the agent decl")
	}
	if decl.Provider != backendProviderName {
		t.Fatalf("decl provider = %q, want %q", decl.Provider, backendProviderName)
	}
	if decl.Model != "gpt-5.5" {
		t.Fatalf("decl model = %q, want the declared gpt-5.5 (provider must not displace it)", decl.Model)
	}

	def.Backend = ""
	appDef, err = Synthesize(def, Options{
		WorkingDir:  t.TempDir(),
		SchemaDir:   t.TempDir(),
		CheckPolicy: allow,
	})
	if err != nil {
		t.Fatalf("Synthesize (no backend): %v", err)
	}
	if len(appDef.Providers) != 0 {
		t.Fatalf("backendless definition synthesized providers %v, want none (ambient backend)", appDef.Providers)
	}
	if appDef.Agents["driver"].Provider != "" {
		t.Fatalf("backendless decl provider = %q, want empty", appDef.Agents["driver"].Provider)
	}
}

// TestResolve_LibraryBackendFrontmatter pins backend: as a library-frontmatter
// field — a definition like pog-driver.md pairs its codex model with
// backend: codex and must resolve with both.
func TestResolve_LibraryBackendFrontmatter(t *testing.T) {
	libRoot := t.TempDir()
	writeLibraryAgent(t, libRoot, "driver",
		"backend: codex\nmodel: gpt-5.5\ntools: Read", "Driver instructions.")
	def, err := Resolve("driver", Sources{
		Materialize: func(context.Context) (string, error) { return libRoot, nil },
	})
	if err != nil {
		t.Fatalf("Resolve(driver): %v", err)
	}
	if def.Backend != "codex" || def.Model != "gpt-5.5" {
		t.Fatalf("resolved backend/model = %q/%q, want codex/gpt-5.5", def.Backend, def.Model)
	}
}

// TestResolve_LibraryMCPServersReachAgentMode pins the packaged-MCP contract
// for agent mode: a library agent resolves with the same builtInAgentMCPServers
// set `agent launch` renders, and Synthesize carries it into the decl —
// otherwise `agent:pog-driver` allowlists mcp__kitsoki-graph__* tools that no
// attached server provides.
func TestResolve_LibraryMCPServersReachAgentMode(t *testing.T) {
	libRoot := t.TempDir()
	writeLibraryAgent(t, libRoot, "pog-driver",
		"tools: mcp__kitsoki-codeact__*, mcp__kitsoki-graph__*", "Drive POG.")
	writeLibraryAgent(t, libRoot, "other-driver",
		"tools: mcp__kitsoki__*", "Drive studio.")
	src := Sources{Materialize: func(context.Context) (string, error) { return libRoot, nil }}

	def, err := Resolve("pog-driver", src)
	if err != nil {
		t.Fatalf("Resolve(pog-driver): %v", err)
	}
	for _, server := range []string{"kitsoki-graph", "kitsoki-codeact", "kitsoki-agent-launch"} {
		if _, ok := def.MCPServers[server]; !ok {
			t.Fatalf("pog-driver MCPServers = %v, want %s attached", def.MCPServers, server)
		}
	}

	other, err := Resolve("other-driver", src)
	if err != nil {
		t.Fatalf("Resolve(other-driver): %v", err)
	}
	if _, ok := other.MCPServers["kitsoki"]; !ok || len(other.MCPServers) != 1 {
		t.Fatalf("other-driver MCPServers = %v, want the default Studio server", other.MCPServers)
	}

	appDef, err := Synthesize(def, Options{
		WorkingDir: t.TempDir(),
		SchemaDir:  t.TempDir(),
		CheckPolicy: func(context.Context, string, string, string) (host.AgentLaunchDecision, error) {
			return host.AgentLaunchDecision{Enabled: true, Allowed: true, Reason: "allowed"}, nil
		},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	decl := appDef.Agents["pog-driver"]
	if decl == nil || decl.MCP == nil {
		t.Fatalf("synthesized decl mcp = %+v, want servers attached", decl)
	}
	if _, ok := decl.MCP.Servers["kitsoki-graph"]; !ok {
		t.Fatalf("synthesized mcp servers = %v, want kitsoki-graph", decl.MCP.Servers)
	}
}

// TestResolve_ProjectOverlayKeepsPackagedToolContract pins the federated-graph
// override shape: a project overlay that only swaps an MCP server command must
// inherit the embedded base's tool list and external effect (via the rendered
// `tools` field), not fall back to the generic sandbox_mode write toolbox —
// and must keep the base's other packaged servers.
func TestResolve_ProjectOverlayKeepsPackagedToolContract(t *testing.T) {
	libRoot := t.TempDir()
	writeLibraryAgent(t, libRoot, "pog-driver",
		"tools: mcp__kitsoki-codeact__*, mcp__kitsoki-graph__*", "Drive POG.")
	projDir := filepath.Join(t.TempDir(), "agents")
	writeProjectAgent(t, projDir, "pog-driver.toml",
		"[mcp_servers.kitsoki-graph]\ncommand = \"bash\"\nargs = [\"scripts/pog-driver-mcp.sh\"]\n")

	def, err := Resolve("pog-driver", Sources{
		ProjectDirs: []string{projDir},
		Materialize: func(context.Context) (string, error) { return libRoot, nil },
	})
	if err != nil {
		t.Fatalf("Resolve(pog-driver): %v", err)
	}
	if def.Source != SourceProject {
		t.Fatalf("source = %s, want project", def.Source)
	}
	if len(def.Tools) != 2 || def.Tools[1] != "mcp__kitsoki-graph__*" {
		t.Fatalf("tools = %v, want the packaged mcp__* contract", def.Tools)
	}
	if def.Effect != effect.External {
		t.Fatalf("effect = %s, want external", def.Effect)
	}
	graph, ok := def.MCPServers["kitsoki-graph"].(map[string]any)
	if !ok || graph["command"] != "bash" {
		t.Fatalf("kitsoki-graph server = %v, want overridden bash launcher", def.MCPServers["kitsoki-graph"])
	}
	if _, ok := def.MCPServers["kitsoki-codeact"]; !ok {
		t.Fatalf("servers = %v, want base kitsoki-codeact preserved", def.MCPServers)
	}
}

// TestSynthesize_RealBuiltinsRoundTrip resolves every real builtin (in-memory
// registry, no embedded library, no project dirs) and synthesizes it — a
// smoke test that arbitrary real prompts/tool surfaces survive the YAML
// round-trip through the normal loader.
func TestSynthesize_RealBuiltinsRoundTrip(t *testing.T) {
	src := Sources{ProjectDirs: []string{}, Materialize: noLibrary}
	for _, name := range agents.BuiltinNames() {
		def, err := Resolve(name, src)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", name, err)
		}
		appDef, err := Synthesize(def, Options{WorkingDir: t.TempDir(), SchemaDir: t.TempDir()})
		if err != nil {
			t.Fatalf("Synthesize(%s): %v", name, err)
		}
		if appDef.App.ID != Scheme+name {
			t.Fatalf("Synthesize(%s) app id = %q", name, appDef.App.ID)
		}
		if appDef.States[RoomName] == nil {
			t.Fatalf("Synthesize(%s) lost the agent room", name)
		}
	}
}

// TestResolveBuiltin_CwdEnvExpansion pins the catalog/startability invariant
// for builtins whose DefaultCwd references env vars (the kitsoki-* agents'
// "${KITSOKI_REPO}"): every row List returns must be synthesizable in the
// same environment. With the var unset, the cwd is dropped at resolve time
// (falling back to the session working dir) and synthesis still succeeds;
// with it set, the resolved cwd is the expanded path.
func TestResolveBuiltin_CwdEnvExpansion(t *testing.T) {
	src := Sources{ProjectDirs: []string{}, Materialize: noLibrary}

	t.Run("unset var drops cwd and every catalog row stays startable", func(t *testing.T) {
		t.Setenv("KITSOKI_REPO", "") // registers cleanup/restore
		if err := os.Unsetenv("KITSOKI_REPO"); err != nil {
			t.Fatal(err)
		}

		infos, err := List(src)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		seen := map[string]bool{}
		for _, info := range infos {
			seen[info.Name] = true
			def, err := Resolve(info.Name, src)
			if err != nil {
				t.Fatalf("Resolve(%s): List advertises an unresolvable agent: %v", info.Name, err)
			}
			if _, err := Synthesize(def, Options{WorkingDir: t.TempDir(), SchemaDir: t.TempDir()}); err != nil {
				t.Fatalf("Synthesize(%s): List advertises an unstartable agent: %v", info.Name, err)
			}
		}
		if !seen[agents.NameKitsokiEngineer] {
			t.Fatalf("List = %v, want it to include %s", infos, agents.NameKitsokiEngineer)
		}

		def, err := Resolve(agents.NameKitsokiEngineer, src)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", agents.NameKitsokiEngineer, err)
		}
		if def.Cwd != "" {
			t.Fatalf("Def.Cwd = %q, want empty (fallback to session working dir) when KITSOKI_REPO is unset", def.Cwd)
		}
		appDef, err := Synthesize(def, Options{WorkingDir: t.TempDir(), SchemaDir: t.TempDir()})
		if err != nil {
			t.Fatalf("Synthesize(%s): %v", agents.NameKitsokiEngineer, err)
		}
		decl := appDef.Agents[agents.NameKitsokiEngineer]
		if decl == nil {
			t.Fatalf("synthesized root lost agent decl %q", agents.NameKitsokiEngineer)
		}
		if decl.Cwd != "" {
			t.Fatalf("synthesized decl cwd = %q, want empty fallback", decl.Cwd)
		}
	})

	t.Run("set var expands cwd to the repo path", func(t *testing.T) {
		repo := t.TempDir()
		t.Setenv("KITSOKI_REPO", repo)

		def, err := Resolve(agents.NameKitsokiEngineer, src)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", agents.NameKitsokiEngineer, err)
		}
		if def.Cwd != repo {
			t.Fatalf("Def.Cwd = %q, want expanded %q", def.Cwd, repo)
		}
		appDef, err := Synthesize(def, Options{WorkingDir: t.TempDir(), SchemaDir: t.TempDir()})
		if err != nil {
			t.Fatalf("Synthesize(%s): %v", agents.NameKitsokiEngineer, err)
		}
		decl := appDef.Agents[agents.NameKitsokiEngineer]
		if decl == nil {
			t.Fatalf("synthesized root lost agent decl %q", agents.NameKitsokiEngineer)
		}
		if decl.Cwd != repo {
			t.Fatalf("synthesized decl cwd = %q, want expanded %q", decl.Cwd, repo)
		}
	})
}

func TestSynthesize_LaunchPolicyDeniesProtectedRootWriteAgent(t *testing.T) {
	def := writeAgentDef()
	denied := host.AgentLaunchDecision{
		Enabled:       true,
		Allowed:       false,
		Verb:          "agent.mode",
		Agent:         "demo",
		ProtectedRoot: "/protected/repo",
		Reason:        "working_dir /protected/repo is inside protected root /protected/repo",
	}
	_, err := Synthesize(def, Options{
		WorkingDir: "/protected/repo",
		SchemaDir:  t.TempDir(),
		CheckPolicy: func(_ context.Context, verb, agentName, workingDir string) (host.AgentLaunchDecision, error) {
			if verb != "agent.mode" || agentName != "demo" {
				t.Fatalf("preflight called with (%q, %q), want (agent.mode, demo)", verb, agentName)
			}
			if workingDir == "" {
				t.Fatal("preflight called with empty working dir")
			}
			return denied, fmt.Errorf("agent launch policy denied: %s", denied.Reason)
		},
	})
	if err == nil {
		t.Fatal("Synthesize succeeded, want launch-policy denial")
	}
	var policyErr *PolicyDeniedError
	if !errors.As(err, &policyErr) {
		t.Fatalf("error is %T, want *PolicyDeniedError: %v", err, err)
	}
	if policyErr.Decision.ProtectedRoot != "/protected/repo" || policyErr.Decision.Allowed {
		t.Fatalf("decision not surfaced: %+v", policyErr.Decision)
	}
	if !strings.Contains(err.Error(), "capsule") {
		t.Fatalf("denial should carry capsule guidance, got: %v", err)
	}
}
