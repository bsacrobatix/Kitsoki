package appdef_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/appdef"
)

// minimalAppYAML is the tiny valid closure every test in this package
// compiles: one terminal state, no hosts, no agents — the smallest manifest
// app.Load accepts (mirrors internal/app/loader_agent_plugins_test.go's
// minimalApp). version is a %s slot so tests can produce distinct,
// still-valid byte content for successive patches.
const minimalAppYAML = `
app:
  id: appdef-test
  version: %s
root: idle
states:
  idle:
    terminal: true
`

// minimalClosure returns a fresh, valid Closure at the given version.
func minimalClosure(version string) appdef.Closure {
	return appdef.Closure{
		Entry: "app.yaml",
		Files: map[string][]byte{
			"app.yaml": []byte(fmt.Sprintf(minimalAppYAML, version)),
		},
	}
}

// failMarker, when present in any file's content, makes fakeLoader refuse to
// compile — the injected "this candidate does not load" case, standing in
// for a real YAML syntax error without needing to hand-craft one.
const failMarker = "APPDEF_TEST_FORCE_COMPILE_FAILURE"

// fakeLoader wraps appdef.DefaultLoader{} so every successful compile is a
// REAL compile through app.LoadFromFiles (no shortcuts), while letting a
// test force a compile failure by embedding failMarker in a file's content.
// It also counts Load calls so refcounting/materialisation tests can assert
// exactly how many times the underlying loader actually ran.
type fakeLoader struct {
	mu        sync.Mutex
	loadCalls int
}

func (f *fakeLoader) Load(ctx context.Context, c appdef.Closure) (*appdef.Handle, error) {
	f.mu.Lock()
	f.loadCalls++
	f.mu.Unlock()

	for _, b := range c.Files {
		if strings.Contains(string(b), failMarker) {
			return nil, fmt.Errorf("fakeLoader: refusing to compile: %s present", failMarker)
		}
	}
	return appdef.DefaultLoader{}.Load(ctx, c)
}

func (f *fakeLoader) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadCalls
}

func newTestService(t *testing.T) (*appdef.Service, *fakeLoader) {
	t.Helper()
	fl := &fakeLoader{}
	svc := appdef.NewService(appdef.NewMemStorage(), appdef.WithLoader(fl))
	return svc, fl
}

func TestService_Capture_RefusesUncompilable(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	broken := appdef.Closure{
		Entry: "app.yaml",
		Files: map[string][]byte{"app.yaml": []byte(failMarker)},
	}
	_, err := svc.Capture(ctx, broken)
	require.Error(t, err)

	revs, err := svc.Revisions(ctx, "")
	require.NoError(t, err)
	require.Empty(t, revs, "an uncompilable closure must never be stored")
}

func TestService_Capture_IdempotentOnIdenticalBytes(t *testing.T) {
	ctx := context.Background()
	svc, fl := newTestService(t)

	c := minimalClosure("0.1.0")
	rev1, err := svc.Capture(ctx, c)
	require.NoError(t, err)

	callsAfterFirst := fl.calls()

	rev2, err := svc.Capture(ctx, c)
	require.NoError(t, err)
	require.Equal(t, rev1.Digest, rev2.Digest)

	revs, err := svc.Revisions(ctx, "")
	require.NoError(t, err)
	require.Len(t, revs, 1, "re-capturing identical bytes must not create a second stored revision")

	// Re-capture still compiles a probe (Capture always validates before
	// consulting storage) but must not have stored a duplicate.
	require.GreaterOrEqual(t, fl.calls(), callsAfterFirst)
}

func TestService_Patch_ProducesNewRevisionWithParent(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	base, err := svc.Capture(ctx, minimalClosure("0.1.0"))
	require.NoError(t, err)
	require.Equal(t, appdef.SourceCapture, base.Source)
	require.Empty(t, base.ParentDigest)

	result, err := svc.Patch(ctx, base.Digest, []appdef.Op{
		{Op: appdef.OpSetFile, Path: "app.yaml", Content: fmt.Sprintf(minimalAppYAML, "0.2.0")},
	})
	require.NoError(t, err)
	require.False(t, result.Rejected, "rejects: %+v", result.Rejects)

	require.NotEqual(t, base.Digest, result.Revision.Digest)
	require.Equal(t, base.Digest, result.Revision.ParentDigest)
	require.Equal(t, appdef.SourcePatch, result.Revision.Source)
	require.Equal(t, base.AppID, result.Revision.AppID)

	revs, err := svc.Revisions(ctx, base.AppID)
	require.NoError(t, err)
	require.Len(t, revs, 2)
	require.Equal(t, result.Revision.Digest, revs[0].Digest, "newest first")
}

func TestService_Patch_RejectsEachCode(t *testing.T) {
	ctx := context.Background()

	t.Run(appdef.RejectUnknownBase, func(t *testing.T) {
		svc, _ := newTestService(t)
		result, err := svc.Patch(ctx, "sha256:does-not-exist", []appdef.Op{
			{Op: appdef.OpSetFile, Path: "app.yaml", Content: "x"},
		})
		require.NoError(t, err)
		require.True(t, result.Rejected)
		require.Len(t, result.Rejects, 1)
		require.Equal(t, appdef.RejectUnknownBase, result.Rejects[0].Code)
	})

	t.Run(appdef.RejectNoOps, func(t *testing.T) {
		svc, _ := newTestService(t)
		base, err := svc.Capture(ctx, minimalClosure("0.1.0"))
		require.NoError(t, err)

		result, err := svc.Patch(ctx, base.Digest, nil)
		require.NoError(t, err)
		require.True(t, result.Rejected)
		require.Len(t, result.Rejects, 1)
		require.Equal(t, appdef.RejectNoOps, result.Rejects[0].Code)
	})

	t.Run(appdef.RejectUnknownOp, func(t *testing.T) {
		svc, _ := newTestService(t)
		base, err := svc.Capture(ctx, minimalClosure("0.1.0"))
		require.NoError(t, err)

		result, err := svc.Patch(ctx, base.Digest, []appdef.Op{
			{Op: "delete_file", Path: "app.yaml"},
		})
		require.NoError(t, err)
		require.True(t, result.Rejected)
		require.Len(t, result.Rejects, 1)
		require.Equal(t, appdef.RejectUnknownOp, result.Rejects[0].Code)
	})

	t.Run(appdef.RejectBadPath, func(t *testing.T) {
		svc, _ := newTestService(t)
		base, err := svc.Capture(ctx, minimalClosure("0.1.0"))
		require.NoError(t, err)

		result, err := svc.Patch(ctx, base.Digest, []appdef.Op{
			{Op: appdef.OpSetFile, Path: "../escape.yaml", Content: "x"},
		})
		require.NoError(t, err)
		require.True(t, result.Rejected)
		require.Len(t, result.Rejects, 1)
		require.Equal(t, appdef.RejectBadPath, result.Rejects[0].Code)
	})

	t.Run("bad_path_reports_every_op", func(t *testing.T) {
		svc, _ := newTestService(t)
		base, err := svc.Capture(ctx, minimalClosure("0.1.0"))
		require.NoError(t, err)

		result, err := svc.Patch(ctx, base.Digest, []appdef.Op{
			{Op: appdef.OpSetFile, Path: "/absolute.yaml", Content: "x"},
			{Op: appdef.OpSetFile, Path: "has\\backslash.yaml", Content: "x"},
			{Op: "not_a_real_op", Path: "whatever.yaml"},
		})
		require.NoError(t, err)
		require.True(t, result.Rejected)
		require.Len(t, result.Rejects, 3, "every bad op must be reported, not just the first")
	})

	t.Run(appdef.RejectNoChange, func(t *testing.T) {
		svc, _ := newTestService(t)
		content := fmt.Sprintf(minimalAppYAML, "0.1.0")
		base, err := svc.Capture(ctx, appdef.Closure{
			Entry: "app.yaml",
			Files: map[string][]byte{"app.yaml": []byte(content)},
		})
		require.NoError(t, err)

		result, err := svc.Patch(ctx, base.Digest, []appdef.Op{
			{Op: appdef.OpSetFile, Path: "app.yaml", Content: content},
		})
		require.NoError(t, err)
		require.True(t, result.Rejected)
		require.Len(t, result.Rejects, 1)
		require.Equal(t, appdef.RejectNoChange, result.Rejects[0].Code)
	})

	t.Run(appdef.RejectEntryRemoved, func(t *testing.T) {
		// entry_removed is unreachable through set_file alone starting from
		// a compiled base (set_file only adds/overwrites, never deletes,
		// and Capture refuses to store a base whose Entry isn't among its
		// own Files because that base could never have compiled). Exercise
		// the defensive check directly by storing a base revision whose
		// Entry does not appear in its own Files, bypassing Capture.
		store := appdef.NewMemStorage()
		svc := appdef.NewService(store, appdef.WithLoader(&fakeLoader{}))

		require.NoError(t, store.Put(ctx, appdef.Revision{
			Schema: appdef.RevisionSchema,
			Digest: "sha256:missing-entry-base",
			Entry:  "app.yaml",
			Files:  map[string][]byte{"other.yaml": []byte("unrelated")},
			Source: appdef.SourceCapture,
		}))

		result, err := svc.Patch(ctx, "sha256:missing-entry-base", []appdef.Op{
			{Op: appdef.OpSetFile, Path: "other.yaml", Content: "changed"},
		})
		require.NoError(t, err)
		require.True(t, result.Rejected)
		require.Len(t, result.Rejects, 1)
		require.Equal(t, appdef.RejectEntryRemoved, result.Rejects[0].Code)
		require.Equal(t, "app.yaml", result.Rejects[0].File)
	})

	t.Run(appdef.RejectCompileFailed, func(t *testing.T) {
		svc, _ := newTestService(t)
		base, err := svc.Capture(ctx, minimalClosure("0.1.0"))
		require.NoError(t, err)

		result, err := svc.Patch(ctx, base.Digest, []appdef.Op{
			{Op: appdef.OpSetFile, Path: "app.yaml", Content: failMarker},
		})
		require.NoError(t, err)
		require.True(t, result.Rejected)
		require.Len(t, result.Rejects, 1)
		require.Equal(t, appdef.RejectCompileFailed, result.Rejects[0].Code)
		require.Equal(t, "app.yaml", result.Rejects[0].File)
		require.NotEmpty(t, result.Rejects[0].Message)

		// A rejected patch must not have moved the revision list forward.
		revs, err := svc.Revisions(ctx, base.AppID)
		require.NoError(t, err)
		require.Len(t, revs, 1)
	})
}

func TestService_Patch_NeverMutatesBase(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	base, err := svc.Capture(ctx, minimalClosure("0.1.0"))
	require.NoError(t, err)

	result, err := svc.Patch(ctx, base.Digest, []appdef.Op{
		{Op: appdef.OpSetFile, Path: "app.yaml", Content: fmt.Sprintf(minimalAppYAML, "0.2.0")},
	})
	require.NoError(t, err)
	require.False(t, result.Rejected)

	revs, err := svc.Revisions(ctx, base.AppID)
	require.NoError(t, err)
	var reGot appdef.Revision
	found := false
	for _, r := range revs {
		if r.Digest == base.Digest {
			reGot = r
			found = true
		}
	}
	require.True(t, found)
	require.Equal(t, base.Files, reGot.Files)
	require.Equal(t, base.Digest, reGot.Digest)
}

func TestService_Checkout_RefcountsPerDigest(t *testing.T) {
	ctx := context.Background()
	svc, fl := newTestService(t)

	rev, err := svc.Capture(ctx, minimalClosure("0.1.0"))
	require.NoError(t, err)

	callsAfterCapture := fl.calls()

	h1, err := svc.Checkout(ctx, rev.Digest)
	require.NoError(t, err)
	require.Equal(t, callsAfterCapture+1, fl.calls(), "first Checkout of a digest must compile exactly once")
	dir := h1.Def.BaseDir
	requireDirExists(t, dir)

	h2, err := svc.Checkout(ctx, rev.Digest)
	require.NoError(t, err)
	require.Equal(t, callsAfterCapture+1, fl.calls(), "a second Checkout of the SAME digest must share the first materialisation, not recompile")
	require.Equal(t, dir, h2.Def.BaseDir)

	h1.Release()
	requireDirExists(t, dir) // h2 still holds a reference

	h2.Release()
	requireDirGone(t, dir) // last reference released: tree retired
}

func TestService_Checkout_UnknownDigest(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	_, err := svc.Checkout(ctx, "sha256:nope")
	require.ErrorIs(t, err, appdef.ErrUnknownRevision)
}

func requireDirExists(t *testing.T, dir string) {
	t.Helper()
	_, err := os.Stat(dir)
	require.NoError(t, err, "expected %s to still exist", dir)
}

func requireDirGone(t *testing.T, dir string) {
	t.Helper()
	_, err := os.Stat(dir)
	require.True(t, os.IsNotExist(err), "expected %s to be removed, stat err = %v", dir, err)
}
