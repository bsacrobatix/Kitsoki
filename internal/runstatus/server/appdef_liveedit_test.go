// appdef_liveedit_test.go is THE deliverable proof for the appdef live-edit
// slice (.context/appdef-live-edit-implementation-spec.md): the full
// error -> fix -> retry -> continue sequence, driven over real JSON-RPC
// against a real orchestrator, a real internal/appdef.Service/Binding, and a
// real orchestrator.SessionBinding — no harness, no cassette, no LLM, no
// process restart, and no file ever written to the fixture directory.
//
// It intentionally does NOT use cmd/kitsoki's SessionRegistry: that package
// cannot be imported from here (it is package main, and importing it would
// also drag in the whole CLI wiring). Instead this file builds the smallest
// faithful stand-in — liveEditProvider + gatedDriver — reproducing exactly
// the two disciplines SessionRegistry provides in production:
//
//  1. registry_appdef.go's shape: AppDefProvider methods delegate to one
//     appdef.Binding per session, wrapping the Gate's busy sentinels in
//     server.ErrSessionBusy so the RPC layer reports codeBusy instead of a
//     generic error.
//  2. registry.go's trackingDriver: every turn-advancing Driver call
//     brackets itself with the SAME Gate's BeginTurn/EndTurn, so a
//     definition swap can never race a live turn.
package server_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/appdef"
	"kitsoki/internal/host"
	"kitsoki/internal/journal"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/runstatus"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/store"
)

// liveEditFixture is the ONE canonical copy of the scenario story (spec §5).
// Never written to; every "fixed" revision in this file is produced only in
// memory via strings.ReplaceAll, exactly as appdef_binding_test.go's
// (Slice 4) probe does.
const liveEditFixture = "../../../testdata/apps/liveedit/app.yaml"

// errLiveEditReadOnly is what liveEditProvider's lifecycle methods (the ones
// this acceptance sequence never calls: NewSession/Reload/Staleness/Rescan)
// report, mirroring singleEntryProvider's errReadOnlySurface shape without
// reaching for that unexported sentinel from outside the server package.
var errLiveEditReadOnly = errors.New("liveedit fixture: unsupported outside the appdef acceptance sequence")

// busyWrap wraps a Gate refusal ([appdef.ErrTurnInFlight],
// [appdef.ErrReloadInProgress]) in [server.ErrSessionBusy] so the RPC layer
// (server.lifecycleOrBusyErr) reports codeBusy rather than a generic error —
// the exact boundary cmd/kitsoki/registry_appdef.go's busyWrap crosses in
// production. Reproduced here because that function is unexported in a
// different package.
func busyWrap(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, appdef.ErrTurnInFlight) || errors.Is(err, appdef.ErrReloadInProgress) {
		return fmt.Errorf("%w: %w", server.ErrSessionBusy, err)
	}
	return err
}

// liveEditAppDefRevisionWire projects an appdef.Revision onto the server's
// wire type, mirroring cmd/kitsoki/registry_appdef.go's appDefRevisionWire.
func liveEditAppDefRevisionWire(rev appdef.Revision) server.AppDefRevision {
	h := rev.Header()
	return server.AppDefRevision{
		Schema:       h.Schema,
		Digest:       h.Digest,
		AppID:        h.AppID,
		Entry:        h.Entry,
		ParentDigest: h.ParentDigest,
		Source:       h.Source,
		CreatedAt:    h.CreatedAt,
		Files:        h.Files,
		TotalBytes:   h.TotalBytes,
	}
}

// gatedDriver wraps the live [server.OrchestratorDriver] so a turn cannot run
// concurrently with a definition swap: Turn/SubmitDirect/ContinueTurn bracket
// themselves with the SAME Gate that AppDefReload's Binding.ReloadTo takes,
// reproducing cmd/kitsoki/registry.go's trackingDriver in miniature — Gate's
// two directions are asymmetric (BeginTurn blocks a turn behind an
// in-progress swap; BeginReload refuses a swap outright while a turn is
// running), so this is the piece that makes TestAppDefLiveEdit_
// ReloadRefusedMidTurn's refusal real rather than assumed.
type gatedDriver struct {
	server.Driver
	gate *appdef.Gate
}

func (d gatedDriver) Turn(ctx context.Context, input string) (*orchestrator.TurnOutcome, error) {
	d.gate.BeginTurn()
	defer d.gate.EndTurn()
	return d.Driver.Turn(ctx, input)
}

func (d gatedDriver) SubmitDirect(ctx context.Context, intent string, slots map[string]any) (*orchestrator.TurnOutcome, error) {
	d.gate.BeginTurn()
	defer d.gate.EndTurn()
	return d.Driver.SubmitDirect(ctx, intent, slots)
}

func (d gatedDriver) ContinueTurn(ctx context.Context, slots map[string]any) (*orchestrator.TurnOutcome, error) {
	d.gate.BeginTurn()
	defer d.gate.EndTurn()
	return d.Driver.ContinueTurn(ctx, slots)
}

// liveEditProvider adapts ONE live session (a real orchestrator + a real
// appdef.Binding over it) to the full [server.SessionProvider] interface,
// plus [server.AppDefProvider] — the same two-interface shape
// cmd/kitsoki's SessionRegistry satisfies in production, minimized to what
// this acceptance sequence exercises. The lifecycle methods it does not need
// (NewSession/Reload/Staleness/ListStories/Rescan) report errLiveEditReadOnly,
// exactly as singleEntryProvider does for the methods it has no orchestrator
// behind (provider.go:380-396).
type liveEditProvider struct {
	entry   server.Entry
	binding *appdef.Binding
}

func (p *liveEditProvider) Get(string) (server.Entry, bool) { return p.entry, true }

func (p *liveEditProvider) List() []runstatus.SessionHeader {
	snap, err := p.entry.Source.Snapshot()
	if err != nil || len(snap.Events) == 0 {
		return []runstatus.SessionHeader{}
	}
	return []runstatus.SessionHeader{snap.Session}
}

func (p *liveEditProvider) NewSession(context.Context, string) (string, error) {
	return "", errLiveEditReadOnly
}

func (p *liveEditProvider) Reload(context.Context, string) (bool, error) {
	return false, errLiveEditReadOnly
}

func (p *liveEditProvider) Staleness(context.Context, string) (bool, string, error) {
	return false, "", errLiveEditReadOnly
}

func (p *liveEditProvider) ListStories() []server.StoryHeader { return nil }

func (p *liveEditProvider) Rescan() ([]server.StoryHeader, error) {
	return nil, errLiveEditReadOnly
}

func (p *liveEditProvider) AppDefCurrent(ctx context.Context, sessionID string) (server.AppDefRevision, error) {
	rev, err := p.binding.Current(ctx)
	if err != nil {
		return server.AppDefRevision{}, busyWrap(err)
	}
	return liveEditAppDefRevisionWire(rev), nil
}

func (p *liveEditProvider) AppDefPatch(ctx context.Context, sessionID, baseDigest string, ops []server.AppDefPatchOp) (server.AppDefPatchResult, error) {
	opsIn := make([]appdef.Op, len(ops))
	for i, op := range ops {
		opsIn[i] = appdef.Op{Op: op.Op, Path: op.Path, Content: op.Content}
	}
	res, err := p.binding.Patch(ctx, baseDigest, opsIn)
	if err != nil {
		return server.AppDefPatchResult{}, busyWrap(err)
	}
	out := server.AppDefPatchResult{Rejected: res.Rejected}
	for _, rej := range res.Rejects {
		out.Rejects = append(out.Rejects, server.AppDefReject{Code: rej.Code, File: rej.File, Message: rej.Message})
	}
	if !res.Rejected {
		wire := liveEditAppDefRevisionWire(res.Revision)
		out.Revision = &wire
	}
	return out, nil
}

func (p *liveEditProvider) AppDefRevisions(ctx context.Context, sessionID string) ([]server.AppDefRevision, error) {
	revs, err := p.binding.Revisions(ctx)
	if err != nil {
		return nil, busyWrap(err)
	}
	out := make([]server.AppDefRevision, len(revs))
	for i, rev := range revs {
		out[i] = liveEditAppDefRevisionWire(rev)
	}
	return out, nil
}

func (p *liveEditProvider) AppDefReload(ctx context.Context, sessionID, digest string) (bool, error) {
	prevStateExists, err := p.binding.ReloadTo(ctx, digest)
	if err != nil {
		return false, busyWrap(err)
	}
	return prevStateExists, nil
}

var _ server.SessionProvider = (*liveEditProvider)(nil)
var _ server.AppDefProvider = (*liveEditProvider)(nil)

// buildLiveEditServer wires one live session over the liveedit fixture
// (testdata/apps/liveedit/app.yaml) behind an httptest JSON-RPC server,
// modelled on write_test.go's buildLiveCloak, plus:
//
//   - the two probe hosts (host.probe.fail / host.probe.ok) the fixture's
//     `work` state invokes, and a no-op host.agent.task registration
//     (injectBuiltinStoryAuthoringRoom appends it to every loaded def's
//     hosts: allow-list unconditionally — see Slice 0's notes; without it
//     Reload's ValidateAllowList fails on every swap even though the
//     fixture never calls it);
//   - a POISONED orchestrator.WithReloader fallback that errors loudly. If
//     the revision-reload path ever silently fell through to it (a missing
//     or mis-scoped pending-def cell — implementation spec Open Risk #11),
//     this test fails LOUDLY instead of passing for the wrong reason
//     (e.g. because the on-disk fixture happens to already match the
//     patch). Do not "fix" a failure by making this fallback work.
//   - a real appdef.Service/Gate/Binding over a real orchestrator.
//     SessionBinding, wired into a liveEditProvider + gatedDriver exactly
//     as cmd/kitsoki wires SessionRegistry, and mounted via server.NewMulti
//     (the provider-routing constructor `kitsoki web` itself uses) rather
//     than NewWithSource/WithDriver, which cannot carry an AppDefProvider.
//
// Returns the server, the one session's id (every RPC call in this file
// passes it explicitly — liveEditProvider.Get ignores it, but NewMulti's
// dispatch still requires a session_id param to route), and the shared Gate
// so the negative sub-test can drive BeginTurn/EndTurn directly without a
// real in-flight turn.
func buildLiveEditServer(t *testing.T) (*httptest.Server, string, *appdef.Gate) {
	t.Helper()
	def, err := app.Load(liveEditFixture)
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	jw, err := journal.NewSQLiteWriter(s.DB())
	require.NoError(t, err)
	jr, err := journal.NewSQLiteReader(s.DB())
	require.NoError(t, err)

	reg := host.NewRegistry()
	reg.Register("host.probe.fail", func(context.Context, map[string]any) (host.Result, error) {
		return host.Result{Error: "deliberate probe failure"}, nil
	})
	reg.Register("host.probe.ok", func(context.Context, map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"value": "probe ok"}}, nil
	})
	reg.Register("host.agent.task", func(context.Context, map[string]any) (host.Result, error) {
		return host.Result{}, nil
	})

	orch := orchestrator.New(def, m, s, nil,
		orchestrator.WithJournalWriter(jw),
		orchestrator.WithJournalReader(jr),
		orchestrator.WithHostRegistry(reg),
		orchestrator.WithReloader(func() (*app.AppDef, error) {
			return nil, fmt.Errorf("liveedit fixture: disk reload is not part of this test")
		}),
	)

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	sink, err := store.OpenJSONL(filepath.Join(t.TempDir(), "run.jsonl"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sink.Close() })

	live := server.NewLiveSession(sink, def, string(sid), string(orch.InitialState()))
	orch.SetEventSink(live)
	require.NoError(t, orch.RunInitialOnEnter(ctx, sid))

	svc := appdef.NewService(appdef.NewMemStorage())
	gate := appdef.NewGate()
	binding := appdef.NewBinding(svc, orchestrator.SessionBinding{Orch: orch, SID: sid}, gate)

	driver := gatedDriver{Driver: server.OrchestratorDriver{Orch: orch, SID: sid}, gate: gate}

	provider := &liveEditProvider{
		entry:   server.Entry{Source: live, Driver: driver},
		binding: binding,
	}

	ts := httptest.NewServer(server.NewMulti(provider).Handler())
	t.Cleanup(ts.Close)
	return ts, string(sid), gate
}

// TestAppDefLiveEdit_ErrorFixRetryContinue drives the full acceptance
// sequence from the implementation spec's §2 table end to end: a session
// errors, a client patches the definition into a new immutable revision, the
// session reloads onto it keeping its live world state, and the retry
// continues the story past the step that used to fail.
func TestAppDefLiveEdit_ErrorFixRetryContinue(t *testing.T) {
	t.Parallel()
	ts, sid, _ := buildLiveEditServer(t)

	originalBytes, err := os.ReadFile(liveEditFixture)
	require.NoError(t, err)

	// Step 1: begin -> work's on_enter invokes host.probe.fail, which has no
	// on_error: of its own, so on_error_default: builtin redirects into
	// __error__ with world.error_origin = "work". Assert through the wire:
	// there is no runstatus.session.world RPC, so the rendered view is the
	// only way to see world.error_origin from a client.
	var out1 turnResultWire
	rpcCall(t, ts, "runstatus.session.submit",
		map[string]any{"session_id": sid, "intent": "begin"}, &out1)
	require.Equal(t, "__error__", out1.State)
	assert.Contains(t, out1.View, "Something went wrong")
	// Anchored to the "Failed in:" kv line specifically, NOT a bare
	// Contains(view, "work"): the error room also renders the world.error_log
	// tail, whose entries carry "state":"work", so an unanchored match would
	// pass even if error_origin were never stamped at all.
	assert.Regexp(t, `Failed in:\s+work`, out1.View,
		"the error room's Failed in: kv should render world.error_origin (\"work\")")

	// Step 2: appdef.current captures and pins the session's root revision.
	var cur struct {
		Revision server.AppDefRevision `json:"revision"`
	}
	rpcCall(t, ts, "runstatus.appdef.current", map[string]any{"session_id": sid}, &cur)
	d0 := cur.Revision.Digest
	require.True(t, strings.HasPrefix(d0, "sha256:"), "digest %q should be content-addressed", d0)
	assert.Equal(t, "app.yaml", cur.Revision.Entry)
	assert.Contains(t, cur.Revision.Files, "app.yaml")

	// Step 3: patch app.yaml with the one-substitution fix. The fixed
	// content lives only in memory — never written to liveEditFixture.
	fixed := strings.ReplaceAll(string(originalBytes), "host.probe.fail", "host.probe.ok")
	require.NotEqual(t, string(originalBytes), fixed, "the substitution must actually change the content")

	var patchOut appDefPatchWire
	rpcCall(t, ts, "runstatus.appdef.patch", map[string]any{
		"session_id":  sid,
		"base_digest": d0,
		"ops": []map[string]any{
			{"op": "set_file", "path": "app.yaml", "content": fixed},
		},
	}, &patchOut)
	require.False(t, patchOut.Rejected, "patch should compile clean: %+v", patchOut.RejectDetails)
	require.NotNil(t, patchOut.Revision)
	d1 := patchOut.Revision.Digest
	assert.NotEqual(t, d0, d1)
	assert.Equal(t, d0, patchOut.Revision.ParentDigest)

	// Confirm the fixture file on disk was never touched by the patch.
	afterPatchBytes, err := os.ReadFile(liveEditFixture)
	require.NoError(t, err)
	assert.Equal(t, string(originalBytes), string(afterPatchBytes), "the patch must never write to the fixture file")

	// Step 4: revisions -> two, newest first.
	var revsOut struct {
		Revisions []server.AppDefRevision `json:"revisions"`
	}
	rpcCall(t, ts, "runstatus.appdef.revisions", map[string]any{"session_id": sid}, &revsOut)
	require.Len(t, revsOut.Revisions, 2)
	assert.Equal(t, d1, revsOut.Revisions[0].Digest, "newest first")
	assert.Equal(t, d0, revsOut.Revisions[1].Digest)

	// Step 5: move the LIVE session onto d1, keeping its live world state.
	var reloadOut struct {
		OK              bool   `json:"ok"`
		PrevStateExists bool   `json:"prev_state_exists"`
		Revision        string `json:"revision"`
	}
	rpcCall(t, ts, "runstatus.session.reload",
		map[string]any{"session_id": sid, "revision": d1}, &reloadOut)
	assert.True(t, reloadOut.OK)
	require.True(t, reloadOut.PrevStateExists,
		"__error__ must survive: the patch preserves on_error_default: builtin; if this is false the patch dropped it and __error__ vanished")
	assert.Equal(t, d1, reloadOut.Revision)

	// Steps 6+7: retry the failed step. Both assertions matter together —
	// the state alone would pass even if the retried on_enter were elided.
	var out2 turnResultWire
	rpcCall(t, ts, "runstatus.session.submit",
		map[string]any{"session_id": sid, "intent": "error_retry"}, &out2)
	assert.Equal(t, "work", out2.State, "error_retry should route __error__ -> world.error_origin (\"work\")")
	assert.Contains(t, out2.View, "Probe said: probe ok",
		"the retried on_enter must have re-executed the FIXED host.probe.ok call, not been elided")

	// Step 8: the story continues past the step that used to fail.
	var out3 turnResultWire
	rpcCall(t, ts, "runstatus.session.submit",
		map[string]any{"session_id": sid, "intent": "done"}, &out3)
	assert.Equal(t, "finish", out3.State)
}

// TestAppDefLiveEdit_ReloadRefusedMidTurn is the negative proof: with a turn
// artificially held in flight via the session's own Gate, a revision reload
// is refused with a NAMED error (codeBusy) rather than racing the swap
// against the turn. Once the turn ends, the identical call succeeds.
func TestAppDefLiveEdit_ReloadRefusedMidTurn(t *testing.T) {
	t.Parallel()
	ts, sid, gate := buildLiveEditServer(t)

	var cur struct {
		Revision server.AppDefRevision `json:"revision"`
	}
	rpcCall(t, ts, "runstatus.appdef.current", map[string]any{"session_id": sid}, &cur)
	d0 := cur.Revision.Digest

	// Hold a turn open exactly as gatedDriver.SubmitDirect would around a
	// real in-flight call, without needing one to actually be running —
	// Gate only tracks that a turn is active, not which call is holding it.
	gate.BeginTurn()

	code, msg := rpcCallExpectError(t, ts, "runstatus.session.reload",
		map[string]any{"session_id": sid, "revision": d0})
	assert.Equal(t, -32005, code, "reload during a live turn must be a named refusal, not a silent race")
	assert.Contains(t, msg, "turn is in flight")

	gate.EndTurn()

	var reloadOut struct {
		OK              bool `json:"ok"`
		PrevStateExists bool `json:"prev_state_exists"`
	}
	rpcCall(t, ts, "runstatus.session.reload",
		map[string]any{"session_id": sid, "revision": d0}, &reloadOut)
	assert.True(t, reloadOut.OK, "the identical reload must succeed once the turn ends")
}

// TestAppDefLiveEdit_PatchRejectsBrokenYAML proves a patch that does not
// compile is a structured 200-OK rejection (never an rpcError), and —
// crucially — that a rejected patch stores nothing and moves nothing:
// runstatus.appdef.current still reports the PRE-patch digest afterward.
func TestAppDefLiveEdit_PatchRejectsBrokenYAML(t *testing.T) {
	t.Parallel()
	ts, sid, _ := buildLiveEditServer(t)

	var cur struct {
		Revision server.AppDefRevision `json:"revision"`
	}
	rpcCall(t, ts, "runstatus.appdef.current", map[string]any{"session_id": sid}, &cur)
	d0 := cur.Revision.Digest

	var patchOut appDefPatchWire
	rpcCall(t, ts, "runstatus.appdef.patch", map[string]any{
		"session_id":  sid,
		"base_digest": d0,
		"ops": []map[string]any{
			{"op": "set_file", "path": "app.yaml", "content": "states:\n  {{{"},
		},
	}, &patchOut)
	require.True(t, patchOut.Rejected)
	require.NotEmpty(t, patchOut.RejectDetails)
	assert.Equal(t, "compile_failed", patchOut.RejectDetails[0].Code)
	assert.Nil(t, patchOut.Revision)

	var curAfter struct {
		Revision server.AppDefRevision `json:"revision"`
	}
	rpcCall(t, ts, "runstatus.appdef.current", map[string]any{"session_id": sid}, &curAfter)
	assert.Equal(t, d0, curAfter.Revision.Digest, "a rejected patch must not move the session's pinned digest")
}
