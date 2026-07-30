package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// TestReloadForSession_NoRaceAgainstBackgroundJobCompletion is the -race
// regression test for the appdef-live-edit review's blocker finding: before
// [orchestrator.Orchestrator.ReloadForSession] existed, Reload swapped
// o.def/o.machine under o.mu while the session listener's handleJobTerminal
// (spawned by Orchestrator.NewSession whenever a scheduler is configured,
// per background_test.go's TestBackgroundJobEndToEnd) read them under a
// DIFFERENT, per-session lock (sessionLock) — the same lock Turn/
// SubmitDirect/RerunOnEnter already use to serialize against
// handleJobTerminal, but that the bare Reload never took.
//
// Concretely: swapping the call below back to the bare `orch.Reload("",
// app.StatePath("lobby"))` (no session lock) and running
// `go test -run TestReloadForSession_NoRaceAgainstBackgroundJobCompletion
// -count=10 -race` reliably reproduces (verified during this fix — a real
// race is inherently non-deterministic, so it does not fire on every single
// run, but did on multiple runs out of ten):
//
//	WARNING: DATA RACE
//	Write at 0x... by goroutine running Reload:
//	  kitsoki/internal/render.newAppRenderer()
//	  kitsoki/internal/machine.New()
//	  kitsoki/internal/orchestrator.(*Orchestrator).Reload()
//	Previous read at 0x... by goroutine running handleJobTerminal:
//	  kitsoki/internal/render.(*AppRenderer).Render()
//	  kitsoki/internal/machine.(*machineImpl).RenderStateTyped()
//	  kitsoki/internal/orchestrator.(*Orchestrator).handleJobTerminal()
//	  kitsoki/internal/orchestrator.(*Orchestrator).startSessionListener.func1.1()
//
// — the swap in Reload (rebuilding o.machine's underlying pongo2 template
// set) racing the listener's synthetic background-completion turn reading
// that exact template set to render its view. This is the same o.machine
// straddling hazard the review's manually-constructed probe found; it is
// simply which specific field inside o.machine gets caught that varies
// between runs.
//
// This test drives a background job to completion CONCURRENTLY with a
// ReloadForSession call for the same session, with the host handler
// deliberately gated so the two overlap in real wall-clock time rather than
// happening to run sequentially. It must be green under `-race`, and the
// background job's on_complete effects must still land correctly (proving
// the serialization doesn't just avoid the race by accident — the turn the
// listener produces is coherent).
func TestReloadForSession_NoRaceAgainstBackgroundJobCompletion(t *testing.T) {
	def := &app.AppDef{
		App:   app.AppMeta{ID: "reload-race-test"},
		Root:  "init",
		Hosts: []string{"host.test.gate"},
		World: map[string]app.VarDef{
			"x":           {Type: "string", Default: ""},
			"last_job_id": {Type: "string", Default: ""},
		},
		Intents: map[string]app.Intent{
			"enter": {Title: "Enter"},
			"done":  {Title: "Done"},
		},
		States: map[string]*app.State{
			"init": {
				View: app.LegacyView("init"),
				On:   map[string][]app.Transition{"enter": {{Target: "lobby"}}},
			},
			"lobby": {
				View: app.LegacyView("lobby x={{ world.x }}"),
				OnEnter: []app.Effect{
					{
						Invoke:     "host.test.gate",
						With:       map[string]any{"msg": "hello"},
						Background: true,
						Bind:       map[string]string{"last_job_id": "job_id"},
						OnComplete: []app.Effect{
							{Set: map[string]any{"x": "{{ world.last_job_result.output }}"}},
							{Say: "done"},
						},
					},
				},
				On: map[string][]app.Transition{"done": {{Target: "end"}}},
			},
			"end": {Terminal: true, View: app.LegacyView("ended")},
		},
	}

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	jobStore, err := jobs.NewJobStore(s.DB())
	require.NoError(t, err)
	sched := jobs.NewScheduler(jobStore)

	// Gated echo handler: blocks until `unblock` is closed, so the test
	// controls exactly when the job (and therefore handleJobTerminal on the
	// session listener goroutine) completes relative to the concurrent
	// ReloadForSession call below.
	unblock := make(chan struct{})
	reg := host.NewRegistry()
	reg.Register("host.test.gate", func(ctx context.Context, args map[string]any) (host.Result, error) {
		<-unblock
		msg, _ := args["msg"].(string)
		return host.Result{Data: map[string]any{"output": msg}}, nil
	})

	h := &staticHarness{intentName: "enter"}

	orch := orchestrator.New(def, m, s, h,
		orchestrator.WithHostRegistry(reg),
		orchestrator.WithScheduler(sched),
		orchestrator.WithJobStore(jobStore),
		// ReloadForSession consults this closure (via Reload); returning the
		// SAME def is enough to exercise the swap machinery (o.mu-guarded
		// def/machine replacement, ValidateAllowList, promptRenderer
		// rebuild) without needing a second on-disk fixture.
		orchestrator.WithReloader(func() (*app.AppDef, error) { return def, nil }),
	)

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.Turn(ctx, sid, "enter")
	require.NoError(t, err)
	require.Equal(t, app.StatePath("lobby"), out.NewState)

	journey, err := orch.LoadJourney(sid)
	require.NoError(t, err)
	require.NotEmpty(t, journey.World.Vars["last_job_id"], "background job must have been dispatched")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(unblock)
	}()
	// Give the session listener goroutine a head start into
	// handleJobTerminal (loadJourney + store reads/writes are real,
	// measurable work) so the reload below lands squarely INSIDE its
	// critical section rather than racing to start first and finishing
	// before the listener has even begun — that ordering would let the
	// buggy (unguarded) code path pass by sheer scheduling luck instead of
	// exercising the overlap this test exists to catch.
	time.Sleep(2 * time.Millisecond)

	_, reloadErr := orch.ReloadForSession("", app.StatePath("lobby"), sid)
	require.NoError(t, reloadErr)

	wg.Wait()

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	require.NoError(t, sched.WaitIdle(waitCtx), "scheduler did not go idle in time")
	require.NoError(t, orch.WaitListenerIdle(waitCtx, sid), "listener did not go idle in time")

	final, err := orch.LoadJourney(sid)
	require.NoError(t, err)
	require.Equal(t, "hello", final.World.Vars["x"],
		"on_complete must still have resolved correctly despite the concurrent reload")
}
