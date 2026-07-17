// Package agentroot is the foundation of agent mode: it recognizes the
// virtual story path `agent:<name>`, resolves the named agent definition
// through the same search order `kitsoki agent launch` uses (project
// .kitsoki/agents → .codex/agents → ~/.codex/agents → embedded agent library
// → builtin registry), and synthesizes a one-room in-memory AppDef around it
// so every existing session surface (TUI, web, CLI, MCP studio, flow tests)
// runs an agent session as a perfectly normal story session.
//
// This composes two tricks the codebase already ships: the synthesized
// implicit root (app.SynthesizeRootWithResolver — an AppDef with no file on
// disk) and the `workbench:` room macro (internal/app/workbench.go — a room
// that IS an agent floor). See .context/agent-mode-design.md /
// docs/architecture/room-workbench.md.
package agentroot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kitsoki/internal/agents"
	"kitsoki/internal/baseskills"
	"kitsoki/internal/effect"
)

// Scheme is the virtual story-path prefix agent mode owns.
const Scheme = "agent:"

// Source identifies where a resolved agent definition came from, in search
// order: a higher source shadows a lower one under the same name.
type Source string

const (
	// SourceProject is a `.kitsoki/agents` / `.codex/agents` / `~/.codex/agents`
	// TOML file (with extends chains and embedded-base overlay fallback).
	SourceProject Source = "project"
	// SourceLibrary is the embedded agent library (baseskills, agents/*.md).
	SourceLibrary Source = "library"
	// SourceBuiltin is the builtin registry (internal/agents.NewBuiltins).
	SourceBuiltin Source = "builtin"
)

// IsAgentPath reports whether path uses the `agent:<name>` scheme and returns
// the agent name. It is deliberately cheap (string ops only) — loader heads
// call it before filepath.Abs on every story-path argument. A syntactically
// invalid name (empty, ".", "..", or containing a path separator) returns
// ok=false so the caller falls through to normal path handling.
func IsAgentPath(path string) (name string, ok bool) {
	if !strings.HasPrefix(path, Scheme) {
		return "", false
	}
	name = path[len(Scheme):]
	if !ValidName(name) {
		return "", false
	}
	return name, true
}

// ValidName reports whether name is an acceptable agent name: non-empty, not
// "." or "..", and free of path separators (both slash flavors, so a name can
// never escape into path-shaped code on any platform).
func ValidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

// Sources is the DI bundle Resolve/List search. The zero value means the real
// chain: DefaultProjectAgentDirs, the embedded baseskills library, and the
// builtin registry. Tests inject fakes per field.
type Sources struct {
	// ProjectDirs are the directories searched (in order) for `<name>.toml`
	// with a `<name>.local.toml` shadow, exactly like `kitsoki agent launch`.
	// Nil means DefaultProjectAgentDirs(); an explicit empty non-nil slice
	// disables the project tier.
	ProjectDirs []string
	// Materialize materializes the embedded agent library. Nil means
	// baseskills.Materialize; a library that is not staged
	// (baseskills.ErrNotStaged) is silently skipped.
	Materialize LibraryMaterializer
	// Builtins is the builtin agent registry. Nil means agents.NewBuiltins().
	Builtins agents.Registry
}

func (s Sources) projectDirs() []string {
	if s.ProjectDirs != nil {
		return s.ProjectDirs
	}
	return DefaultProjectAgentDirs()
}

func (s Sources) builtins() agents.Registry {
	if s.Builtins != nil {
		return s.Builtins
	}
	return agents.NewBuiltins()
}

// Def is a resolved agent definition — the source-neutral projection Synthesize
// consumes. Exactly one source populated it (Source/Path say which).
type Def struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	SystemPrompt string `json:"system_prompt"`
	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
	// Tools is the resolved tool surface. Project TOML agents declare no tool
	// list, so Resolve assigns one from SandboxMode (see resolveProjectDef);
	// library/builtin agents carry their declared list.
	Tools []string `json:"tools,omitempty"`
	// MCPServers carries `[mcp_servers.*]` blocks from a project TOML agent
	// (map of name → {command, args, cwd}).
	MCPServers map[string]any `json:"mcp_servers,omitempty"`
	// Cwd is a builtin agent's default working dir, env-expanded by Resolve
	// (defFromBuiltin). A DefaultCwd referencing an unset env var (e.g. the
	// kitsoki-* builtins' "${KITSOKI_REPO}" outside a checkout) resolves to ""
	// so the synthesized decl falls back to the session working dir — every
	// catalog row List returns stays startable in the same environment.
	Cwd string `json:"cwd,omitempty"`
	// SandboxMode is the raw codex sandbox_mode of a project TOML agent
	// ("read-only" | "workspace-write" | "danger-full-access" | "").
	SandboxMode string `json:"sandbox_mode,omitempty"`
	// Effect is the resolved effect class: write agents synthesize a
	// workbench room; read|pure|external agents synthesize a conversational
	// off-ramp room (see Synthesize for why external is conversational).
	// Always populated by Resolve.
	Effect effect.Effect `json:"effect"`
	Source Source        `json:"source"`
	// Path is the resolved definition file for project agents ("" otherwise).
	Path string `json:"path,omitempty"`
}

// projectWriteToolbox is the tool surface a project TOML agent gets when its
// sandbox_mode permits writes — the same full Claude-Code-like surface
// dev-story's landing_toolbox carries (stories/dev-story/app.yaml).
var projectWriteToolbox = []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash"}

// projectReadToolbox is the read-only surface for sandbox_mode: read-only.
var projectReadToolbox = []string{"Read", "Grep", "Glob"}

// Resolve resolves name to an agent definition through the search order:
// project TOML dirs (with extends chains and embedded-base overlay fallback,
// byte-compatible with `kitsoki agent launch`) → embedded agent library →
// builtin registry. An unknown name returns an error naming the catalog so
// the caller's UX matches an invalid story path.
func Resolve(name string, src Sources) (Def, error) {
	if !ValidName(name) {
		return Def{}, fmt.Errorf("agent name %q is invalid (must be non-empty with no path separators)", name)
	}

	// 1. Project TOML chain.
	for _, candidate := range standaloneAgentCandidates(name, src.projectDirs()) {
		st, err := os.Stat(candidate)
		if err != nil || st.IsDir() {
			continue
		}
		def, err := resolveProjectDef(name, candidate, src.Materialize)
		if err != nil {
			return Def{}, err
		}
		return def, nil
	}

	// 2. Embedded agent library.
	if def, ok, err := resolveLibraryDef(name, src.Materialize); err != nil {
		return Def{}, err
	} else if ok {
		return def, nil
	}

	// 3. Builtin registry.
	if a, ok := src.builtins().Get(name); ok {
		return defFromBuiltin(a), nil
	}

	catalog, _ := List(src)
	names := make([]string, 0, len(catalog))
	for _, info := range catalog {
		names = append(names, info.Name)
	}
	return Def{}, fmt.Errorf("agent %q not found in project agent dirs, the embedded agent library, or the builtin registry (known agents: %s)", name, strings.Join(names, ", "))
}

// resolveProjectDef loads one project TOML candidate: a complete definition
// (possibly via extends) loads directly; a partial overlay merges over the
// embedded library base, mirroring ResolveStandaloneAgentFile's fallback.
func resolveProjectDef(name, path string, materialize LibraryMaterializer) (Def, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Def{}, err
	}
	agent, loadErr := LoadStandaloneCodexAgent(abs)
	if loadErr != nil {
		rendered, cleanup, err := renderBuiltInStandaloneCodexAgentWithTemplate(name, abs, materialize)
		if err != nil {
			return Def{}, fmt.Errorf("load project agent %s: %w", abs, loadErr)
		}
		if cleanup != nil {
			defer cleanup()
		}
		agent, err = LoadStandaloneCodexAgent(rendered)
		if err != nil {
			return Def{}, fmt.Errorf("load rendered project agent %s: %w", abs, err)
		}
	}
	return defFromStandalone(name, agent, abs), nil
}

// defFromStandalone projects a loaded TOML agent onto Def. A TOML agent
// declares no tool list, so the effect (and the synthesized tool surface)
// derive from sandbox_mode: read-only stays a conversational read agent,
// anything else gets the full write toolbox — the write-mode gate, not the
// static list, is the runtime protection (room-workbench contract).
func defFromStandalone(name string, agent StandaloneCodexAgent, path string) Def {
	def := Def{
		Name:         name,
		Description:  agent.Description,
		SystemPrompt: agent.DeveloperInstructions,
		Model:        agent.Model,
		Effort:       agent.Effort,
		MCPServers:   agent.MCPServers,
		SandboxMode:  agent.SandboxMode,
		Source:       SourceProject,
		Path:         path,
	}
	if strings.TrimSpace(agent.SandboxMode) == "read-only" {
		def.Tools = append([]string(nil), projectReadToolbox...)
		def.Effect = effect.Read
	} else {
		def.Tools = append([]string(nil), projectWriteToolbox...)
		def.Effect = effect.Write
	}
	return def
}

// resolveLibraryDef looks name up in the embedded agent library. ok=false
// means "not there" (including a library that is not staged); a malformed
// entry is an error.
func resolveLibraryDef(name string, materialize LibraryMaterializer) (Def, bool, error) {
	root, err := materialize.orDefault()(context.Background())
	if err != nil {
		if errors.Is(err, baseskills.ErrNotStaged) {
			return Def{}, false, nil
		}
		return Def{}, false, fmt.Errorf("materialize embedded agent library: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "agents", name+".md"))
	if err != nil {
		if os.IsNotExist(err) {
			return Def{}, false, nil
		}
		return Def{}, false, fmt.Errorf("read embedded agent %q: %w", name, err)
	}
	front, instructions, err := parseBuiltInAgentMarkdown(raw)
	if err != nil {
		return Def{}, false, fmt.Errorf("parse embedded agent %q: %w", name, err)
	}
	if front.Name != "" && front.Name != name {
		return Def{}, false, fmt.Errorf("embedded agent %q declares name %q", name, front.Name)
	}
	if strings.TrimSpace(instructions) == "" {
		return Def{}, false, fmt.Errorf("embedded agent %q has no instructions", name)
	}
	return Def{
		Name:         name,
		Description:  front.Description,
		SystemPrompt: instructions,
		Model:        front.Model,
		Effort:       front.Effort,
		Tools:        append([]string(nil), front.Tools...),
		Effect:       effect.FromTools(front.Tools),
		Source:       SourceLibrary,
	}, true, nil
}

func defFromBuiltin(a agents.Agent) Def {
	return Def{
		Name:         a.Name,
		Description:  builtinDescription(a),
		SystemPrompt: a.SystemPrompt,
		Model:        a.Model,
		Tools:        append([]string(nil), a.Tools...),
		Cwd:          expandBuiltinCwd(a.DefaultCwd),
		Effect:       effect.FromTools(a.Tools),
		Source:       SourceBuiltin,
	}
}

// expandBuiltinCwd resolves `$VAR` / `${VAR}` tokens in a builtin agent's
// DefaultCwd against the environment. When any referenced var is unset the
// whole cwd is dropped (returns "") instead of erroring: Synthesize then
// omits cwd from the decl, so the agent falls back to the session working
// dir. This mirrors the meta-mode adapter's os.LookupEnv("KITSOKI_REPO")
// gate (internal/app/builtin_meta_modes.go) — the loader's expandMetaCwd
// hard-errors on unset vars, which would make catalog rows unstartable.
func expandBuiltinCwd(s string) string {
	if s == "" {
		return ""
	}
	missing := false
	expanded := os.Expand(s, func(name string) string {
		if name == "$" {
			return "$" // pass `$$` literals through, like expandMetaCwd
		}
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = true
		}
		return v
	})
	if missing {
		return ""
	}
	return expanded
}

// builtinDescription derives a picker one-liner from a builtin agent's system
// prompt (the registry declares no description field): the first line,
// truncated. Good enough for `agent list` / selector rows.
func builtinDescription(a agents.Agent) string {
	line := a.SystemPrompt
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	const max = 140
	if len(line) > max {
		line = strings.TrimSpace(line[:max]) + "…"
	}
	return line
}

// Info is one catalog row for pickers (`kitsoki agent list`, the TUI /agents
// selector, the web agents card section).
type Info struct {
	Name        string        `json:"name"`
	Source      Source        `json:"source"`
	Description string        `json:"description,omitempty"`
	Effect      effect.Effect `json:"effect"`
	// Shadows lists the lower-priority sources that also define this name;
	// the row itself is the search-order winner.
	Shadows []Source `json:"shadows,omitempty"`
}

// List merges the catalog across all sources, deterministic (sorted by name).
// A name defined by several sources appears once — the search-order winner —
// with the shadowed sources reported on Info.Shadows. Malformed project/library
// entries are skipped (Resolve on them reports the real error).
func List(src Sources) ([]Info, error) {
	byName := map[string]*Info{}
	add := func(info Info) {
		if existing, ok := byName[info.Name]; ok {
			existing.Shadows = append(existing.Shadows, info.Source)
			return
		}
		copied := info
		byName[info.Name] = &copied
	}

	// 1. Project dirs, in search order; within a dir a .local.toml shadows
	// the plain file silently (same-source layering, not a collision).
	seenProject := map[string]bool{}
	for _, dir := range src.projectDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
				continue
			}
			base := strings.TrimSuffix(entry.Name(), ".toml")
			base = strings.TrimSuffix(base, ".local")
			if base == "" || seenProject[base] {
				continue
			}
			seenProject[base] = true
			names = append(names, base)
		}
		sort.Strings(names)
		for _, name := range names {
			def, err := Resolve(name, Sources{ProjectDirs: []string{dir}, Materialize: src.Materialize, Builtins: emptyRegistry{}})
			if err != nil {
				continue
			}
			add(Info{Name: name, Source: SourceProject, Description: def.Description, Effect: def.Effect})
		}
	}

	// 2. Embedded library.
	if root, err := src.Materialize.orDefault()(context.Background()); err == nil {
		entries, readErr := os.ReadDir(filepath.Join(root, "agents"))
		if readErr == nil {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
					continue
				}
				names = append(names, strings.TrimSuffix(entry.Name(), ".md"))
			}
			sort.Strings(names)
			for _, name := range names {
				def, ok, defErr := resolveLibraryDef(name, src.Materialize)
				if defErr != nil || !ok {
					continue
				}
				add(Info{Name: name, Source: SourceLibrary, Description: def.Description, Effect: def.Effect})
			}
		}
	} else if !errors.Is(err, baseskills.ErrNotStaged) {
		return nil, fmt.Errorf("materialize embedded agent library: %w", err)
	}

	// 3. Builtins.
	reg := src.builtins()
	for _, name := range reg.List() {
		a, ok := reg.Get(name)
		if !ok {
			continue
		}
		def := defFromBuiltin(a)
		add(Info{Name: name, Source: SourceBuiltin, Description: def.Description, Effect: def.Effect})
	}

	out := make([]Info, 0, len(byName))
	for _, info := range byName {
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// emptyRegistry lets List's per-dir project probe reuse Resolve without
// falling through to builtins for a name the dir doesn't define.
type emptyRegistry struct{}

func (emptyRegistry) Get(string) (agents.Agent, bool) { return agents.Agent{}, false }
func (emptyRegistry) List() []string                  { return nil }
func (emptyRegistry) Register(agents.Agent)           {}
