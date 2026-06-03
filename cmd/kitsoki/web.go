// web.go — implements `kitsoki web`, the interactive browser surface.
//
// Where `kitsoki status serve` is a read-only observer that tails a JSONL
// trace another process writes, `kitsoki web` hosts a LIVE orchestrator in the
// same process and serves the runstatus SPA/RPC/SSE surface against it. This is
// option A of docs/proposals/web-ui.md: one process, one orchestrator, reusing
// the runstatus read plumbing for observation. The read side lets the browser
// observe the live session render and trace; the write RPCs let the browser
// DRIVE the session (turn / submit / continue / offpath) and read the current
// room (session.view).
//
// Construction is shared with `kitsoki run` via buildSessionRuntime
// (runtime.go); web.go layers the live posture choices on top: a fresh session
// every launch, an optional deterministic --flow / --host-cassette posture (no
// LLM — intents are submitted explicitly), and the HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"

	"kitsoki/internal/app"
	"kitsoki/internal/inbox"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/store"
	"kitsoki/internal/testrunner"
	"kitsoki/internal/tui"
)

func webCmd() *cobra.Command {
	var (
		addr          string
		harnessType   string
		claudeModel   string
		recordingPath string
		recordPath    string
		dbPath        string
		execModeFlag  string
		flowPath      string
		hostCassette  string
		warpBasisPath string
	)

	cmd := &cobra.Command{
		Use:   "web <app.yaml>",
		Short: "Serve an interactive browser UI for an app (live session)",
		Long: `Load an app definition, start a live session in-process, and serve the
runstatus web UI over HTTP against it.

Unlike 'kitsoki status serve' — which tails a JSONL trace another process
writes, read-only — 'kitsoki web' hosts the orchestrator itself, so the browser
observes a session running in this process. A fresh session is started on each
launch.

  kitsoki web testdata/apps/cloak/app.yaml
  kitsoki web myapp.yaml --addr 127.0.0.1:7777 --harness claude

Deterministic (no-LLM) posture for UI development and Playwright tests:

  kitsoki web stories/prd/app.yaml --flow stories/prd/flows/happy_path.yaml

With --flow, the flow fixture's host_handlers stubs back every host.* call and
the harness is nil — the browser drives the session by submitting intents
(runstatus.session.submit) exactly as the flow's turns specify, with no LLM.

The runstatus SPA must be bundled into the binary (run 'make build', which runs
'pnpm build' under tools/runstatus/); otherwise the page reports the UI as
unbuilt. Assumes a trusted localhost / internal network; there is no
authentication.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appPath := args[0]

			// Resolve the execution mode (execution-modes proposal). Staged by
			// default, matching the TUI.
			var execMode orchestrator.ExecutionMode
			switch execModeFlag {
			case "staged":
				execMode = orchestrator.ExecStaged
			case "one-shot", "oneshot":
				execMode = orchestrator.ExecOneShot
			default:
				return fmt.Errorf("--mode %q is invalid (want \"staged\" or \"one-shot\")", execModeFlag)
			}

			// ── Load the flow fixture (deterministic posture) if requested ──
			var (
				fixture      *testrunner.FlowFixture
				flowFilePath string
			)
			if flowPath != "" {
				abs, aerr := filepath.Abs(flowPath)
				if aerr != nil {
					return fmt.Errorf("resolve --flow path: %w", aerr)
				}
				flowFilePath = abs
				data, rerr := os.ReadFile(abs)
				if rerr != nil {
					return fmt.Errorf("read --flow %q: %w", flowPath, rerr)
				}
				var f testrunner.FlowFixture
				if uerr := yaml.Unmarshal(data, &f); uerr != nil {
					return fmt.Errorf("parse --flow %q: %w", flowPath, uerr)
				}
				fixture = &f
				// The flow's `app:` is relative to the flow file. Prefer the
				// explicit <app.yaml> arg; fall back to the flow's app: only
				// when the arg resolves to the same file conceptually. We use
				// the arg as authoritative here (the caller supplied it).
			} else if hostCassette != "" {
				// --host-cassette without --flow: a minimal deterministic
				// fixture carrying only the cassette, so the nil-harness posture
				// applies and cassette episodes back every host.* call.
				fixture = &testrunner.FlowFixture{}
			}

			// --host-cassette overrides / sets the fixture's cassette path. The
			// path is resolved relative to the flow file (when --flow is set) or
			// the cwd (when standalone) inside buildSessionRuntime.
			if hostCassette != "" && fixture != nil {
				fixture.HostCassette = hostCassette
				if flowFilePath == "" {
					if abs, aerr := filepath.Abs(hostCassette); aerr == nil {
						fixture.HostCassette = abs
					}
				}
			}

			def, err := loadAppWithEnv(appPath)
			if err != nil {
				return err
			}

			if dbPath == "" {
				dbPath = defaultDBPath()
			}
			if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
				return fmt.Errorf("create db directory: %w", err)
			}

			// ── Orchestrator construction (shared with `kitsoki run`) ───────
			rt, err := buildSessionRuntime(runtimeConfig{
				AppPath:       appPath,
				Def:           def,
				DBPath:        dbPath,
				ExecMode:      execMode,
				HarnessType:   harnessType,
				ClaudeModel:   claudeModel,
				RecordingPath: recordingPath,
				RecordPath:    recordPath,
				Flow:          fixture,
				FlowFilePath:  flowFilePath,
			})
			if err != nil {
				return err
			}
			defer rt.Close()
			orch := rt.Orch

			ctx := context.Background()

			// ── Fresh session + live event sink ────────────────────────────
			sid, err := orch.NewSession(ctx)
			if err != nil {
				return fmt.Errorf("create session: %w", err)
			}

			tracePath := store.DefaultTracePath(def.App.ID, "web", string(sid))
			if mkErr := os.MkdirAll(filepath.Dir(tracePath), 0o755); mkErr != nil {
				return fmt.Errorf("create trace directory: %w", mkErr)
			}
			sink, err := store.OpenJSONL(tracePath)
			if err != nil {
				return fmt.Errorf("open trace sink: %w", err)
			}
			defer func() { _ = sink.Close() }()

			// Wrap the sink so the orchestrator's appends and the HTTP server's
			// reads share one lock — the JSONLSink underneath is not safe for
			// concurrent Append + History.
			live := server.NewLiveSession(sink, def, string(sid), string(orch.InitialState()))
			orch.SetEventSink(live)

			// Record the effective story as the first event so the trace
			// self-describes (matches `kitsoki run`).
			if err := orch.RecordEffectiveStory(ctx, sid); err != nil {
				return fmt.Errorf("record effective story: %w", err)
			}

			// Seed the initial world from the flow fixture's initial_world (if
			// any) BEFORE on_enter runs. The orchestrator has no initial-world
			// option, so we apply it via a Teleport to the fixture's
			// initial_state with the world as slots. Skipped when --warp is also
			// given (warp owns the bootstrap teleport). Documented in the flow
			// posture: this seeds the room the flow expects to start in.
			warpUsed := warpBasisPath != ""
			if fixture != nil && len(fixture.InitialWorld) > 0 && !warpUsed {
				target := app.StatePath(fixture.InitialState)
				if target == "" {
					target = orch.InitialState()
				}
				slots := make(map[string]any, len(fixture.InitialWorld))
				for k, v := range fixture.InitialWorld {
					slots[k] = v
				}
				if _, terr := orch.Teleport(ctx, sid, inbox.TeleportTarget{State: target, Slots: slots}); terr != nil {
					return fmt.Errorf("seed initial_world via teleport: %w", terr)
				}
			}

			// Fire the initial state's on_enter chain before serving so the
			// first frame the browser renders reflects on-enter-bound world
			// keys. When a teleport above already landed us in the initial
			// state, on_enter ran as part of the teleport; running it again is
			// idempotent for the prd flow's stubbed host calls.
			if !warpUsed && (fixture == nil || len(fixture.InitialWorld) == 0) {
				if err := orch.RunInitialOnEnter(ctx, sid); err != nil {
					return fmt.Errorf("run initial on_enter: %w", err)
				}
			}

			// --warp: bootstrap teleport (mirrors `kitsoki run`). Applied after
			// session create so the operator lands at the primed state.
			if warpUsed {
				resolved, basis, basisErr := tui.LoadWarpBasis(warpBasisPath, appPath)
				if basisErr != nil {
					return fmt.Errorf("--warp %q: %w", warpBasisPath, basisErr)
				}
				if basis.State == "" {
					return fmt.Errorf("--warp %s: missing required `state:` field", resolved)
				}
				slots := make(map[string]any, len(basis.World))
				for k, v := range basis.World {
					slots[k] = v
				}
				if _, warpErr := orch.Teleport(ctx, sid, inbox.TeleportTarget{
					State: app.StatePath(basis.State),
					Slots: slots,
				}); warpErr != nil {
					return fmt.Errorf("--warp %s: teleport: %w", resolved, warpErr)
				}
			}

			// ── Serve ───────────────────────────────────────────────────────
			driver := server.OrchestratorDriver{Orch: orch, SID: sid}
			srv := server.NewWithSource(live, server.WithDriver(driver))
			httpSrv := &http.Server{
				Addr:              addr,
				Handler:           srv.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}

			serveCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
			defer cancel()
			go func() {
				<-serveCtx.Done()
				shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer shutCancel()
				_ = httpSrv.Shutdown(shutCtx)
			}()

			fmt.Fprintf(cmd.ErrOrStderr(), "kitsoki: web UI for app %q (session %s) on http://%s\n", def.App.ID, sid, addr)
			if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("serve: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7777", "HTTP listen address")
	cmd.Flags().StringVar(&harnessType, "harness", "", "harness: claude | live | replay | recording (default: auto-select; ignored with --flow)")
	cmd.Flags().StringVar(&claudeModel, "claude-model", "", "claude model when --harness=claude (e.g. opus, sonnet)")
	cmd.Flags().StringVar(&recordingPath, "recording", "", "path to recording YAML (for --harness replay)")
	cmd.Flags().StringVar(&recordPath, "record", "", "path to output JSONL recording (for --harness recording)")
	cmd.Flags().StringVar(&dbPath, "db", "", "SQLite session store path (default: nearest .kitsoki/sessions.db)")
	cmd.Flags().StringVar(&execModeFlag, "mode", "staged", "execution mode: staged | one-shot")
	cmd.Flags().StringVar(&flowPath, "flow", "", "drive the session deterministically from a flow fixture (no LLM; host_handlers stub host.* calls, intents are submitted explicitly)")
	cmd.Flags().StringVar(&hostCassette, "host-cassette", "", "host cassette file backing host.* calls (deterministic, no LLM); combinable with --flow")
	cmd.Flags().StringVar(&warpBasisPath, "warp", "", "path to a warp-basis YAML (state + world overrides); applied as the first action after session create")

	return cmd
}
