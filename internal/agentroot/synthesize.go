package agentroot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/app"
	"kitsoki/internal/effect"
	"kitsoki/internal/host"
)

// RoomName is the single top-level room every synthesized agent root carries.
// It is the chats/room key an agent session binds under:
// (app_id: "agent:<name>", room: "agent", scope_key).
const RoomName = "agent"

// PolicyCheckFunc matches host.AgentLaunchPolicy.Check — the injectable seam
// for the synthesis-time launch-policy preflight.
type PolicyCheckFunc func(ctx context.Context, verb, agentName, workingDir string) (host.AgentLaunchDecision, error)

// Options configures Synthesize. The zero value synthesizes against "." with
// no launch policy (Enabled: false allows everything, matching the launch
// path's checkLaunchPolicy) and the default schema cache dir.
type Options struct {
	// WorkingDir is the directory the session's agent dispatches run in. It
	// becomes the synthesized `workdir` world default (the workbench dispatch
	// reads `{{ world.workdir }}`) and the launch-policy preflight target.
	// Empty means the current directory.
	WorkingDir string
	// LaunchPolicy is the deterministic preflight gate applied to
	// write/external agents at synthesis time (design §2.1). The zero value
	// (Enabled: false) allows everything. In-session dispatches are gated
	// again by the policy installed on the orchestrator context plus the
	// write-mode gate — this preflight only fails fast before a session
	// starts.
	LaunchPolicy host.AgentLaunchPolicy
	// CheckPolicy overrides the preflight check (tests inject fakes). Nil
	// means LaunchPolicy.Check.
	CheckPolicy PolicyCheckFunc
	// SchemaDir is the directory the synthesized acceptance schema is written
	// to (host.agent.task requires a schema file on disk). Empty means a
	// content-addressed dir under the user cache
	// (~/.cache/kitsoki/agentroot/<version>). Tests pass t.TempDir().
	SchemaDir string
}

// PolicyDeniedError is returned by Synthesize when the launch-policy
// preflight denies a write/external agent's working dir. It carries the
// standard auditable decision so callers can render it exactly like
// `kitsoki agent launch` renders a denied plan.
type PolicyDeniedError struct {
	Decision host.AgentLaunchDecision
	Err      error
}

func (e *PolicyDeniedError) Error() string {
	return fmt.Sprintf("agent mode preflight: %v; run from a managed capsule workspace (scripts/dev-workspace.sh create / kitsoki capsule) or extend agent_launch_policy allowed_roots", e.Err)
}

func (e *PolicyDeniedError) Unwrap() error { return e.Err }

// validSynthesizedEfforts mirrors internal/app's validateEffort enum. A
// resolved definition may carry a backend-native effort outside this set
// (e.g. codex "minimal"); it is dropped from the synthesized AgentDecl rather
// than failing the load — effort selection then falls to the harness profile,
// the same precedence every agent call already honors.
var validSynthesizedEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// Synthesize builds the one-room AppDef for a resolved agent definition and
// runs it through the normal loader (app.LoadBytes → resolveAgentDecls →
// expandWorkbenches / expandOffRampCaptures → validateDef), so every
// downstream pass sees exactly the shape a hand-written story would produce:
//
//   - write agents desugar via a `workbench:` block — the proven
//     landing-room shape (write-mode gate, off-ramp Q&A, `<room>_capture`,
//     on_enter host.agent.task), zero new permission surface;
//   - read|pure agents get the conversational off-ramp shape directly
//     (`agent_off_ramp: {agent: <name>, capture_free_text: true}`), since
//     expandOneWorkbench correctly rejects read-only agents;
//   - external agents ALSO get the conversational shape: although
//     expandOneWorkbench accepts effect external, the workbench macro always
//     sets write_mode: read_only, and validateWriteMode hard-rejects that
//     posture over an external agent (internal/app/loader.go's
//     "write_mode: read_only contradicts agent ... external_side_effect:
//     true" check). Conversational is the conservative shape — the agent
//     keeps its persona and MCP surface through the off-ramp converse lane.
//
// App ID is "agent:<name>"; version is a content hash of the resolved
// definition so resume detects definition drift the same way story edits do.
func Synthesize(def Def, opts Options) (*app.AppDef, error) {
	if !ValidName(def.Name) {
		return nil, fmt.Errorf("agent name %q is invalid (must be non-empty with no path separators)", def.Name)
	}
	if strings.TrimSpace(def.SystemPrompt) == "" {
		return nil, fmt.Errorf("agent %q resolved with an empty system prompt", def.Name)
	}
	version, err := contentVersion(def)
	if err != nil {
		return nil, err
	}

	workingDir := strings.TrimSpace(opts.WorkingDir)
	if workingDir == "" {
		workingDir = "."
	}
	if abs, absErr := filepath.Abs(workingDir); absErr == nil {
		workingDir = abs
	}

	// Only write agents take the workbench shape; see the function comment
	// for why external agents fall back to the conversational off-ramp.
	workbench := def.Effect == effect.Write

	// Launch-policy preflight (write/external): fail fast with the auditable
	// decision before any session/room exists. Read-only agents never
	// dispatch mutating work, so they are exempt by design. External agents
	// keep the preflight even on the conversational shape — their tool
	// surface is the most privileged tier.
	if def.Effect == effect.Write || def.Effect == effect.External {
		check := opts.CheckPolicy
		if check == nil {
			policy := opts.LaunchPolicy.Normalized()
			check = policy.Check
		}
		decision, checkErr := check(context.Background(), "agent.mode", def.Name, workingDir)
		if checkErr != nil {
			return nil, &PolicyDeniedError{Decision: decision, Err: checkErr}
		}
	}

	doc := map[string]any{
		"app": map[string]any{
			"id":      Scheme + def.Name,
			"version": version,
			"title":   firstNonEmpty(def.Description, def.Name+" — agent session"),
		},
		"root": RoomName,
	}

	agentDecl := map[string]any{
		"system_prompt": def.SystemPrompt,
	}
	if strings.TrimSpace(def.Model) != "" {
		agentDecl["model"] = def.Model
	}
	if validSynthesizedEfforts[strings.TrimSpace(def.Effort)] {
		agentDecl["effort"] = strings.TrimSpace(def.Effort)
	}
	if strings.TrimSpace(def.Cwd) != "" {
		agentDecl["cwd"] = def.Cwd
	}
	if len(def.MCPServers) > 0 {
		agentDecl["mcp"] = map[string]any{"servers": def.MCPServers}
	}

	room := map[string]any{
		"description": firstNonEmpty(def.Description, fmt.Sprintf("Agent session: %s", def.Name)),
	}

	if workbench {
		// WS vocabulary: workbench: requires the dispatched agent to declare
		// toolbox: + effect: (internal/app/workbench.go invariants). The
		// declared effect is the resolved one, so it always agrees with the
		// tool-surface join.
		doc["world"] = map[string]any{
			"workdir": map[string]any{"type": "string", "default": workingDir},
		}
		doc["toolboxes"] = map[string]any{
			"agent_mode_toolbox": map[string]any{
				"tools":  toolsOrDefault(def.Tools),
				"effect": string(def.Effect),
			},
		}
		agentDecl["toolbox"] = "agent_mode_toolbox"

		schemaPath, schemaErr := materializeAcceptanceSchema(opts.SchemaDir, version)
		if schemaErr != nil {
			return nil, schemaErr
		}
		room["workbench"] = map[string]any{
			"agent":             def.Name,
			"prompt":            workbenchPrompt(def),
			"acceptance_schema": schemaPath,
		}
		room["view"] = []any{
			map[string]any{
				"prose": fmt.Sprintf("Agent workbench — describe what you want %s to do, in your own words. Work runs read-only until you grant write mode.", def.Name),
			},
		}
	} else {
		if len(def.Tools) > 0 {
			agentDecl["tools"] = append([]string(nil), def.Tools...)
		}
		// The conversational lane has no world.workdir threading — the
		// synthesized decl's cwd is the only channel to the converse dispatch.
		// External agents just passed the preflight against workingDir, so that
		// (possibly auto-provisioned capsule) path must be where they actually
		// run; leaving cwd to def.Cwd or the process cwd would re-enter the
		// protected root the policy already steered away from. Mirrors the
		// workbench precedence, where world.workdir wins over the declared cwd.
		if def.Effect == effect.External {
			agentDecl["cwd"] = workingDir
		}
		// Conversational shape (read/pure/external): capture_free_text makes
		// the room's off-ramp the deterministic free-text sink
		// (expandOffRampCaptures synthesizes the `<room>_discuss` intent +
		// default_intent).
		room["agent_off_ramp"] = map[string]any{
			"agent":             def.Name,
			"capture_free_text": true,
		}
		room["view"] = []any{
			map[string]any{
				"prose": fmt.Sprintf("Chat with %s.", def.Name),
			},
		}
	}

	doc["agents"] = map[string]any{def.Name: agentDecl}
	doc["states"] = map[string]any{RoomName: room}

	raw, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("agent mode: marshal synthesized root: %w", err)
	}
	appDef, err := app.LoadBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("agent mode: synthesized root for %q failed to load: %w", def.Name, err)
	}
	return appDef, nil
}

// toolsOrDefault guards against a write/external definition that somehow lost
// its tool list — the workbench toolbox must be non-empty.
func toolsOrDefault(tools []string) []string {
	if len(tools) > 0 {
		return append([]string(nil), tools...)
	}
	return append([]string(nil), projectWriteToolbox...)
}

// workbenchPrompt is the inline dispatch prompt (host.agent.task's
// context.prompt accepts inline text, mirroring prompts/landing.md's shape).
func workbenchPrompt(def Def) string {
	return fmt.Sprintf(`You are %s, dispatched from an agent session.

The operator's request:

> {{ args.request }}

Carry out the request in the working directory. You start read-only: attempt
any change you need and the runtime will surface it to the operator for a
write-mode grant; if the grant is declined or you are headless, stay
read-only and describe precisely what you would change instead.

When done, submit the close-out note through the validator tool: a one-line
summary (required) plus optional details.`, def.Name)
}

// acceptanceSchemaJSON is the minimal permissive close-out contract every
// synthesized workbench dispatch uses — the same shape as dev-story's
// schemas/landing-note.json floor: `summary` required, everything else open.
const acceptanceSchemaJSON = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "agent_mode_note",
  "description": "Close-out note for an agent-mode workbench dispatch. Only summary is required; the schema stays open so the agent may attach anything.",
  "type": "object",
  "required": ["summary"],
  "properties": {
    "summary": {
      "type": "string",
      "minLength": 1,
      "description": "One line: what the agent did or found this turn."
    },
    "details": {
      "type": "string",
      "description": "Optional longer markdown narrative of the exploration / change."
    }
  },
  "additionalProperties": true
}
`

// materializeAcceptanceSchema writes the acceptance schema to disk (the
// host.agent.task validator stats the schema file at dispatch time) and
// returns its absolute path. dir empty means a content-addressed cache dir;
// writes are idempotent.
func materializeAcceptanceSchema(dir, version string) (string, error) {
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("agent mode: resolve schema cache dir: %w", err)
		}
		dir = filepath.Join(base, "kitsoki", "agentroot", version)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("agent mode: create schema dir: %w", err)
	}
	path := filepath.Join(dir, "agent-mode-note.json")
	if existing, err := os.ReadFile(path); err == nil && string(existing) == acceptanceSchemaJSON {
		return path, nil
	}
	if err := os.WriteFile(path, []byte(acceptanceSchemaJSON), 0o644); err != nil {
		return "", fmt.Errorf("agent mode: write acceptance schema: %w", err)
	}
	return path, nil
}

// contentVersion hashes the resolved definition into the synthesized app
// version, so a resumed session surfaces definition drift exactly like a
// story edit does (app_version mismatch). JSON marshaling sorts map keys, so
// the hash is deterministic for a given definition.
func contentVersion(def Def) (string, error) {
	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("agent mode: hash agent definition: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:12], nil
}
