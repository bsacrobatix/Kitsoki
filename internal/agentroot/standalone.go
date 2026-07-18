// Freestanding (standalone) Codex-agent TOML loading — moved verbatim from
// cmd/kitsoki/agent_launch.go so both `kitsoki agent launch` (thin aliases in
// package main) and agent mode's Resolve share one loader. The TOML dialect,
// extends-chain semantics, embedded-library fallback, and template-overlay
// behavior are unchanged; only the library materializer became an explicit
// parameter (LibraryMaterializer) so callers keep their own test seams.
package agentroot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/baseskills"
)

// StandaloneCodexAgent is a freestanding agent definition parsed from a
// `.codex/agents/*.toml`-style file (or rendered from the embedded agent
// library). Formerly cmd/kitsoki's unexported standaloneCodexAgent.
type StandaloneCodexAgent struct {
	Name                        string
	Description                 string
	DeveloperInstructions       string
	DeveloperInstructionsAppend string
	Extends                     string
	Model                       string
	Effort                      string
	SandboxMode                 string
	// Tools is an explicit tool allowlist (`tools = ["mcp__x__*", ...]`).
	// When declared it wins over the sandbox_mode-derived surface, letting a
	// project overlay keep a packaged agent's tool contract (e.g. pog-driver's
	// graph-only mcp__* set) while overriding other fields.
	Tools      []string
	MCPServers map[string]any
}

// LibraryMaterializer materializes the embedded agent library and returns the
// absolute path of the materialized root (the directory containing `agents/`).
// It is the DI seam formerly held by cmd/kitsoki's package var
// materializeBuiltInAgentLibrary; nil means baseskills.Materialize.
type LibraryMaterializer func(context.Context) (string, error)

func (m LibraryMaterializer) orDefault() LibraryMaterializer {
	if m != nil {
		return m
	}
	return baseskills.Materialize
}

type builtInAgentFrontmatter struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description"`
	Backend     string        `yaml:"backend"`
	Model       string        `yaml:"model"`
	Effort      string        `yaml:"effort"`
	Tools       agentToolList `yaml:"tools"`
}

// agentToolList accepts both frontmatter spellings of a tool list: the YAML
// sequence form and the Claude Code convention of one comma-separated scalar
// (`tools: Bash, Read`), which the embedded agent library is authored in.
type agentToolList []string

func (t *agentToolList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var joined string
		if err := value.Decode(&joined); err != nil {
			return err
		}
		*t = nil
		for _, part := range strings.Split(joined, ",") {
			if part = strings.TrimSpace(part); part != "" {
				*t = append(*t, part)
			}
		}
		return nil
	default:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*t = list
		return nil
	}
}

// DefaultProjectAgentDirs returns the standard freestanding-agent search
// chain: project `.kitsoki/agents` → project `.codex/agents` → user
// `~/.codex/agents`. Within each dir a `<name>.local.toml` shadows
// `<name>.toml` — the exact candidate order ResolveStandaloneAgentFile has
// always used.
func DefaultProjectAgentDirs() []string {
	dirs := []string{
		filepath.Join(".kitsoki", "agents"),
		filepath.Join(".codex", "agents"),
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		dirs = append(dirs, filepath.Join(home, ".codex", "agents"))
	}
	return dirs
}

// standaloneAgentCandidates expands the search dirs into ordered file
// candidates for one agent name (local override first per dir).
func standaloneAgentCandidates(agentName string, dirs []string) []string {
	candidates := make([]string, 0, len(dirs)*2)
	for _, dir := range dirs {
		candidates = append(candidates,
			filepath.Join(dir, agentName+".local.toml"),
			filepath.Join(dir, agentName+".toml"),
		)
	}
	return candidates
}

// ResolveStandaloneAgentFile resolves an agent name (or explicit file /
// template overlay) to a loadable agent TOML path, rendering the embedded
// library to a private temp dir when needed. The returned cleanup func (may
// be nil) removes any rendered temp dir. materialize nil means the real
// embedded library.
func ResolveStandaloneAgentFile(agentName, explicit, template string, materialize LibraryMaterializer) (string, func(), error) {
	if strings.TrimSpace(explicit) != "" {
		path, err := filepath.Abs(explicit)
		return path, nil, err
	}
	if strings.TrimSpace(template) != "" {
		path, err := filepath.Abs(template)
		if err != nil {
			return "", nil, err
		}
		if st, statErr := os.Stat(path); statErr != nil || st.IsDir() {
			if statErr != nil {
				return "", nil, fmt.Errorf("agent template %s: %w", path, statErr)
			}
			return "", nil, fmt.Errorf("agent template %s is a directory", path)
		}
		return renderBuiltInStandaloneCodexAgentWithTemplate(agentName, path, materialize)
	}
	for _, candidate := range standaloneAgentCandidates(agentName, DefaultProjectAgentDirs()) {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			path, absErr := filepath.Abs(candidate)
			if absErr != nil {
				return "", nil, absErr
			}
			// Keep complete legacy definitions usable without requiring the embedded
			// library to have been staged into a source-tree test binary. Partial
			// legacy layers fall through to the isolated embedded-base renderer.
			if _, loadErr := LoadStandaloneCodexAgent(path); loadErr == nil {
				return path, nil, nil
			}
			return renderBuiltInStandaloneCodexAgentWithTemplate(agentName, path, materialize)
		}
	}
	path, cleanup, err := renderBuiltInStandaloneCodexAgent(agentName, materialize)
	if err == nil {
		return path, cleanup, nil
	}
	return "", nil, fmt.Errorf("freestanding agent %q not found in project overrides or the embedded Kitsoki agent library; pass --app for a story agents: entry or --agent-file for a .codex/agents/*.toml file: %w", agentName, err)
}

// renderBuiltInStandaloneCodexAgentWithTemplate resolves a TOML overlay against
// the packaged agent, then writes only the fully resolved result to the private
// launch directory. A template need only contain the fields it changes.
func renderBuiltInStandaloneCodexAgentWithTemplate(agentName, templatePath string, materialize LibraryMaterializer) (string, func(), error) {
	basePath, cleanup, err := renderBuiltInStandaloneCodexAgent(agentName, materialize)
	if err != nil {
		return "", nil, err
	}
	fail := func(err error) (string, func(), error) {
		cleanup()
		return "", nil, err
	}
	base, err := LoadStandaloneCodexAgent(basePath)
	if err != nil {
		return fail(fmt.Errorf("load rendered embedded agent: %w", err))
	}
	overlay, err := LoadStandaloneCodexAgentOverlay(templatePath)
	if err != nil {
		return fail(fmt.Errorf("load agent template %s: %w", templatePath, err))
	}
	resolved := mergeStandaloneCodexAgents(base, overlay)
	resolved.Extends = ""
	if strings.TrimSpace(resolved.Name) != "" && resolved.Name != agentName {
		return fail(fmt.Errorf("agent template %s declares name %q, not %q", templatePath, resolved.Name, agentName))
	}
	if strings.TrimSpace(resolved.Name) == "" {
		resolved.Name = agentName
	}
	path := filepath.Join(filepath.Dir(basePath), agentName+".templated.toml")
	if err := os.WriteFile(path, []byte(renderStandaloneCodexAgentTOML(resolved)), 0o600); err != nil {
		return fail(fmt.Errorf("write rendered templated agent: %w", err))
	}
	return path, cleanup, nil
}

func renderBuiltInStandaloneCodexAgent(agentName string, materialize LibraryMaterializer) (string, func(), error) {
	root, err := materialize.orDefault()(context.Background())
	if err != nil {
		return "", nil, fmt.Errorf("materialize embedded agent library: %w", err)
	}
	sourcePath := filepath.Join(root, "agents", agentName+".md")
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, fmt.Errorf("embedded agent %q not found", agentName)
		}
		return "", nil, fmt.Errorf("read embedded agent %q: %w", agentName, err)
	}
	front, instructions, err := parseBuiltInAgentMarkdown(raw)
	if err != nil {
		return "", nil, fmt.Errorf("parse embedded agent %q: %w", agentName, err)
	}
	if front.Name != "" && front.Name != agentName {
		return "", nil, fmt.Errorf("embedded agent %q declares name %q", agentName, front.Name)
	}
	if strings.TrimSpace(instructions) == "" {
		return "", nil, fmt.Errorf("embedded agent %q has no instructions", agentName)
	}

	dir, err := os.MkdirTemp("", "kitsoki-agent-launch-")
	if err != nil {
		return "", nil, fmt.Errorf("create rendered agent directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, agentName+".toml")
	if err := os.WriteFile(path, []byte(renderBuiltInAgentTOML(agentName, front, instructions)), 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write rendered agent: %w", err)
	}
	return path, cleanup, nil
}

func parseBuiltInAgentMarkdown(raw []byte) (builtInAgentFrontmatter, string, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return builtInAgentFrontmatter{}, "", fmt.Errorf("missing YAML frontmatter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return builtInAgentFrontmatter{}, "", fmt.Errorf("unterminated YAML frontmatter")
	}
	end += 4
	var front builtInAgentFrontmatter
	if err := yaml.Unmarshal([]byte(text[4:end]), &front); err != nil {
		return builtInAgentFrontmatter{}, "", err
	}
	return front, strings.TrimSpace(text[end+5:]), nil
}

// builtInAgentMCPServers returns the MCP server set packaged with an embedded
// library agent. Packaged agents are Studio-MCP contracts, so the default is
// the Studio server; named agents override it, and codeact-worker attaches
// nothing (its server comes from the CodeAct launch mode). Shared by the
// standalone launch render and agent-mode library resolution so
// `agent launch` and `agent:<name>` sessions expose the same tool surface —
// a library agent whose tools name mcp__* families is unusable without them.
func builtInAgentMCPServers(agentName string) map[string]any {
	switch agentName {
	case "pog-driver":
		return map[string]any{
			"kitsoki-agent-launch": map[string]any{
				"command": "kitsoki",
				"args":    []string{"mcp-agent-launch", "--allow-agent", "codeact-worker", "--allow-agent", "pog-driver"},
			},
			"kitsoki-codeact": map[string]any{
				"command": "kitsoki",
				"args":    []string{"mcp-codeact", "--working-dir", ".", "--capabilities-json", `{"fs":true,"vcs":"read"}`},
			},
			"kitsoki-graph": map[string]any{
				"command": "kitsoki",
				"args":    []string{"mcp-graph", "--catalog", "pog/catalog.yaml", "--mode", "propose", "--feedback-sink", "local"},
			},
		}
	case "codeact-worker":
		return nil
	}
	return map[string]any{"kitsoki": map[string]any{"command": "kitsoki", "args": []string{"mcp"}}}
}

func renderBuiltInAgentTOML(agentName string, front builtInAgentFrontmatter, instructions string) string {
	return renderStandaloneCodexAgentTOML(StandaloneCodexAgent{
		Name:                  firstNonEmpty(front.Name, agentName),
		Description:           front.Description,
		Model:                 front.Model,
		Effort:                front.Effort,
		DeveloperInstructions: instructions,
		// The frontmatter tool list rides the rendered TOML so a project
		// overlay inherits the packaged contract instead of the generic
		// sandbox_mode-derived surface.
		Tools: append([]string(nil), front.Tools...),
		// Rendering these servers only for the isolated launch keeps normal
		// Codex sessions untouched.
		MCPServers: builtInAgentMCPServers(agentName),
	})
}

// LoadStandaloneCodexAgent loads a freestanding agent TOML, following its
// extends chain, and requires the merged result to carry
// developer_instructions.
func LoadStandaloneCodexAgent(path string) (StandaloneCodexAgent, error) {
	agent, err := loadStandaloneCodexAgentChain(path, nil)
	if err != nil {
		return StandaloneCodexAgent{}, err
	}
	if strings.TrimSpace(agent.DeveloperInstructions) == "" {
		return StandaloneCodexAgent{}, fmt.Errorf("agent file %s must set developer_instructions", path)
	}
	if strings.TrimSpace(agent.Name) == "" {
		agent.Name = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), ".local")
	}
	return agent, nil
}

// LoadStandaloneCodexAgentOverlay permits a partial TOML layer. Its parent
// chain remains available for compatibility, while the embedded agent supplies
// any fields the layer intentionally leaves blank.
func LoadStandaloneCodexAgentOverlay(path string) (StandaloneCodexAgent, error) {
	return loadStandaloneCodexAgentChain(path, nil)
}

func loadStandaloneCodexAgentChain(path string, seen map[string]bool) (StandaloneCodexAgent, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return StandaloneCodexAgent{}, err
	}
	if seen == nil {
		seen = map[string]bool{}
	}
	if seen[abs] {
		return StandaloneCodexAgent{}, fmt.Errorf("agent extends cycle at %s", abs)
	}
	seen[abs] = true
	defer delete(seen, abs)
	raw, err := os.ReadFile(abs)
	if err != nil {
		return StandaloneCodexAgent{}, fmt.Errorf("read agent file: %w", err)
	}
	agent, err := parseStandaloneCodexAgentTOML(string(raw))
	if err != nil {
		return StandaloneCodexAgent{}, fmt.Errorf("parse agent file %s: %w", abs, err)
	}
	if strings.TrimSpace(agent.Extends) != "" {
		parentPath := expandStandaloneAgentPath(agent.Extends, filepath.Dir(abs))
		parent, parentErr := loadStandaloneCodexAgentChain(parentPath, seen)
		if parentErr != nil {
			return StandaloneCodexAgent{}, fmt.Errorf("load parent agent %s: %w", parentPath, parentErr)
		}
		agent = mergeStandaloneCodexAgents(parent, agent)
	}
	return agent, nil
}

func expandStandaloneAgentPath(path, baseDir string) string {
	path = os.ExpandEnv(strings.TrimSpace(path))
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	return filepath.Clean(path)
}

func mergeStandaloneCodexAgents(parent, child StandaloneCodexAgent) StandaloneCodexAgent {
	merged := parent
	if child.Name != "" {
		merged.Name = child.Name
	}
	if child.Description != "" {
		merged.Description = child.Description
	}
	if child.DeveloperInstructions != "" {
		merged.DeveloperInstructions = child.DeveloperInstructions
	}
	if child.Model != "" {
		merged.Model = child.Model
	}
	if child.Effort != "" {
		merged.Effort = child.Effort
	}
	if child.SandboxMode != "" {
		merged.SandboxMode = child.SandboxMode
	}
	if len(child.Tools) > 0 {
		merged.Tools = append([]string(nil), child.Tools...)
	}
	if child.DeveloperInstructionsAppend != "" {
		merged.DeveloperInstructions = strings.TrimSpace(merged.DeveloperInstructions) + "\n\n" + strings.TrimSpace(child.DeveloperInstructionsAppend)
	}
	merged.Extends = child.Extends
	if parent.MCPServers != nil || child.MCPServers != nil {
		merged.MCPServers = map[string]any{}
		for name, server := range parent.MCPServers {
			merged.MCPServers[name] = cloneStandaloneMCPServer(server)
		}
		for name, server := range child.MCPServers {
			parentServer, hasParent := merged.MCPServers[name]
			if hasParent {
				merged.MCPServers[name] = mergeStandaloneMCPServer(parentServer, server)
			} else {
				merged.MCPServers[name] = cloneStandaloneMCPServer(server)
			}
		}
	}
	return merged
}

func cloneStandaloneMCPServer(server any) any {
	fields, ok := server.(map[string]any)
	if !ok {
		return server
	}
	clone := make(map[string]any, len(fields))
	for key, value := range fields {
		clone[key] = value
	}
	return clone
}

func mergeStandaloneMCPServer(parent, child any) any {
	merged, ok := cloneStandaloneMCPServer(parent).(map[string]any)
	if !ok {
		return cloneStandaloneMCPServer(child)
	}
	childFields, ok := child.(map[string]any)
	if !ok {
		return cloneStandaloneMCPServer(child)
	}
	for key, value := range childFields {
		merged[key] = value
	}
	return merged
}

func renderStandaloneCodexAgentTOML(agent StandaloneCodexAgent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name = %s\n", TOMLString(agent.Name))
	if strings.TrimSpace(agent.Description) != "" {
		fmt.Fprintf(&b, "description = %s\n", TOMLString(agent.Description))
	}
	if strings.TrimSpace(agent.Model) != "" {
		fmt.Fprintf(&b, "model = %s\n", TOMLString(agent.Model))
	}
	if strings.TrimSpace(agent.Effort) != "" {
		fmt.Fprintf(&b, "model_reasoning_effort = %s\n", TOMLString(agent.Effort))
	}
	if strings.TrimSpace(agent.SandboxMode) != "" {
		fmt.Fprintf(&b, "sandbox_mode = %s\n", TOMLString(agent.SandboxMode))
	}
	if len(agent.Tools) > 0 {
		fmt.Fprintf(&b, "tools = %s\n", tomlStringArray(agent.Tools))
	}
	fmt.Fprintf(&b, "developer_instructions = %s\n", TOMLString(agent.DeveloperInstructions))
	for _, name := range sortedStandaloneMCPServerNames(agent.MCPServers) {
		server, ok := agent.MCPServers[name].(map[string]any)
		if !ok {
			continue
		}
		b.WriteString("\n[mcp_servers." + name + "]\n")
		if command, ok := server["command"].(string); ok && strings.TrimSpace(command) != "" {
			fmt.Fprintf(&b, "command = %s\n", TOMLString(command))
		}
		if args, ok := stringSliceFromAny(server["args"]); ok {
			fmt.Fprintf(&b, "args = %s\n", tomlStringArray(args))
		}
		if cwd, ok := server["cwd"].(string); ok && strings.TrimSpace(cwd) != "" {
			fmt.Fprintf(&b, "cwd = %s\n", TOMLString(cwd))
		}
	}
	return b.String()
}

func sortedStandaloneMCPServerNames(servers map[string]any) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func parseStandaloneCodexAgentTOML(src string) (StandaloneCodexAgent, error) {
	var agent StandaloneCodexAgent
	agent.MCPServers = map[string]any{}
	section := ""
	currentServer := ""
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(stripLaunchTOMLComment(lines[i]))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			currentServer = ""
			if strings.HasPrefix(section, "mcp_servers.") {
				currentServer = strings.TrimPrefix(section, "mcp_servers.")
				if currentServer == "" || strings.Contains(currentServer, ".") {
					return StandaloneCodexAgent{}, fmt.Errorf("unsupported section [%s]", section)
				}
				if _, ok := agent.MCPServers[currentServer]; !ok {
					agent.MCPServers[currentServer] = map[string]any{}
				}
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return StandaloneCodexAgent{}, fmt.Errorf("line %d: expected key = value", i+1)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, `"""`) {
			var text string
			text, i = collectLaunchTOMLMultilineString(lines, i, val)
			val = text
		}
		if currentServer != "" {
			server := agent.MCPServers[currentServer].(map[string]any)
			switch key {
			case "command":
				server["command"] = parseLaunchTOMLString(val)
			case "args":
				server["args"] = parseLaunchTOMLStringArray(val)
			case "cwd":
				server["cwd"] = parseLaunchTOMLString(val)
			default:
				return StandaloneCodexAgent{}, fmt.Errorf("line %d: unsupported mcp server key %q", i+1, key)
			}
			continue
		}
		if section != "" {
			return StandaloneCodexAgent{}, fmt.Errorf("line %d: unsupported section [%s]", i+1, section)
		}
		switch key {
		case "name":
			agent.Name = parseLaunchTOMLString(val)
		case "description":
			agent.Description = parseLaunchTOMLString(val)
		case "developer_instructions":
			agent.DeveloperInstructions = parseLaunchTOMLString(val)
		case "developer_instructions_append":
			agent.DeveloperInstructionsAppend = parseLaunchTOMLString(val)
		case "extends":
			agent.Extends = parseLaunchTOMLString(val)
		case "model":
			agent.Model = parseLaunchTOMLString(val)
		case "model_reasoning_effort":
			agent.Effort = parseLaunchTOMLString(val)
		case "sandbox_mode":
			agent.SandboxMode = parseLaunchTOMLString(val)
		case "tools":
			agent.Tools = parseLaunchTOMLStringArray(val)
		default:
			// Ignore Codex-agent fields that are useful to Codex itself but not
			// needed for launch planning, such as description variants.
		}
	}
	if len(agent.MCPServers) == 0 {
		agent.MCPServers = nil
	}
	return agent, nil
}

func stripLaunchTOMLComment(line string) string {
	inString := false
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inString {
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
			continue
		}
		if r == '#' && !inString {
			return line[:i]
		}
	}
	return line
}

func collectLaunchTOMLMultilineString(lines []string, start int, firstVal string) (string, int) {
	rest := strings.TrimPrefix(firstVal, `"""`)
	if end := strings.Index(rest, `"""`); end >= 0 {
		return rest[:end], start
	}
	var b strings.Builder
	b.WriteString(rest)
	for i := start + 1; i < len(lines); i++ {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		line := lines[i]
		if end := strings.Index(line, `"""`); end >= 0 {
			b.WriteString(line[:end])
			return b.String(), i
		}
		b.WriteString(line)
	}
	return b.String(), len(lines) - 1
}

func parseLaunchTOMLString(val string) string {
	val = strings.TrimSpace(val)
	if strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`) && len(val) >= 2 {
		val = strings.TrimSuffix(strings.TrimPrefix(val, `"`), `"`)
		repl := strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n", `\t`, "\t", `\r`, "\r")
		return repl.Replace(val)
	}
	return val
}

func parseLaunchTOMLStringArray(val string) []string {
	val = strings.TrimSpace(val)
	if !strings.HasPrefix(val, "[") || !strings.HasSuffix(val, "]") {
		return nil
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(val, "["), "]"))
	if inner == "" {
		return nil
	}
	var out []string
	var b strings.Builder
	inString := false
	escaped := false
	for _, r := range inner {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\' && inString:
			escaped = true
		case r == '"':
			inString = !inString
		case r == ',' && !inString:
			out = append(out, parseLaunchTOMLString(strings.TrimSpace(b.String())))
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if strings.TrimSpace(b.String()) != "" {
		out = append(out, parseLaunchTOMLString(strings.TrimSpace(b.String())))
	}
	return out
}

func stringSliceFromAny(v any) ([]string, bool) {
	switch typed := v.(type) {
	case []string:
		return typed, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

// TOMLString renders s as a double-quoted TOML string (formerly cmd/kitsoki's
// launchTOMLString; the Codex plan builder still uses it via a thin alias).
func TOMLString(s string) string {
	repl := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\t", `\t`,
		"\r", `\r`,
	)
	return `"` + repl.Replace(s) + `"`
}

func tomlStringArray(xs []string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = TOMLString(x)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
