package server_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/runstatus/server"
)

// appDefStubProvider extends stubProvider with the AppDefProvider capability,
// modelled on editor_test.go's editorStubProvider: each method delegates to a
// func field so a test wires only the behaviour it asserts.
type appDefStubProvider struct {
	*stubProvider

	currentFn   func(ctx context.Context, sessionID string) (server.AppDefRevision, error)
	patchFn     func(ctx context.Context, sessionID, baseDigest string, ops []server.AppDefPatchOp) (server.AppDefPatchResult, error)
	revisionsFn func(ctx context.Context, sessionID string) ([]server.AppDefRevision, error)
	reloadFn    func(ctx context.Context, sessionID, digest string) (bool, error)
}

func (p *appDefStubProvider) AppDefCurrent(ctx context.Context, sessionID string) (server.AppDefRevision, error) {
	return p.currentFn(ctx, sessionID)
}

func (p *appDefStubProvider) AppDefPatch(ctx context.Context, sessionID, baseDigest string, ops []server.AppDefPatchOp) (server.AppDefPatchResult, error) {
	return p.patchFn(ctx, sessionID, baseDigest, ops)
}

func (p *appDefStubProvider) AppDefRevisions(ctx context.Context, sessionID string) ([]server.AppDefRevision, error) {
	return p.revisionsFn(ctx, sessionID)
}

func (p *appDefStubProvider) AppDefReload(ctx context.Context, sessionID, digest string) (bool, error) {
	return p.reloadFn(ctx, sessionID, digest)
}

func newAppDefServer(t *testing.T, p server.SessionProvider) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(server.NewMulti(p).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// appDefPatchWire is the appdef.patch (and equivalently graph-style) wire
// response shape, decoded field-by-field so a test can assert the exact
// contract described in appdef.go's doc comment.
type appDefPatchWire struct {
	Rejected      bool                   `json:"rejected"`
	RejectReasons []string               `json:"reject_reasons"`
	RejectDetails []server.AppDefReject  `json:"reject_details"`
	Revision      *server.AppDefRevision `json:"revision"`
}

func sampleAppDefRevision(digest string) server.AppDefRevision {
	return server.AppDefRevision{
		Schema:     "application-definition-revision/v1",
		Digest:     digest,
		AppID:      "liveedit",
		Entry:      "app.yaml",
		Source:     "capture",
		CreatedAt:  time.Now().UTC(),
		Files:      []string{"app.yaml"},
		TotalBytes: 42,
	}
}

// ── Capability miss ──────────────────────────────────────────────────────

// TestAppDef_CapabilityMiss proves that a SessionProvider which does not
// implement AppDefProvider (the bare stubProvider) makes every
// runstatus.appdef.* method AND the revision-targeted form of
// runstatus.session.reload answer codeReadOnly, exactly as EditorProvider's
// fallback does.
func TestAppDef_CapabilityMiss(t *testing.T) {
	t.Parallel()
	ts := newAppDefServer(t, newStubProvider())

	cases := []struct {
		method string
		params map[string]any
	}{
		{"runstatus.appdef.current", map[string]any{"session_id": "s1"}},
		{"runstatus.appdef.patch", map[string]any{"session_id": "s1", "ops": []any{}}},
		{"runstatus.appdef.revisions", map[string]any{"session_id": "s1"}},
		{"runstatus.session.reload", map[string]any{"session_id": "s1", "revision": "sha256:abc"}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			code, msg := rpcCallExpectError(t, ts, tc.method, tc.params)
			assert.Equal(t, -32001, code, "%s should return codeReadOnly", tc.method)
			assert.NotEmpty(t, msg)
		})
	}
}

// ── Happy paths ──────────────────────────────────────────────────────────

func TestAppDef_Current_HappyPath(t *testing.T) {
	t.Parallel()
	want := sampleAppDefRevision("sha256:d0")
	p := &appDefStubProvider{
		stubProvider: newStubProvider(),
		currentFn: func(ctx context.Context, sessionID string) (server.AppDefRevision, error) {
			assert.Equal(t, "s1", sessionID)
			return want, nil
		},
	}
	ts := newAppDefServer(t, p)

	var out struct {
		Revision server.AppDefRevision `json:"revision"`
	}
	rpcCall(t, ts, "runstatus.appdef.current", map[string]any{"session_id": "s1"}, &out)
	assert.Equal(t, want.Digest, out.Revision.Digest)
	assert.Equal(t, want.Entry, out.Revision.Entry)
	assert.Equal(t, want.AppID, out.Revision.AppID)
	assert.Equal(t, []string{"app.yaml"}, out.Revision.Files)
}

func TestAppDef_Revisions_HappyPath(t *testing.T) {
	t.Parallel()
	revs := []server.AppDefRevision{sampleAppDefRevision("sha256:d1"), sampleAppDefRevision("sha256:d0")}
	p := &appDefStubProvider{
		stubProvider: newStubProvider(),
		revisionsFn: func(ctx context.Context, sessionID string) ([]server.AppDefRevision, error) {
			return revs, nil
		},
	}
	ts := newAppDefServer(t, p)

	var out struct {
		Revisions []server.AppDefRevision `json:"revisions"`
	}
	rpcCall(t, ts, "runstatus.appdef.revisions", map[string]any{"session_id": "s1"}, &out)
	require.Len(t, out.Revisions, 2)
	assert.Equal(t, "sha256:d1", out.Revisions[0].Digest)
	assert.Equal(t, "sha256:d0", out.Revisions[1].Digest)
}

func TestAppDef_Revisions_EmptyIsNonNilArray(t *testing.T) {
	t.Parallel()
	p := &appDefStubProvider{
		stubProvider: newStubProvider(),
		revisionsFn: func(ctx context.Context, sessionID string) ([]server.AppDefRevision, error) {
			return nil, nil
		},
	}
	ts := newAppDefServer(t, p)

	var raw struct {
		Revisions []server.AppDefRevision `json:"revisions"`
	}
	rpcCall(t, ts, "runstatus.appdef.revisions", map[string]any{"session_id": "s1"}, &raw)
	assert.NotNil(t, raw.Revisions)
	assert.Len(t, raw.Revisions, 0)
}

func TestAppDef_Patch_HappyPath(t *testing.T) {
	t.Parallel()
	rev := sampleAppDefRevision("sha256:d1")
	rev.ParentDigest = "sha256:d0"
	p := &appDefStubProvider{
		stubProvider: newStubProvider(),
		patchFn: func(ctx context.Context, sessionID, baseDigest string, ops []server.AppDefPatchOp) (server.AppDefPatchResult, error) {
			assert.Equal(t, "s1", sessionID)
			assert.Equal(t, "sha256:d0", baseDigest)
			require.Len(t, ops, 1)
			assert.Equal(t, "set_file", ops[0].Op)
			assert.Equal(t, "app.yaml", ops[0].Path)
			return server.AppDefPatchResult{Rejected: false, Revision: &rev}, nil
		},
	}
	ts := newAppDefServer(t, p)

	var out appDefPatchWire
	rpcCall(t, ts, "runstatus.appdef.patch", map[string]any{
		"session_id":  "s1",
		"base_digest": "sha256:d0",
		"ops": []any{
			map[string]any{"op": "set_file", "path": "app.yaml", "content": "..."},
		},
	}, &out)
	assert.False(t, out.Rejected)
	require.NotNil(t, out.Revision)
	assert.Equal(t, "sha256:d1", out.Revision.Digest)
	assert.Equal(t, "sha256:d0", out.Revision.ParentDigest)
	assert.NotNil(t, out.RejectReasons)
	assert.Empty(t, out.RejectReasons)
	assert.NotNil(t, out.RejectDetails)
	assert.Empty(t, out.RejectDetails)
}

// ── Patch rejection: 200 with a structured payload, never an rpcError ─────

// TestAppDef_Patch_RejectedIsNotAnRPCError proves the mutation-result
// contract from graph_rpc.go: a compile failure returns HTTP 200 with
// rejected: true, non-empty reject_reasons/reject_details, and no JSON-RPC
// error member. rpcCall itself asserts frame.Error == nil, so a passing call
// here is the "not an rpcError" proof.
func TestAppDef_Patch_RejectedIsNotAnRPCError(t *testing.T) {
	t.Parallel()
	p := &appDefStubProvider{
		stubProvider: newStubProvider(),
		patchFn: func(ctx context.Context, sessionID, baseDigest string, ops []server.AppDefPatchOp) (server.AppDefPatchResult, error) {
			return server.AppDefPatchResult{
				Rejected: true,
				Rejects: []server.AppDefReject{
					{Code: "compile_failed", File: "app.yaml", Message: "states: unexpected token"},
				},
			}, nil
		},
	}
	ts := newAppDefServer(t, p)

	var out appDefPatchWire
	rpcCall(t, ts, "runstatus.appdef.patch", map[string]any{
		"session_id": "s1",
		"ops": []any{
			map[string]any{"op": "set_file", "path": "app.yaml", "content": "states:\n  {{{"},
		},
	}, &out)
	assert.True(t, out.Rejected)
	require.NotEmpty(t, out.RejectReasons)
	assert.Contains(t, out.RejectReasons[0], "unexpected token")
	require.NotEmpty(t, out.RejectDetails)
	assert.Equal(t, "compile_failed", out.RejectDetails[0].Code)
	assert.Nil(t, out.Revision)
}

// TestAppDef_Patch_OpsMalformedIsRejectedNotError proves that a missing,
// non-array, or non-object-element ops param is a rejected result (code
// no_ops / bad_op), never an rpcError — one failure shape for the client to
// handle regardless of where the refusal originates.
func TestAppDef_Patch_OpsMalformedIsRejectedNotError(t *testing.T) {
	t.Parallel()
	// patchFn must never be invoked: malformed ops are rejected before the
	// provider is called.
	called := false
	p := &appDefStubProvider{
		stubProvider: newStubProvider(),
		patchFn: func(ctx context.Context, sessionID, baseDigest string, ops []server.AppDefPatchOp) (server.AppDefPatchResult, error) {
			called = true
			return server.AppDefPatchResult{}, nil
		},
	}
	ts := newAppDefServer(t, p)

	cases := []struct {
		name string
		ops  any
		code string
	}{
		{"missing", nil, "no_ops"},
		{"empty_array", []any{}, "no_ops"},
		{"not_an_array", "not-an-array", "no_ops"},
		{"element_not_object", []any{"not-an-object"}, "bad_op"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{"session_id": "s1"}
			if tc.ops != nil {
				params["ops"] = tc.ops
			}
			var out appDefPatchWire
			rpcCall(t, ts, "runstatus.appdef.patch", params, &out)
			assert.True(t, out.Rejected)
			require.NotEmpty(t, out.RejectDetails)
			assert.Equal(t, tc.code, out.RejectDetails[0].Code)
			assert.Nil(t, out.Revision)
		})
	}
	assert.False(t, called, "the provider must not be called for malformed ops")
}

// ── Busy: the definition plane is refused, never silently raced ──────────

// TestAppDef_Reload_Busy proves a provider wrapping server.ErrSessionBusy
// around a refused swap surfaces as codeBusy (-32005) through
// runstatus.session.reload{revision}.
func TestAppDef_Reload_Busy(t *testing.T) {
	t.Parallel()
	p := &appDefStubProvider{
		stubProvider: newStubProvider(),
		reloadFn: func(ctx context.Context, sessionID, digest string) (bool, error) {
			return false, fmt.Errorf("%w: turn in flight", server.ErrSessionBusy)
		},
	}
	ts := newAppDefServer(t, p)

	code, msg := rpcCallExpectError(t, ts, "runstatus.session.reload",
		map[string]any{"session_id": "s1", "revision": "sha256:d1"})
	assert.Equal(t, -32005, code)
	assert.Contains(t, msg, "turn in flight")
}

// TestAppDef_Reload_HappyPath proves the additive "revision" field on
// runstatus.session.reload's response, both with and without a revision
// param.
func TestAppDef_Reload_HappyPath(t *testing.T) {
	t.Parallel()
	p := &appDefStubProvider{
		stubProvider: newStubProvider(),
		reloadFn: func(ctx context.Context, sessionID, digest string) (bool, error) {
			assert.Equal(t, "sha256:d1", digest)
			return true, nil
		},
	}
	ts := newAppDefServer(t, p)

	var out struct {
		OK              bool   `json:"ok"`
		PrevStateExists bool   `json:"prev_state_exists"`
		Revision        string `json:"revision"`
	}
	rpcCall(t, ts, "runstatus.session.reload", map[string]any{"session_id": "s1", "revision": "sha256:d1"}, &out)
	assert.True(t, out.OK)
	assert.True(t, out.PrevStateExists)
	assert.Equal(t, "sha256:d1", out.Revision)
}
