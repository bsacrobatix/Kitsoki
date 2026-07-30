package main

// registry_appdef_test.go exercises SessionRegistry's [server.AppDefProvider]
// implementation (registry_appdef.go) against a real, deterministic (no-LLM)
// session built the same way registry_test.go's other registry-level tests
// are: NewRegistry + NewSession over minimalStory. That is a deliberate,
// named deviation from this slice's own brief, which suggested hand-building
// a bare &SessionRegistry{} + entry (the registry_feedback_test.go /
// registry_application_read_models_test.go pattern) to avoid the full
// NewRegistry/runtimeBase machinery. AppDefCurrent needs a WORKING
// orchestrator behind e.binding — orchestrator.SessionBinding.Closure calls
// store.CollectEffectiveStory(o.currentDef()), which needs a real, loaded
// AppDef and a live session id — so a hand-assembled entry would still need
// to build a real orchestrator.Orchestrator + machine.New + store.OpenMemory
// by hand (the pattern internal/orchestrator/appdef_binding_test.go uses).
// Reusing this package's own existing NewSession path is simpler and no less
// deterministic: buildSessionRuntime's nil-harness posture (deterministicBase)
// touches no LLM and no network, exactly like every other registry_test.go
// case.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/webconfig"
)

// var _ server.AppDefProvider = (*SessionRegistry)(nil) is also asserted in
// registry.go next to the rest of the provider seams; repeated here, as the
// slice brief asks, so this file independently proves the shape it exercises
// compiles even if that registry.go assertion were ever removed.
var _ server.AppDefProvider = (*SessionRegistry)(nil)

func TestSessionRegistry_AppDefProvider_CurrentAndRevisions(t *testing.T) {
	ctx := context.Background()
	storiesDir, appPath := writeStory(t, "mini", []byte(minimalStory))
	reg := NewRegistry(webconfig.WebConfig{}, []string{storiesDir}, deterministicBase(t))
	t.Cleanup(reg.Close)
	_, err := reg.Rescan()
	require.NoError(t, err)

	sid, err := reg.NewSession(ctx, appPath)
	require.NoError(t, err)

	// First call captures a root revision and pins the session to it.
	rev, err := reg.AppDefCurrent(ctx, sid)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(rev.Digest, "sha256:"), "digest = %q, want a sha256: prefix", rev.Digest)
	assert.Equal(t, "app.yaml", rev.Entry)
	assert.Equal(t, "mini-story", rev.AppID)
	assert.Contains(t, rev.Files, "app.yaml")

	// A second AppDefCurrent call reports the SAME pinned digest — no new
	// revision is minted merely by re-reading it.
	rev2, err := reg.AppDefCurrent(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, rev.Digest, rev2.Digest)

	revs, err := reg.AppDefRevisions(ctx, sid)
	require.NoError(t, err)
	require.Len(t, revs, 1, "one root revision has been captured so far")
	assert.Equal(t, rev.Digest, revs[0].Digest)

	// AppDefCurrent's engagement pins the session: Reload's plain disk path no
	// longer refuses outright — it captures whatever is currently on disk as
	// a revision and moves the session onto it (SessionRegistry.
	// reloadPinnedFromDisk), keeping the documented edit -> reload_requested
	// -> session.reload SPA flow (docs/web/README.md) working even after a
	// session has separately engaged the definition control plane. Disk is
	// UNCHANGED since AppDefCurrent's capture above, so Capture returns the
	// SAME revision (Put is idempotent for identical content) rather than
	// minting a new one — this exercises the "no-op" case of the pinned
	// reload path specifically.
	_, err = reg.Reload(ctx, sid)
	require.NoError(t, err)

	revsAfterReload, err := reg.AppDefRevisions(ctx, sid)
	require.NoError(t, err)
	require.Len(t, revsAfterReload, 1, "disk is unchanged, so no NEW revision should have been minted")
	assert.Equal(t, rev.Digest, revsAfterReload[0].Digest)

	// Staleness compares disk against the PINNED revision's own bytes —
	// still identical, so "not stale" here is a genuine same-bytes
	// comparison, not a no-baseline default.
	stale, diff, err := reg.Staleness(ctx, sid)
	require.NoError(t, err)
	assert.False(t, stale)
	assert.Empty(t, diff)
}

func TestSessionRegistry_AppDefProvider_ReloadRefusedWhileTurnInFlight(t *testing.T) {
	ctx := context.Background()
	storiesDir, appPath := writeStory(t, "mini", []byte(minimalStory))
	reg := NewRegistry(webconfig.WebConfig{}, []string{storiesDir}, deterministicBase(t))
	t.Cleanup(reg.Close)
	_, err := reg.Rescan()
	require.NoError(t, err)

	sid, err := reg.NewSession(ctx, appPath)
	require.NoError(t, err)

	// A valid, stored digest is required: appdef.Binding.ReloadTo checks the
	// revision out (which would fail with ErrUnknownRevision on a made-up
	// digest) BEFORE it ever consults the Gate, so the busy path can only be
	// observed against a digest that actually resolves.
	rev, err := reg.AppDefCurrent(ctx, sid)
	require.NoError(t, err)

	reg.mu.Lock()
	e, ok := reg.sessions[sid]
	reg.mu.Unlock()
	require.True(t, ok, "unknown session %q", sid)

	e.gate.BeginTurn()
	t.Cleanup(e.gate.EndTurn)

	_, err = reg.AppDefReload(ctx, sid, rev.Digest)
	require.Error(t, err)
	assert.True(t, errors.Is(err, server.ErrSessionBusy), "err = %v, want it to wrap server.ErrSessionBusy", err)
}

// TestSessionRegistry_AppDefProvider_ErrorFixRetryContinue closes the
// reviewer's finding that the production AppDefProvider — SessionRegistry
// itself — is never driven through the full patch -> reload -> retry
// sequence. internal/runstatus/server/appdef_liveedit_test.go proves that
// sequence over real JSON-RPC, but it is built on a hand-written
// liveEditProvider stand-in (SessionRegistry lives in package main and
// cannot be imported from internal/runstatus/server), so a bug in THIS
// package's own op mapping or delegation — e.g. registry_appdef.go's
// AppDefPatch dropping op.Content when building []appdef.Op, or AppDefReload
// forgetting to invalidate e.metaController — would leave the acceptance
// suite green while `kitsoki web` shipped a broken live-edit path. This test
// drives the SessionRegistry methods directly (AppDefCurrent, AppDefPatch,
// AppDefReload, and a real SubmitDirect("error_retry")) over the same
// testdata/apps/liveedit/app.yaml fixture and the same probe-host shapes the
// acceptance test uses, so a regression here is caught at the layer that
// actually ships.
func TestSessionRegistry_AppDefProvider_ErrorFixRetryContinue(t *testing.T) {
	ctx := context.Background()
	repoRoot := repoRootForRegistryTest(t)
	fixturePath := filepath.Join(repoRoot, "testdata", "apps", "liveedit", "app.yaml")
	originalBytes, err := os.ReadFile(fixturePath)
	require.NoError(t, err)

	reg := NewRegistry(webconfig.WebConfig{}, []string{filepath.Dir(fixturePath)}, deterministicBase(t))
	t.Cleanup(reg.Close)
	_, err = reg.Rescan()
	require.NoError(t, err)

	sid, err := reg.NewSession(ctx, fixturePath)
	require.NoError(t, err)

	// Register the two probe hosts BEFORE driving "begin" — the fixture's
	// `work` state invokes host.probe.fail from its on_enter, so the handler
	// must exist in the session's own host registry before that turn runs.
	// Shapes copied verbatim from appdef_liveedit_test.go's
	// buildLiveEditServer so both proofs exercise the identical scenario.
	hostReg, ok := reg.ApplicationHostRegistry(sid)
	require.True(t, ok, "a live session must expose its application host registry")
	hostReg.Replace("host.probe.fail", func(context.Context, map[string]any) (host.Result, error) {
		return host.Result{Error: "deliberate probe failure"}, nil
	})
	hostReg.Replace("host.probe.ok", func(context.Context, map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"value": "probe ok"}}, nil
	})

	entry, ok := reg.Get(sid)
	require.True(t, ok)

	// Step 1: "begin" drives into `work`, whose on_enter invoke fails and (via
	// on_error_default: builtin) redirects into the builtin error room.
	out1, err := entry.Driver.SubmitDirect(ctx, "begin", nil)
	require.NoError(t, err)
	require.Equal(t, app.ErrorRoomState, string(out1.NewState))

	// Step 2: AppDefCurrent captures and pins the session's root revision —
	// this is the real SessionRegistry.AppDefCurrent, not a stand-in.
	d0Rev, err := reg.AppDefCurrent(ctx, sid)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(d0Rev.Digest, "sha256:"), "digest = %q, want a sha256: prefix", d0Rev.Digest)

	// Step 3: AppDefPatch substitutes host.probe.fail -> host.probe.ok. If
	// registry_appdef.go's op mapping ever dropped op.Content while building
	// []appdef.Op (the exact bug this test exists to catch), the patch would
	// compile against an EMPTY app.yaml and fail with RejectCompileFailed —
	// require.False below would catch that directly.
	fixed := strings.ReplaceAll(string(originalBytes), "host.probe.fail", "host.probe.ok")
	require.NotEqual(t, string(originalBytes), fixed, "the substitution must actually change the content")

	patchOut, err := reg.AppDefPatch(ctx, sid, d0Rev.Digest, []server.AppDefPatchOp{
		{Op: "set_file", Path: "app.yaml", Content: fixed},
	})
	require.NoError(t, err)
	require.False(t, patchOut.Rejected, "patch should compile clean: %+v", patchOut.Rejects)
	require.NotNil(t, patchOut.Revision)
	assert.NotEqual(t, d0Rev.Digest, patchOut.Revision.Digest, "the patched revision must be a new digest")
	assert.Equal(t, d0Rev.Digest, patchOut.Revision.ParentDigest, "the patched revision must name its parent")

	// The fixture file on disk must never be touched by the patch.
	afterPatchBytes, err := os.ReadFile(fixturePath)
	require.NoError(t, err)
	assert.Equal(t, string(originalBytes), string(afterPatchBytes), "AppDefPatch must never write to the fixture file")

	// Step 4: AppDefReload moves the LIVE session onto the patched revision,
	// keeping its live world state (world.error_origin == "work" survives).
	prevStateExists, err := reg.AppDefReload(ctx, sid, patchOut.Revision.Digest)
	require.NoError(t, err)
	require.True(t, prevStateExists,
		"__error__ must survive the reload: the patch preserves on_error_default: builtin")

	// Steps 5+6: retry the failed step through a real SubmitDirect. Both
	// assertions matter together — the state alone would pass even if the
	// retried on_enter were elided instead of re-executed against the FIXED
	// definition (registry_appdef.go's AppDefReload -> e.binding.ReloadTo ->
	// binder.Reenter path).
	out2, err := entry.Driver.SubmitDirect(ctx, app.ErrorRoomRetryIntent, nil)
	require.NoError(t, err)
	assert.Equal(t, "work", string(out2.NewState),
		"error_retry should route __error__ -> world.error_origin (\"work\")")
	assert.Contains(t, out2.View, "probe ok",
		"the retried on_enter must have re-executed the FIXED host.probe.ok call, not been elided")
}

// TestSessionRegistry_Reload_PinnedDigestGuard closes the reviewer's finding
// that SessionRegistry.Reload's pinnedDigest guard (registry.go's Reload,
// ~line 2450) has no test in either direction.
//
// NOTE on what "guard" means here: the slice-6 spec's ORIGINAL plan had a
// pinned session's plain Reload return a bare named refusal
// ("... is pinned to revision ..."). That was superseded before this shipped
// — see registry.go's own Reload doc comment ("Revised after review") —
// because a permanent refusal would break the documented
// edit -> reload_requested -> session.reload SPA flow (docs/web/README.md)
// the instant ANY surface separately engaged the definition control plane
// for the same session. The shipped guard instead reroutes a pinned
// session's plain Reload through reloadPinnedFromDisk, which captures disk
// as a NEW revision and moves the session onto it through the SAME
// Gate-guarded ReloadTo path an explicit session.reload{revision} call uses
// — never a silent disk-only swap, and never an outright refusal, for a
// session with an on-disk story. reloadPinnedFromDisk's own doc comment
// notes a THIRD case, a synthetic (no on-disk story) pinned session, which
// keeps the literal named refusal — but that branch is currently
// unreachable through the public API: both synthetic session kinds
// (synthesizeAgentRoot, synthesizeImplicitRoot) fail earlier, inside
// AppDefCurrent's own store.CollectEffectiveStory call, before a synthetic
// session can ever become pinned in the first place (verified by hand while
// writing this test — CollectEffectiveStory requires a real on-disk entry
// file among its captured files, which no synthesized root has). Not this
// finding's scope to fix; noted here so the next reader does not assume an
// integration test for it exists or is easy to add.
//
// Both directions, proven here:
//   - BEFORE any appdef.* call, e.pinnedDigest is "" and a plain Reload takes
//     the ordinary orch.ReloadForSession path, leaving pinnedDigest "" —
//     the definition control plane is not touched at all.
//   - AFTER an AppDefCurrent call pins the session (markPinned fires on ANY
//     successful appdef.* call, including a bare read — see markPinned's own
//     doc comment), a SUBSEQUENT disk edit is captured as a genuinely NEW
//     revision with correct lineage, and the pin moves onto it — not
//     silently discarded, and not refused.
func TestSessionRegistry_Reload_PinnedDigestGuard(t *testing.T) {
	ctx := context.Background()
	storiesDir, appPath := writeStory(t, "mini", []byte(minimalStory))
	reg := NewRegistry(webconfig.WebConfig{}, []string{storiesDir}, deterministicBase(t))
	t.Cleanup(reg.Close)
	_, err := reg.Rescan()
	require.NoError(t, err)

	sid, err := reg.NewSession(ctx, appPath)
	require.NoError(t, err)

	reg.mu.Lock()
	e, ok := reg.sessions[sid]
	reg.mu.Unlock()
	require.True(t, ok, "unknown session %q", sid)

	// --- BEFORE: no appdef.* call has ever been made. ---
	reg.mu.Lock()
	pinnedBefore := e.pinnedDigest
	reg.mu.Unlock()
	require.Empty(t, pinnedBefore, "a fresh session must not be pinned")

	require.NoError(t, os.WriteFile(appPath, append([]byte("# first edit\n"), []byte(minimalStory)...), 0o644))
	prevExists, err := reg.Reload(ctx, sid)
	require.NoError(t, err)
	assert.True(t, prevExists, "foyer still exists after a benign edit")

	reg.mu.Lock()
	pinnedAfterPlainReload := e.pinnedDigest
	reg.mu.Unlock()
	assert.Empty(t, pinnedAfterPlainReload,
		"a plain disk Reload before any appdef.* call must not engage the definition control plane")

	// --- AFTER: AppDefCurrent pins the session. ---
	d0, err := reg.AppDefCurrent(ctx, sid)
	require.NoError(t, err)

	reg.mu.Lock()
	pinnedAfterCurrent := e.pinnedDigest
	reg.mu.Unlock()
	assert.Equal(t, d0.Digest, pinnedAfterCurrent, "AppDefCurrent must pin the entry to the digest it returns")

	// A materially different disk edit — the pinned revision's own bytes must
	// no longer match, so the guard has real content to re-capture.
	require.NoError(t, os.WriteFile(appPath, append([]byte("# second edit, after pinning\n"), []byte(minimalStory)...), 0o644))

	prevExists2, err := reg.Reload(ctx, sid)
	require.NoError(t, err, "a pinned session's plain Reload must NOT be refused for an on-disk story")
	assert.True(t, prevExists2)

	revs, err := reg.AppDefRevisions(ctx, sid)
	require.NoError(t, err)
	require.Len(t, revs, 2, "the post-pin disk edit must be captured as its own new revision")
	assert.NotEqual(t, d0.Digest, revs[0].Digest, "newest revision must be the freshly captured disk content")
	assert.Equal(t, d0.Digest, revs[1].Digest)

	reg.mu.Lock()
	pinnedAfterPinnedReload := e.pinnedDigest
	reg.mu.Unlock()
	assert.Equal(t, revs[0].Digest, pinnedAfterPinnedReload, "the pin must move onto the newly captured revision")
}

// TestSessionRegistry_Staleness_PinnedDigestGuard closes the reviewer's
// finding that Staleness's pinnedDigest guard (registry.go's Staleness,
// ~line 2563) has no test in either direction. See
// TestSessionRegistry_Reload_PinnedDigestGuard's doc comment for why the
// guard's actual behaviour is NOT the slice-6 spec's original "always
// (false, "") once pinned" plan — that plan was superseded (registry.go's
// own Staleness doc comment) because it would silently lie the moment disk
// diverged from the revision a pinned session is actually serving. The
// shipped guard instead diffs disk against the PINNED REVISION's own entry
// bytes once pinned, rather than against e.loadedContent (the last disk read
// from BEFORE the session pinned).
//
// Both directions, proven here:
//   - BEFORE any appdef.* call, Staleness compares disk against
//     e.loadedContent exactly as an ordinary, never-pinned session does.
//   - AFTER an AppDefCurrent call pins the session, Staleness compares disk
//     against the PINNED revision's bytes: a disk edit that lands AFTER
//     pinning is reported genuinely stale (never an unconditional "false" by
//     definition-plane coincidence), and re-pinning via Reload clears it.
func TestSessionRegistry_Staleness_PinnedDigestGuard(t *testing.T) {
	ctx := context.Background()
	storiesDir, appPath := writeStory(t, "mini", []byte(minimalStory))
	reg := NewRegistry(webconfig.WebConfig{}, []string{storiesDir}, deterministicBase(t))
	t.Cleanup(reg.Close)
	_, err := reg.Rescan()
	require.NoError(t, err)

	sid, err := reg.NewSession(ctx, appPath)
	require.NoError(t, err)

	// --- BEFORE: no appdef.* call yet — an ordinary disk-vs-loaded-content
	// staleness check. ---
	stale, diff, err := reg.Staleness(ctx, sid)
	require.NoError(t, err)
	assert.False(t, stale)
	assert.Empty(t, diff)

	require.NoError(t, os.WriteFile(appPath, append([]byte("# edit before pinning\n"), []byte(minimalStory)...), 0o644))
	stale, diff, err = reg.Staleness(ctx, sid)
	require.NoError(t, err)
	assert.True(t, stale, "an unpinned session must report a disk edit as stale")
	assert.NotEmpty(t, diff)

	// Reload to clear staleness, then read AppDefCurrent to pin the session.
	_, err = reg.Reload(ctx, sid)
	require.NoError(t, err)
	_, err = reg.AppDefCurrent(ctx, sid)
	require.NoError(t, err)

	stale, diff, err = reg.Staleness(ctx, sid)
	require.NoError(t, err)
	assert.False(t, stale, "immediately after pinning, disk matches the pinned revision's own bytes")
	assert.Empty(t, diff)

	// --- AFTER: pinned. A disk edit now must diff against the PINNED
	// revision's bytes, not e.loadedContent, and must NOT be reported as an
	// unconditional "not stale". ---
	require.NoError(t, os.WriteFile(appPath, append([]byte("# edit after pinning\n"), []byte(minimalStory)...), 0o644))
	stale, diff, err = reg.Staleness(ctx, sid)
	require.NoError(t, err)
	assert.True(t, stale, "a pinned session must report a disk edit against the PINNED revision as stale")
	assert.NotEmpty(t, diff)

	// A plain Reload for a pinned session re-captures disk (see
	// TestSessionRegistry_Reload_PinnedDigestGuard) and moves the pin, which
	// clears the staleness this test just proved.
	_, err = reg.Reload(ctx, sid)
	require.NoError(t, err)
	stale, diff, err = reg.Staleness(ctx, sid)
	require.NoError(t, err)
	assert.False(t, stale, "after re-capturing, the pinned revision matches disk again")
	assert.Empty(t, diff)
}
