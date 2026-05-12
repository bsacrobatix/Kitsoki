package orchestrator_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/harness"
	"kitsoki/internal/host"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// TestOrchestrator_HostDispatchBindsAndRefreshesView covers the orchestrator's
// post-machine host-call dispatch path: after a state's on_enter invokes a
// host.*, the binding lands in world and the returned view reflects it on the
// same turn (not the next one).
func TestOrchestrator_HostDispatchBindsAndRefreshesView(t *testing.T) {
	def, err := app.Load("testdata/hostbind/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	reg := host.NewRegistry()
	reg.Register("host.probe", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"message": "hello world"}}, nil
	})

	orch := orchestrator.New(def, m, s, noopHarness{}, orchestrator.WithHostRegistry(reg))

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.SubmitDirect(ctx, sid, "ask", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeTransitioned, out.Mode)
	require.Equal(t, app.StatePath("probe"), out.NewState)
	require.True(t, strings.Contains(out.View, "hello world"),
		"expected refreshed view to include bound value, got: %q", out.View)
}

// TestOrchestrator_HostDispatchDisabledWhenNoRegistry verifies the orchestrator
// is safe to run without a host registry: host calls are ignored, bindings do
// not land, and the view still renders (with the pre-host world).
func TestOrchestrator_HostDispatchDisabledWhenNoRegistry(t *testing.T) {
	def, err := app.Load("testdata/hostbind/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Note: no WithHostRegistry — deterministic flow-test posture.
	orch := orchestrator.New(def, m, s, noopHarness{})

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.SubmitDirect(ctx, sid, "ask", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeTransitioned, out.Mode)
	require.Equal(t, app.StatePath("probe"), out.NewState)
	require.False(t, strings.Contains(out.View, "hello world"),
		"host binding should be skipped when no registry is wired")
}

// TestOrchestrator_HostDispatchOnError_RoutesToErrorState verifies that
// when an on_enter `invoke:` step has an `on_error:` arc, a non-empty
// Result.Error from the host handler routes the session to the named
// error state — instead of leaving it stuck in the success target.
//
// Regression for the bugfix room's phase_6_5 verifier hang: the verifier
// returned exit 1 but kitsoki still advanced to the success state because
// the orchestrator captured `last_error` in world without consulting
// hc.OnError to actually transition.
func TestOrchestrator_HostDispatchOnError_RoutesToErrorState(t *testing.T) {
	def, err := app.Load("testdata/hosterror/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	reg := host.NewRegistry()
	reg.Register("host.fail", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Error: "deliberate failure"}, nil
	})

	orch := orchestrator.New(def, m, s, noopHarness{}, orchestrator.WithHostRegistry(reg))

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.SubmitDirect(ctx, sid, "ask", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeTransitioned, out.Mode)
	require.Equal(t, app.StatePath("probe_error"), out.NewState,
		"on_error must route to the named error state on host failure; got %q", out.NewState)
	require.True(t, strings.Contains(out.View, "error_branch"),
		"expected error-state on_enter to fire, got view: %q", out.View)
}

// TestOrchestrator_WithChatStore_InjectsStoreIntoContext verifies that when
// a ChatStore is wired via orchestrator.WithChatStore, it is injected into
// the handler context so ChatStoreFromContext returns it inside the handler.
func TestOrchestrator_WithChatStore_InjectsStoreIntoContext(t *testing.T) {
	def, err := app.Load("testdata/hostbind/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Minimal ChatStore that records whether it was called.
	var storeSeen bool
	cs := &chatStoreProbe{onGet: func() { storeSeen = true }}

	reg := host.NewRegistry()
	reg.Register("host.probe", func(ctx context.Context, args map[string]any) (host.Result, error) {
		got := host.ChatStoreFromContext(ctx)
		if got == cs {
			storeSeen = true
		}
		return host.Result{Data: map[string]any{"message": "ok"}}, nil
	})

	orch := orchestrator.New(def, m, s, noopHarness{},
		orchestrator.WithHostRegistry(reg),
		orchestrator.WithChatStore(cs),
	)

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	_, err = orch.SubmitDirect(ctx, sid, "ask", map[string]any{})
	require.NoError(t, err)
	require.True(t, storeSeen, "expected ChatStore to be present in handler context")
}

// chatStoreProbe is a minimal ChatStore that calls a callback on any method
// to confirm it was injected into context.
type chatStoreProbe struct {
	onGet func()
}

func (p *chatStoreProbe) Get(_ context.Context, _ string) (*host.ChatRecord, error) {
	p.onGet()
	return nil, nil
}
func (p *chatStoreProbe) Resolve(_ context.Context, _, _, _, _ string) (*host.ChatRecord, bool, error) {
	return nil, false, nil
}
func (p *chatStoreProbe) Create(_ context.Context, _, _, _, _ string) (*host.ChatRecord, error) {
	return nil, nil
}
func (p *chatStoreProbe) List(_ context.Context, _, _, _ string) ([]host.ChatRecord, error) {
	return nil, nil
}
func (p *chatStoreProbe) Fork(_ context.Context, _, _ string) (*host.ChatRecord, error) {
	return nil, nil
}
func (p *chatStoreProbe) Archive(_ context.Context, _ string) error               { return nil }
func (p *chatStoreProbe) Rename(_ context.Context, _, _ string) error             { return nil }
func (p *chatStoreProbe) SetClaudeSessionID(_ context.Context, _, _ string) error { return nil }
func (p *chatStoreProbe) AppendMessage(_ context.Context, _, _, _ string, _ map[string]any) (host.ChatMessage, error) {
	return host.ChatMessage{}, nil
}
func (p *chatStoreProbe) Transcript(_ context.Context, _ string, _ int) ([]host.ChatMessage, error) {
	return nil, nil
}
func (p *chatStoreProbe) LatestSeq(_ context.Context, _ string) (int, error) { return -1, nil }
func (p *chatStoreProbe) WithLock(_ context.Context, _ string, fn func(context.Context) error) error {
	return fn(context.Background())
}

// noopHarness is a zero-behavior Harness for SubmitDirect tests. RunTurn is
// never invoked by SubmitDirect, so a stub is sufficient.
type noopHarness struct{}

func (noopHarness) RunTurn(ctx context.Context, in harness.TurnInput) (mcp.CallToolParams, error) {
	return mcp.CallToolParams{}, nil
}
func (noopHarness) Close() error { return nil }

// TestOrchestrator_HostDispatchChained_BoundSlotReachesNextStep verifies
// the core rerenderHostArgs contract: a two-step `on_enter:` block where
// step 2 references step 1's bound slot via a nested template
// (`with.payload.foo: "{{ world.step1_result.value }}"`) must dispatch
// step 2 with the post-bind value, not the machine-time pre-bind nil.
//
// Regression for the silent-fallback bug in rerenderHostArgs: a leaf
// template rendering against `world.step1_result.value` at machine time
// produced nil (slot not yet bound) so the up-front-resolved hc.Args had
// `payload.foo: nil`; the orchestrator's late re-render is what makes it
// land as "X".
func TestOrchestrator_HostDispatchChained_BoundSlotReachesNextStep(t *testing.T) {
	def, err := app.Load("testdata/hostchained/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	var (
		step2Args map[string]any
		mu        sync.Mutex
	)
	reg := host.NewRegistry()
	reg.Register("host.step1", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{
			"step1_result": map[string]any{"value": "X"},
			// Switch type_changer from {} to a string so the leaf
			// `{{ world.type_changer.field }}` in step 3 errors at
			// dispatch time but not at machine time.  Used by the
			// per-leaf fallback test below; harmless for the simpler
			// chained test (step 2 ignores it).
			"type_changer": "now-a-string",
		}}, nil
	})
	reg.Register("host.step2", func(ctx context.Context, args map[string]any) (host.Result, error) {
		mu.Lock()
		step2Args = args
		mu.Unlock()
		return host.Result{Data: map[string]any{"step2_result": map[string]any{"ok": true}}}, nil
	})

	orch := orchestrator.New(def, m, s, noopHarness{}, orchestrator.WithHostRegistry(reg))

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	_, err = orch.SubmitDirect(ctx, sid, "go", map[string]any{})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, step2Args, "host.step2 must have been invoked")
	payload, ok := step2Args["payload"].(map[string]any)
	require.True(t, ok, "step2 args.payload must be a map, got: %#v", step2Args["payload"])
	require.Equal(t, "X", payload["foo"],
		"step2 args.payload.foo must be the post-bind value from step1; got: %#v", payload["foo"])
	require.Equal(t, "kept", payload["literal"],
		"non-template leaves must be preserved verbatim")
}

// TestOrchestrator_HostDispatchChained_LeafFallbackOnBadTemplate verifies
// the per-leaf fallback semantics added to rerenderHostArgs: when one leaf
// of a nested `with:` block fails to render (here, references an unknown
// world slot), the surrounding leaves still see post-bind values and the
// HostDispatched event records `rerender_fell_back: true` so the trace is
// honest about which call received a partially-stale args map.
func TestOrchestrator_HostDispatchChained_LeafFallbackOnBadTemplate(t *testing.T) {
	def, err := app.Load("testdata/hostchained/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	var (
		step3Args map[string]any
		mu        sync.Mutex
	)
	reg := host.NewRegistry()
	reg.Register("host.step1", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{
			"step1_result": map[string]any{"value": "X"},
			// Switch type_changer from {} to a string so the leaf
			// `{{ world.type_changer.field }}` in step 3 errors at
			// dispatch time but not at machine time.  Used by the
			// per-leaf fallback test below; harmless for the simpler
			// chained test (step 2 ignores it).
			"type_changer": "now-a-string",
		}}, nil
	})
	reg.Register("host.step2", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"step2_result": map[string]any{"ok": true}}}, nil
	})
	reg.Register("host.step3", func(ctx context.Context, args map[string]any) (host.Result, error) {
		mu.Lock()
		step3Args = args
		mu.Unlock()
		return host.Result{Data: map[string]any{}}, nil
	})

	orch := orchestrator.New(def, m, s, noopHarness{}, orchestrator.WithHostRegistry(reg))

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	_, err = orch.SubmitDirect(ctx, sid, "go_bad", map[string]any{})
	require.NoError(t, err)

	mu.Lock()
	require.NotNil(t, step3Args, "host.step3 must still run after the bad leaf falls back")
	payload, ok := step3Args["payload"].(map[string]any)
	require.True(t, ok, "step3 args.payload must be a map, got: %#v", step3Args["payload"])
	require.Equal(t, "X", payload["good"],
		"the good leaf must render against the post-bind world; got: %#v", payload["good"])
	require.Equal(t, "kept", payload["literal"],
		"the literal leaf must pass through unchanged")
	// The bad leaf falls back to the machine-time up-front-resolved value.
	// That up-front render of `{{ world.never_bound.does_not_exist }}` also
	// errors against an empty world; the fallback path keeps the raw
	// template string so the handler can still see *something* and the
	// HostDispatched event records the fallback.  The exact value is
	// implementation-defined (nil or raw template); the contract is "the
	// handler still runs and the surrounding leaves are correct".
	mu.Unlock()

	// HostDispatched for host.step3 must record rerender_fell_back: true.
	history, err := s.LoadHistory(sid)
	require.NoError(t, err)
	foundStep3Dispatch := false
	for _, ev := range history {
		if ev.Kind != store.HostDispatched {
			continue
		}
		var p map[string]any
		require.NoError(t, json.Unmarshal(ev.Payload, &p))
		if p["namespace"] != "host.step3" {
			continue
		}
		foundStep3Dispatch = true
		require.Equal(t, true, p["rerender_fell_back"],
			"HostDispatched for step3 must record rerender_fell_back: true; payload=%#v", p)
		// Sanity: the args.payload.good leaf must be in the event payload too.
		argsP, _ := p["args"].(map[string]any)
		payloadP, _ := argsP["payload"].(map[string]any)
		require.Equal(t, "X", payloadP["good"],
			"HostDispatched.args must reflect the rerendered (post-bind) args")
	}
	require.True(t, foundStep3Dispatch,
		"HostDispatched event for host.step3 must appear in the event log")

	// And for step1/step2 (the all-good cases), HostDispatched must record
	// rerender_fell_back: false so the diagnostic story differentiates the
	// good calls from the partially-stale one.
	for _, ev := range history {
		if ev.Kind != store.HostDispatched {
			continue
		}
		var p map[string]any
		require.NoError(t, json.Unmarshal(ev.Payload, &p))
		ns, _ := p["namespace"].(string)
		if ns == "host.step1" || ns == "host.step2" {
			require.Equal(t, false, p["rerender_fell_back"],
				"HostDispatched for %s must NOT record a fallback; payload=%#v", ns, p)
		}
	}
}
