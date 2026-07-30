package appdef_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/appdef"
)

// fakeBinder is a SessionBinder test double. It reports whatever entry/files
// Closure holds (mutable per test), records every SwapDef/Reenter call, and
// lets a test inject a SwapDef/Reenter failure on demand.
type fakeBinder struct {
	mu sync.Mutex

	entry string
	files map[string][]byte

	swapErr         error
	prevStateExists bool
	reenterErr      error

	// appID is what AppID() reports — the session's OWN current
	// application ID, exactly as orchestrator.SessionBinding.AppID reads it
	// off the live def with no compile. "" (the default) mirrors a session
	// whose live def has an empty/unset App.ID, or simply skips the
	// cross-application guard test-by-test where it isn't the point.
	appID string

	swapCalls    int
	swapped      []*app.AppDef
	reenterCalls int
}

func (f *fakeBinder) Closure() (string, map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entry, f.files, nil
}

func (f *fakeBinder) SwapDef(_ context.Context, def *app.AppDef) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.swapCalls++
	if f.swapErr != nil {
		return false, f.swapErr
	}
	f.swapped = append(f.swapped, def)
	return f.prevStateExists, nil
}

func (f *fakeBinder) Reenter(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reenterCalls++
	return f.reenterErr
}

func (f *fakeBinder) AppID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appID
}

func (f *fakeBinder) setAppID(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appID = id
}

func (f *fakeBinder) setSwapErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.swapErr = err
}

func (f *fakeBinder) lastSwapped() *app.AppDef {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.swapped) == 0 {
		return nil
	}
	return f.swapped[len(f.swapped)-1]
}

func newTestBinding(t *testing.T) (*appdef.Binding, *appdef.Service, *fakeBinder, *appdef.Gate) {
	t.Helper()
	svc, _ := newTestService(t)
	binder := &fakeBinder{
		entry: "app.yaml",
		files: map[string][]byte{"app.yaml": []byte(fmt.Sprintf(minimalAppYAML, "0.1.0"))},
	}
	gate := appdef.NewGate()
	binding := appdef.NewBinding(svc, binder, gate)
	return binding, svc, binder, gate
}

func TestBinding_Current_CapturesOnFirstCallOnly(t *testing.T) {
	ctx := context.Background()
	binding, svc, _, _ := newTestBinding(t)

	rev1, err := binding.Current(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, rev1.Digest)

	rev2, err := binding.Current(ctx)
	require.NoError(t, err)
	require.Equal(t, rev1.Digest, rev2.Digest)

	revs, err := svc.Revisions(ctx, rev1.AppID)
	require.NoError(t, err)
	require.Len(t, revs, 1, "Current must capture at most one revision across repeated calls")
}

func TestBinding_Patch_StaleBaseRejected(t *testing.T) {
	ctx := context.Background()
	binding, _, _, _ := newTestBinding(t)

	current, err := binding.Current(ctx)
	require.NoError(t, err)

	result, err := binding.Patch(ctx, "sha256:not-the-current-digest", []appdef.Op{
		{Op: appdef.OpSetFile, Path: "app.yaml", Content: fmt.Sprintf(minimalAppYAML, "0.2.0")},
	})
	require.NoError(t, err)
	require.True(t, result.Rejected)
	require.Len(t, result.Rejects, 1)
	require.Equal(t, appdef.RejectStaleBase, result.Rejects[0].Code)

	// "" (unspecified base) means "whatever is current" and must succeed.
	result2, err := binding.Patch(ctx, "", []appdef.Op{
		{Op: appdef.OpSetFile, Path: "app.yaml", Content: fmt.Sprintf(minimalAppYAML, "0.2.0")},
	})
	require.NoError(t, err)
	require.False(t, result2.Rejected, "rejects: %+v", result2.Rejects)
	require.Equal(t, current.Digest, result2.Revision.ParentDigest)
}

func TestBinding_ReloadTo_SwapFailureKeepsOldHandle(t *testing.T) {
	ctx := context.Background()
	binding, svc, binder, _ := newTestBinding(t)

	// Establish a root pin so there is a "previously served handle" for the
	// failed reload below to leave untouched.
	root, err := binding.Current(ctx)
	require.NoError(t, err)

	binder.prevStateExists = true
	prevExists, err := binding.ReloadTo(ctx, root.Digest)
	require.NoError(t, err)
	require.True(t, prevExists)

	served := binder.lastSwapped()
	require.NotNil(t, served)
	requireDirExists(t, served.BaseDir)

	// Produce a second, different revision to reload onto.
	patchResult, err := svc.Patch(ctx, root.Digest, []appdef.Op{
		{Op: appdef.OpSetFile, Path: "app.yaml", Content: fmt.Sprintf(minimalAppYAML, "0.9.0")},
	})
	require.NoError(t, err)
	require.False(t, patchResult.Rejected)

	// Now make SwapDef fail and attempt to reload onto it.
	boom := errors.New("boom: swap refused")
	binder.setSwapErr(boom)

	prevExists2, err := binding.ReloadTo(ctx, patchResult.Revision.Digest)
	require.ErrorIs(t, err, boom)
	require.False(t, prevExists2)

	// The pin must not have moved: Current still reports the root digest.
	current, err := binding.Current(ctx)
	require.NoError(t, err)
	require.Equal(t, root.Digest, current.Digest)

	// The previously served handle must still be live — its materialised
	// tree must not have been torn down by the failed reload.
	requireDirExists(t, served.BaseDir)
}

func TestBinding_ReloadTo_ReenterFailureDoesNotRollBack(t *testing.T) {
	ctx := context.Background()
	binding, svc, binder, _ := newTestBinding(t)

	root, err := binding.Current(ctx)
	require.NoError(t, err)

	patchResult, err := svc.Patch(ctx, root.Digest, []appdef.Op{
		{Op: appdef.OpSetFile, Path: "app.yaml", Content: fmt.Sprintf(minimalAppYAML, "0.5.0")},
	})
	require.NoError(t, err)
	require.False(t, patchResult.Rejected)

	binder.prevStateExists = true
	binder.reenterErr = errors.New("reenter failed")

	prevExists, err := binding.ReloadTo(ctx, patchResult.Revision.Digest)
	require.Error(t, err, "a Reenter failure must be reported")
	require.True(t, prevExists, "prevStateExists is still reported even when Reenter fails")

	// The swap itself is NOT rolled back: Current must report the NEW digest.
	current, err := binding.Current(ctx)
	require.NoError(t, err)
	require.Equal(t, patchResult.Revision.Digest, current.Digest)
}

func TestBinding_ReloadTo_UnknownDigest(t *testing.T) {
	ctx := context.Background()
	binding, _, binder, _ := newTestBinding(t)

	_, err := binding.ReloadTo(ctx, "sha256:does-not-exist")
	require.ErrorIs(t, err, appdef.ErrUnknownRevision)
	require.Equal(t, 0, binder.swapCalls, "an unknown digest must never reach SwapDef")
}

func TestBinding_ReloadTo_RefusedWhileTurnInFlight(t *testing.T) {
	ctx := context.Background()
	binding, _, _, gate := newTestBinding(t)

	root, err := binding.Current(ctx)
	require.NoError(t, err)

	gate.BeginTurn()
	defer gate.EndTurn()

	_, err = binding.ReloadTo(ctx, root.Digest)
	require.ErrorIs(t, err, appdef.ErrTurnInFlight)
}

// TestBinding_ReloadTo_RefusesForeignAppIDOnFirstCall proves the
// cross-application guard fires on a session's VERY FIRST ReloadTo call —
// with no prior Current()/ReloadTo() for this Binding — because AppID()
// reads the session's own live def, not a cache that only a prior call
// would have populated. This is the exact gap the implementation spec's
// Open Risks item 10 named and left open when ReloadTo's guard depended on
// a Binding-local cache instead.
func TestBinding_ReloadTo_RefusesForeignAppIDOnFirstCall(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	// One Service, shared across the whole registry in production, holds a
	// revision for a DIFFERENT application.
	foreign, err := svc.Capture(ctx, appdef.Closure{
		Entry: "app.yaml",
		Files: map[string][]byte{"app.yaml": []byte(fmt.Sprintf(`
app:
  id: other-app
  version: %s
root: idle
states:
  idle:
    terminal: true
`, "0.1.0"))},
	})
	require.NoError(t, err)
	require.Equal(t, "other-app", foreign.AppID)

	binder := &fakeBinder{
		entry: "app.yaml",
		files: map[string][]byte{"app.yaml": []byte(fmt.Sprintf(minimalAppYAML, "0.1.0"))},
		appID: "appdef-test", // the session's OWN live AppID, known for free
	}
	gate := appdef.NewGate()
	binding := appdef.NewBinding(svc, binder, gate)

	// No binding.Current(ctx) call before this — this IS the session's
	// first appdef interaction — and the guard must still fire.
	_, err = binding.ReloadTo(ctx, foreign.Digest)
	require.Error(t, err)
	require.Contains(t, err.Error(), "other-app")
	require.Equal(t, 0, binder.swapCalls, "a cross-application revision must be refused before SwapDef")
}

// TestBinding_Close_ReleasesServedHandle proves Close tears down the
// materialised tree behind whatever revision this session was moved onto —
// the fix for the leak where nothing released a session's served handle on
// eviction (cleanupEvicted/SessionRegistry.Close touched e.sink/e.rt but
// never e.binding), permanently leaking a temp dir and a Service refcount
// entry per reloaded session.
func TestBinding_Close_ReleasesServedHandle(t *testing.T) {
	ctx := context.Background()
	binding, _, binder, _ := newTestBinding(t)

	root, err := binding.Current(ctx)
	require.NoError(t, err)
	binder.prevStateExists = true

	_, err = binding.ReloadTo(ctx, root.Digest)
	require.NoError(t, err)

	served := binder.lastSwapped()
	require.NotNil(t, served)
	requireDirExists(t, served.BaseDir)

	binding.Close()
	requireDirGone(t, served.BaseDir)

	// Idempotent: a second Close must not panic or double-release.
	binding.Close()
}
