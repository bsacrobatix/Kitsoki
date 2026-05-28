// oracle.go — implements `kitsoki oracle <verb>` CLI commands (Phase 6).
//
// Five subcommands correspond 1:1 to the five oracle handlers:
//
//	kitsoki oracle extract  --schema <path> --input <text|->  [--resolvers-yaml <path>]  [--agent NAME]
//	kitsoki oracle decide   --schema <path> --prompt <path|-> [--agent NAME] [--args-json '{…}'] [--validator-cmd '<argv>']
//	kitsoki oracle ask      --prompt <path|-> --agent NAME    [--working-dir <path>] [--schema <path>] [--args-json '{…}']
//	kitsoki oracle task     --agent NAME     --working-dir <path> --acceptance-schema <path> [--acceptance-cmd '<argv>'] [--context-prompt <path>]
//	kitsoki oracle converse --chat-id <uuid> --message '...' [--agent NAME] [--permission-mode ask|bypassPermissions|denyAll] [--background]
//
// Streaming output:
//   - When stdout is not a TTY: line-delimited JSON (same format as the oracle
//     handler Result.Data map).
//   - When stdout is a TTY: plain human-readable text.
//
// Trace continuity:
//   - Reads KITSOKI_SESSION_ID from the environment and sets it for child
//     subprocesses; accepts --parent-session <id> as an explicit override.
//   - When neither is set, a fresh session ID is minted and printed to stderr.
//
// If KITSOKI_ORACLE_SOCK is set the command auto-delegates to the unix-socket
// JSON-RPC server instead of calling the handler in-process (§5.2).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/host"
)

// oracleSessionIDEnv is the environment variable that carries the parent
// session ID into child subprocesses.
const oracleSessionIDEnv = "KITSOKI_SESSION_ID"

// oracleCmd returns the top-level `kitsoki oracle` command with five subcommands.
func oracleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "oracle",
		Short: "Call a kitsoki oracle verb from the command line (Phase 6)",
		Long: `kitsoki oracle provides command-line access to the five oracle verbs.

Subcommands:
  extract  — tiered resolver: synonyms → slot_template → llm
  decide   — LLM reasoning verdict; schema required; no mutation tools
  ask      — read-only LLM inspection; prose or typed JSON output
  task     — agentic LLM worker; may mutate files; acceptance loop
  converse — free-form conversation with optional chat transcript

Trace continuity:
  When KITSOKI_SESSION_ID is set (or --parent-session is passed), events
  from this oracle call are nested under that session in the journal.
  When neither is set, a fresh session ID is minted.

Auto-delegation:
  When KITSOKI_ORACLE_SOCK is set, the command delegates to the running
  oracle-serve daemon via JSON-RPC instead of invoking the handler in-process.
  This is transparent to callers.`,
	}

	cmd.AddCommand(oracleExtractCmd())
	cmd.AddCommand(oracleDecideCmd())
	cmd.AddCommand(oracleAskCmd())
	cmd.AddCommand(oracleTaskCmd())
	cmd.AddCommand(oracleConverseCmd())

	return cmd
}

// oracleExtractCmd implements `kitsoki oracle extract`.
func oracleExtractCmd() *cobra.Command {
	var (
		schemaPath    string
		inputStr      string
		resolversYAML string
		agentName     string
		parentSession string
	)

	cmd := &cobra.Command{
		Use:   "extract",
		Short: "Extract a typed JSON value from free text (tiered resolver)",
		Long: `Runs the extract handler: synonyms → slot_template → llm.

--input may be literal text or '-' to read from stdin.
--resolvers-yaml is a quick path for a single synonyms file (single-tier
  resolver). For multi-tier usage, author the full resolvers: list in a
  context YAML and invoke the handler via a story effect.

Streaming: line-delimited JSON on non-TTY stdout; plain text on TTY.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := injectSessionID(context.Background(), parentSession)
			// M9: read stdin before socket delegation so both paths receive the
			// resolved content rather than the raw "-" sentinel.
			input, err := readInputArg(inputStr)
			if err != nil {
				return err
			}
			if sockPath := os.Getenv("KITSOKI_ORACLE_SOCK"); sockPath != "" {
				return delegateToSocket(ctx, cmd.OutOrStdout(), sockPath, "oracle.extract", buildExtractArgs(input, schemaPath, resolversYAML, agentName, parentSession))
			}
			callArgs := buildExtractArgs(input, schemaPath, resolversYAML, agentName, parentSession)
			res, handlerErr := host.OracleExtractHandler(ctx, callArgs)
			if handlerErr != nil {
				return handlerErr
			}
			return writeOracleResult(cmd.OutOrStdout(), res)
		},
	}

	cmd.Flags().StringVar(&schemaPath, "schema", "", "path to the JSON schema for the output (required)")
	cmd.Flags().StringVar(&inputStr, "input", "", "input text or '-' to read from stdin (required)")
	cmd.Flags().StringVar(&resolversYAML, "resolvers-yaml", "", "synonyms YAML file (single-tier resolver shortcut)")
	cmd.Flags().StringVar(&agentName, "agent", "", "agent name for the LLM resolver tier")
	cmd.Flags().StringVar(&parentSession, "parent-session", "", "parent session ID for trace continuity (default: $KITSOKI_SESSION_ID)")

	_ = cmd.MarkFlagRequired("schema")
	_ = cmd.MarkFlagRequired("input")

	return cmd
}

// oracleDecideCmd implements `kitsoki oracle decide`.
func oracleDecideCmd() *cobra.Command {
	var (
		schemaPath    string
		promptPath    string
		agentName     string
		argsJSON      string
		validatorCmd  string
		parentSession string
	)

	cmd := &cobra.Command{
		Use:   "decide",
		Short: "Obtain a typed LLM reasoning verdict (schema required, no mutation)",
		Long: `Runs the decide handler: LLM judgment with mandatory schema.

--prompt may be a file path or '-' to read the prompt from stdin.
--args-json passes template variables as a JSON object.
--validator-cmd is a read-only validator command (same sandbox as the
  validator: block in an app.yaml decide effect).

Streaming: line-delimited JSON on non-TTY stdout; plain text on TTY.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := injectSessionID(context.Background(), parentSession)
			if sockPath := os.Getenv("KITSOKI_ORACLE_SOCK"); sockPath != "" {
				callArgs, err := buildDecideArgs(promptPath, schemaPath, agentName, argsJSON, validatorCmd, parentSession)
				if err != nil {
					return err
				}
				return delegateToSocket(ctx, cmd.OutOrStdout(), sockPath, "oracle.decide", callArgs)
			}
			callArgs, err := buildDecideArgs(promptPath, schemaPath, agentName, argsJSON, validatorCmd, parentSession)
			if err != nil {
				return err
			}
			res, handlerErr := host.OracleDecideHandler(ctx, callArgs)
			if handlerErr != nil {
				return handlerErr
			}
			return writeOracleResult(cmd.OutOrStdout(), res)
		},
	}

	cmd.Flags().StringVar(&schemaPath, "schema", "", "path to the JSON schema the verdict must conform to (required)")
	cmd.Flags().StringVar(&promptPath, "prompt", "", "prompt file path or '-' for stdin (required)")
	cmd.Flags().StringVar(&agentName, "agent", "", "agent name for system prompt / model / tools")
	cmd.Flags().StringVar(&argsJSON, "args-json", "", "JSON object of template variables for the prompt")
	cmd.Flags().StringVar(&validatorCmd, "validator-cmd", "", "read-only validator command argv string")
	cmd.Flags().StringVar(&parentSession, "parent-session", "", "parent session ID for trace continuity (default: $KITSOKI_SESSION_ID)")

	_ = cmd.MarkFlagRequired("schema")
	_ = cmd.MarkFlagRequired("prompt")

	return cmd
}

// oracleAskCmd implements `kitsoki oracle ask`.
func oracleAskCmd() *cobra.Command {
	var (
		promptPath    string
		agentName     string
		workingDir    string
		schemaPath    string
		argsJSON      string
		parentSession string
	)

	cmd := &cobra.Command{
		Use:   "ask",
		Short: "Ask the LLM a question with read-only tools; returns prose (or typed JSON)",
		Long: `Runs the ask handler: read-only LLM inspection.

--prompt may be a file path or '-' to read the prompt from stdin.
When --schema is supplied, the LLM also calls a submit MCP tool and the
result includes a 'submitted' field alongside 'stdout'.

Streaming: line-delimited JSON on non-TTY stdout; plain text on TTY.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := injectSessionID(context.Background(), parentSession)
			if sockPath := os.Getenv("KITSOKI_ORACLE_SOCK"); sockPath != "" {
				callArgs, err := buildAskArgs(promptPath, agentName, workingDir, schemaPath, argsJSON, parentSession)
				if err != nil {
					return err
				}
				return delegateToSocket(ctx, cmd.OutOrStdout(), sockPath, "oracle.ask", callArgs)
			}
			callArgs, err := buildAskArgs(promptPath, agentName, workingDir, schemaPath, argsJSON, parentSession)
			if err != nil {
				return err
			}
			res, handlerErr := host.OracleAskHandler(ctx, callArgs)
			if handlerErr != nil {
				return handlerErr
			}
			return writeOracleResult(cmd.OutOrStdout(), res)
		},
	}

	cmd.Flags().StringVar(&promptPath, "prompt", "", "prompt file path or '-' for stdin (required)")
	cmd.Flags().StringVar(&agentName, "agent", "", "agent name for system prompt / model / tools")
	cmd.Flags().StringVar(&workingDir, "working-dir", "", "working directory for the LLM subprocess")
	cmd.Flags().StringVar(&schemaPath, "schema", "", "optional JSON schema; when set, LLM must call submit()")
	cmd.Flags().StringVar(&argsJSON, "args-json", "", "JSON object of template variables for the prompt")
	cmd.Flags().StringVar(&parentSession, "parent-session", "", "parent session ID for trace continuity (default: $KITSOKI_SESSION_ID)")

	_ = cmd.MarkFlagRequired("prompt")

	return cmd
}

// oracleTaskCmd implements `kitsoki oracle task`.
func oracleTaskCmd() *cobra.Command {
	var (
		agentName        string
		workingDir       string
		acceptanceSchema string
		acceptanceCmd    string
		contextPrompt    string
		parentSession    string
	)

	cmd := &cobra.Command{
		Use:   "task",
		Short: "Run an agentic LLM task (may mutate files; acceptance loop until done)",
		Long: `Runs the task handler: agentic LLM with full tool access.

The agent may use Edit, Write, Bash, and other mutation tools within the
declared --working-dir. The acceptance loop runs until the LLM calls
submit() with a payload that passes the --acceptance-schema (and optionally
the --acceptance-cmd verifier).

Streaming: line-delimited JSON on non-TTY stdout; plain text on TTY.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := injectSessionID(context.Background(), parentSession)
			callArgs := buildTaskArgs(agentName, workingDir, acceptanceSchema, acceptanceCmd, contextPrompt, parentSession)
			if sockPath := os.Getenv("KITSOKI_ORACLE_SOCK"); sockPath != "" {
				return delegateToSocket(ctx, cmd.OutOrStdout(), sockPath, "oracle.task", callArgs)
			}
			res, handlerErr := host.OracleTaskHandler(ctx, callArgs)
			if handlerErr != nil {
				return handlerErr
			}
			return writeOracleResult(cmd.OutOrStdout(), res)
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "agent name (required; must declare mutation-capable tools)")
	cmd.Flags().StringVar(&workingDir, "working-dir", "", "working directory for the agent subprocess (required)")
	cmd.Flags().StringVar(&acceptanceSchema, "acceptance-schema", "", "path to JSON schema for the submit() payload (required)")
	cmd.Flags().StringVar(&acceptanceCmd, "acceptance-cmd", "", "verifier command run after schema passes; non-zero exit triggers retry")
	cmd.Flags().StringVar(&contextPrompt, "context-prompt", "", "initial prompt / task description for the agent")
	cmd.Flags().StringVar(&parentSession, "parent-session", "", "parent session ID for trace continuity (default: $KITSOKI_SESSION_ID)")

	_ = cmd.MarkFlagRequired("agent")
	_ = cmd.MarkFlagRequired("working-dir")
	_ = cmd.MarkFlagRequired("acceptance-schema")

	return cmd
}

// oracleConverseCmd implements `kitsoki oracle converse`.
func oracleConverseCmd() *cobra.Command {
	var (
		chatID         string
		message        string
		agentName      string
		permissionMode string
		background     bool
		parentSession  string
	)

	cmd := &cobra.Command{
		Use:   "converse",
		Short: "Hold a free-form conversation with optional persistent chat transcript",
		Long: `Runs the converse handler: stateful conversation with an agent.

When --chat-id is provided and a ChatStore is available, the conversation is
persisted and subsequent calls with the same chat-id resume the transcript.

--permission-mode controls mutation access: ask | bypassPermissions | denyAll.
--background is a hint to the caller; the handler runs normally and the
orchestrator handles background scheduling.

Streaming: line-delimited JSON on non-TTY stdout; plain text on TTY.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := injectSessionID(context.Background(), parentSession)
			callArgs := map[string]any{
				"question": message,
			}
			if chatID != "" {
				callArgs["chat_id"] = chatID
			}
			if agentName != "" {
				callArgs["agent"] = agentName
			}
			if permissionMode != "" {
				callArgs["permission_mode"] = permissionMode
			}
			if parentSession != "" {
				callArgs["session_id"] = parentSession
			}
			_ = background

			if sockPath := os.Getenv("KITSOKI_ORACLE_SOCK"); sockPath != "" {
				return delegateToSocket(ctx, cmd.OutOrStdout(), sockPath, "oracle.converse", callArgs)
			}
			res, handlerErr := host.OracleConverseHandler(ctx, callArgs)
			if handlerErr != nil {
				return handlerErr
			}
			return writeOracleResult(cmd.OutOrStdout(), res)
		},
	}

	cmd.Flags().StringVar(&chatID, "chat-id", "", "chat ID for transcript persistence")
	cmd.Flags().StringVar(&message, "message", "", "message to send to the agent (required)")
	cmd.Flags().StringVar(&agentName, "agent", "", "agent name for system prompt / model / tools")
	cmd.Flags().StringVar(&permissionMode, "permission-mode", "", "permission mode: ask|bypassPermissions|denyAll (default: bypassPermissions)")
	cmd.Flags().BoolVar(&background, "background", false, "no-op under CLI; use this flag through host.oracle.converse in app YAML for orchestrator-backed background jobs")
	cmd.Flags().StringVar(&parentSession, "parent-session", "", "parent session ID for trace continuity (default: $KITSOKI_SESSION_ID)")

	_ = cmd.MarkFlagRequired("message")

	return cmd
}

// ── Arg builders ─────────────────────────────────────────────────────────────

func buildExtractArgs(input, schemaPath, resolversYAML, agentName, parentSession string) map[string]any {
	m := map[string]any{
		"input":  input,
		"schema": schemaPath,
	}
	if resolversYAML != "" {
		m["resolvers"] = []any{
			map[string]any{"synonyms": resolversYAML},
			map[string]any{"llm": map[string]any{"agent": agentName}},
		}
	} else if agentName != "" {
		m["agent"] = agentName
	}
	if parentSession != "" {
		m["parent_session_id"] = parentSession
	}
	return m
}

func buildDecideArgs(promptPath, schemaPath, agentName, argsJSON, validatorCmd, parentSession string) (map[string]any, error) {
	m := map[string]any{
		"schema": schemaPath,
	}

	// --prompt '-' means read from stdin; otherwise treat as file path.
	if strings.TrimSpace(promptPath) == "-" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read prompt from stdin: %w", err)
		}
		m["prompt"] = string(raw)
	} else {
		m["prompt_path"] = promptPath
	}

	if agentName != "" {
		m["agent"] = agentName
	}
	if argsJSON != "" {
		var argsMap map[string]any
		if err := json.Unmarshal([]byte(argsJSON), &argsMap); err != nil {
			return nil, fmt.Errorf("parse --args-json: %w", err)
		}
		m["args"] = argsMap
	}
	if validatorCmd != "" {
		m["validator"] = map[string]any{"post_cmd": validatorCmd}
	}
	if parentSession != "" {
		m["parent_session_id"] = parentSession
	}
	return m, nil
}

func buildAskArgs(promptPath, agentName, workingDir, schemaPath, argsJSON, parentSession string) (map[string]any, error) {
	m := map[string]any{}

	if strings.TrimSpace(promptPath) == "-" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read prompt from stdin: %w", err)
		}
		m["prompt"] = string(raw)
	} else {
		m["prompt"] = promptPath
	}

	if agentName != "" {
		m["agent"] = agentName
	}
	if workingDir != "" {
		m["working_dir"] = workingDir
	}
	if schemaPath != "" {
		m["schema"] = schemaPath
	}
	if argsJSON != "" {
		var argsMap map[string]any
		if err := json.Unmarshal([]byte(argsJSON), &argsMap); err != nil {
			return nil, fmt.Errorf("parse --args-json: %w", err)
		}
		m["args"] = argsMap
	}
	if parentSession != "" {
		m["parent_session_id"] = parentSession
	}
	return m, nil
}

func buildTaskArgs(agentName, workingDir, acceptanceSchema, acceptanceCmd, contextPrompt, parentSession string) map[string]any {
	m := map[string]any{
		"agent":       agentName,
		"working_dir": workingDir,
		"acceptance": map[string]any{
			"schema": acceptanceSchema,
		},
	}
	if acceptanceCmd != "" {
		acceptance, _ := m["acceptance"].(map[string]any)
		acceptance["post_cmd"] = acceptanceCmd
	}
	if contextPrompt != "" {
		m["context"] = map[string]any{"prompt": contextPrompt}
	}
	if parentSession != "" {
		m["parent_session_id"] = parentSession
	}
	return m
}

// ── Output helpers ────────────────────────────────────────────────────────────

// writeOracleResult writes the handler Result to w. On a non-TTY stdout it
// writes line-delimited JSON. On a TTY it writes the 'stdout' or 'answer'
// field as plain text, falling back to JSON.
func writeOracleResult(w io.Writer, res host.Result) error {
	if res.Error != "" {
		return fmt.Errorf("%s", res.Error)
	}
	if isatty(w) {
		return writeOracleResultTTY(w, res)
	}
	return writeOracleResultJSON(w, res)
}

// writeOracleResultJSON writes the Result as a single JSON line to w.
func writeOracleResultJSON(w io.Writer, res host.Result) error {
	b, err := json.Marshal(res.Data)
	if err != nil {
		return fmt.Errorf("marshal oracle result: %w", err)
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// writeOracleResultTTY writes a human-readable representation of the Result to w.
func writeOracleResultTTY(w io.Writer, res host.Result) error {
	if res.Data == nil {
		_, _ = fmt.Fprintln(w, "(no output)")
		return nil
	}
	// Try to surface the most relevant field as plain text.
	for _, key := range []string{"stdout", "answer", "rationale"} {
		if s, ok := res.Data[key].(string); ok && strings.TrimSpace(s) != "" {
			_, err := fmt.Fprint(w, s)
			return err
		}
	}
	// Fall back to JSON.
	return writeOracleResultJSON(w, res)
}

// isatty returns true when w is an *os.File pointing at a terminal.
// Non-file writers (bytes.Buffer, etc.) always return false.
func isatty(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// ── Session ID helpers ────────────────────────────────────────────────────────

// injectSessionID stores the session ID in the returned context via
// host.WithKitsokiSessionID so it can be threaded into subprocess Env slices
// per-call. It does NOT call os.Setenv; that would be process-global and would
// race when multiple oracle-serve clients run concurrently.
func injectSessionID(ctx context.Context, explicitID string) context.Context {
	sid := explicitID
	if sid == "" {
		sid = os.Getenv(oracleSessionIDEnv)
	}
	if sid != "" {
		ctx = host.WithKitsokiSessionID(ctx, sid)
	}
	return ctx
}

// ── Stdin helper ─────────────────────────────────────────────────────────────

// readInputArg reads the input value: returns it directly when it isn't '-',
// otherwise reads from stdin.
func readInputArg(inputStr string) (string, error) {
	if strings.TrimSpace(inputStr) != "-" {
		return inputStr, nil
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read input from stdin: %w", err)
	}
	return string(raw), nil
}

// ── Socket delegation ─────────────────────────────────────────────────────────

// delegateToSocket sends a JSON-RPC call to the oracle-serve daemon over the
// unix socket at sockPath and writes the streamed response to w.
// method is one of "oracle.extract", "oracle.decide", etc.
//
// M11 auto-fallback: if the socket doesn't exist or the dial fails (daemon not
// running), the call is silently routed in-process. This avoids an error when
// KITSOKI_ORACLE_SOCK is set in the environment but no daemon is listening (e.g.
// the parent host-call process didn't start oracle-serve).
func delegateToSocket(ctx context.Context, w io.Writer, sockPath, method string, params map[string]any) error {
	if probe, err := net.Dial("unix", sockPath); err != nil {
		slog.WarnContext(ctx, "no oracle-serve at socket, falling back to in-process",
			"socket", sockPath, "method", method, "reason", err.Error())
		return delegateInProcess(ctx, w, method, params)
	} else {
		probe.Close()
	}
	return oracleRPCCall(ctx, w, sockPath, method, params)
}

// delegateInProcess dispatches a CLI oracle call directly to the handler
// without a subprocess round-trip. Used as the auto-fallback when no
// oracle-serve daemon is reachable.
func delegateInProcess(ctx context.Context, w io.Writer, method string, params map[string]any) error {
	if params == nil {
		params = map[string]any{}
	}
	var res host.Result
	var err error
	switch method {
	case "oracle.extract":
		res, err = host.OracleExtractHandler(ctx, params)
	case "oracle.decide":
		res, err = host.OracleDecideHandler(ctx, params)
	case "oracle.ask":
		res, err = host.OracleAskHandler(ctx, params)
	case "oracle.task":
		res, err = host.OracleTaskHandler(ctx, params)
	case "oracle.converse":
		res, err = host.OracleConverseHandler(ctx, params)
	default:
		return fmt.Errorf("oracle: unknown method %q", method)
	}
	if err != nil {
		return err
	}
	return writeOracleResult(w, res)
}
