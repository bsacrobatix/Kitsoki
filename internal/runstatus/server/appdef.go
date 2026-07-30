// appdef.go — the definition control-plane write RPCs: read the revision a
// session is serving, patch it into a NEW immutable revision, and list stored
// revisions. Paired with the `revision` param on runstatus.session.reload
// (server.go), this is the fix-a-running-application path: a client repairs a
// definition and moves a live session onto the repaired revision with no
// restart and no out-of-band file write.
//
// The seam is [AppDefProvider]: an optional capability a [SessionProvider] may
// implement (the live multi-session registry does; read-only adapters do
// not). When the provider does not implement it, every appdef RPC returns
// codeReadOnly — the same fallback [EditorProvider] uses.
//
// Validation failure is NOT an rpcError. A patch that does not compile
// returns 200 with {rejected, reject_reasons, reject_details} — the same
// contract graph.propose/apply use (graph_rpc.go), so a client has ONE shape
// to handle for "the plane refused my edit", and rpcError stays reserved for
// operational faults.
//
// This file declares plain wire types rather than importing internal/appdef:
// the server must not depend on the control-plane package, so the provider
// (cmd/kitsoki's SessionRegistry) maps appdef.Revision / appdef.Reject onto
// AppDefRevision / AppDefReject at the boundary — the same inversion
// StoryHeader / AgentInfo already use elsewhere in this package.
package server

import (
	"context"
	"strconv"
	"time"
)

// AppDefProvider is the optional capability a [SessionProvider] implements to
// back the definition control-plane RPCs. A provider that does not implement
// it makes every runstatus.appdef.* method (and the revision-targeted form of
// runstatus.session.reload) return [codeReadOnly] — the same fallback
// [EditorProvider] uses when a surface has no live orchestrator behind it.
type AppDefProvider interface {
	// AppDefCurrent returns the revision the session is serving, capturing and
	// storing it as a root revision on first call.
	AppDefCurrent(ctx context.Context, sessionID string) (AppDefRevision, error)
	// AppDefPatch applies ops to the session's current revision and stores the
	// compiled result as a NEW immutable revision. It does NOT move the
	// session — moving is the separate runstatus.session.reload{revision} call.
	AppDefPatch(ctx context.Context, sessionID, baseDigest string, ops []AppDefPatchOp) (AppDefPatchResult, error)
	// AppDefRevisions lists stored revisions for the session's application,
	// newest first.
	AppDefRevisions(ctx context.Context, sessionID string) ([]AppDefRevision, error)
	// AppDefReload moves the session onto the revision named by digest,
	// keeping its live world state. err wraps [ErrSessionBusy] when the swap
	// is refused because a turn is in flight or another swap is already
	// running.
	AppDefReload(ctx context.Context, sessionID, digest string) (prevStateExists bool, err error)
}

// AppDefRevision is the wire projection of an appdef.Revision's header (no
// file bytes — Files is the sorted list of closure-relative paths, matching
// appdef.Header).
type AppDefRevision struct {
	Schema       string    `json:"schema"`
	Digest       string    `json:"digest"`
	AppID        string    `json:"app_id,omitempty"`
	Entry        string    `json:"entry"`
	ParentDigest string    `json:"parent_digest,omitempty"`
	Source       string    `json:"source"`
	CreatedAt    time.Time `json:"created_at"`
	Files        []string  `json:"files"`
	TotalBytes   int       `json:"total_bytes"`
}

// AppDefPatchOp is the wire projection of an appdef.Op: one typed definition
// mutation. The vocabulary is deliberately tiny — see internal/appdef's
// OpSetFile.
type AppDefPatchOp struct {
	Op      string `json:"op"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// AppDefReject is the wire projection of an appdef.Reject: one named reason a
// patch was refused.
type AppDefReject struct {
	Code    string `json:"code"`
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}

// AppDefPatchResult is the wire projection of an appdef.PatchResult. The json
// tags on Rejects/Revision are "-": dispatchAppDef builds the wire map by
// hand (mirroring graph_rpc.go's graphApplyResultWire) rather than relying on
// struct marshalling, so a rejection always carries reject_reasons /
// reject_details / revision:null and a success always carries an empty
// reject_reasons / reject_details — one predictable shape either way.
type AppDefPatchResult struct {
	Rejected bool            `json:"rejected"`
	Rejects  []AppDefReject  `json:"-"`
	Revision *AppDefRevision `json:"-"`
}

// appDefProvider returns the provider's AppDefProvider capability, or false.
func (s *Server) appDefProvider() (AppDefProvider, bool) {
	ap, ok := s.provider.(AppDefProvider)
	return ap, ok
}

// dispatchAppDef handles the runstatus.appdef.* method family. It returns
// (result, nil, true) when it handled the method, or (nil, nil, false) when
// the method is not an appdef method (so the caller's dispatch chain
// continues). The third return is "handled".
func (s *Server) dispatchAppDef(ctx context.Context, method string, params map[string]any) (any, *rpcError, bool) {
	switch method {
	// runstatus.appdef.current {session_id} -> {revision}
	case "runstatus.appdef.current":
		ap, ok := s.appDefProvider()
		if !ok {
			return nil, readOnlyErr("appdef"), true
		}
		sid, rerr := sessionIDParam(params)
		if rerr != nil {
			return nil, rerr, true
		}
		rev, err := ap.AppDefCurrent(ctx, sid)
		if err != nil {
			return nil, lifecycleOrBusyErr(err), true
		}
		return map[string]any{"revision": rev}, nil, true

	// runstatus.appdef.patch {session_id, ops[, base_digest]} ->
	//   {rejected, reject_reasons, reject_details, revision?}
	case "runstatus.appdef.patch":
		ap, ok := s.appDefProvider()
		if !ok {
			return nil, readOnlyErr("appdef"), true
		}
		sid, rerr := sessionIDParam(params)
		if rerr != nil {
			return nil, rerr, true
		}
		baseDigest, _ := params["base_digest"].(string)
		ops, rejected := parseAppDefPatchOps(params["ops"])
		if rejected != nil {
			return appDefPatchResultWire(AppDefPatchResult{Rejected: true, Rejects: []AppDefReject{*rejected}}), nil, true
		}
		res, err := ap.AppDefPatch(ctx, sid, baseDigest, ops)
		if err != nil {
			return nil, lifecycleOrBusyErr(err), true
		}
		return appDefPatchResultWire(res), nil, true

	// runstatus.appdef.revisions {session_id} -> {revisions: [...]}
	case "runstatus.appdef.revisions":
		ap, ok := s.appDefProvider()
		if !ok {
			return nil, readOnlyErr("appdef"), true
		}
		sid, rerr := sessionIDParam(params)
		if rerr != nil {
			return nil, rerr, true
		}
		revs, err := ap.AppDefRevisions(ctx, sid)
		if err != nil {
			return nil, lifecycleOrBusyErr(err), true
		}
		if revs == nil {
			revs = []AppDefRevision{}
		}
		return map[string]any{"revisions": revs}, nil, true

	default:
		return nil, nil, false
	}
}

// parseAppDefPatchOps reads the ops param ([]any of objects) into
// []AppDefPatchOp. A missing/empty ops, a non-array ops, or a non-object
// element is reported as a *AppDefReject rather than an error: the client
// sees the same {rejected: true, ...} shape a compile-time rejection returns,
// so there is exactly one failure shape for "the plane refused my edit".
func parseAppDefPatchOps(raw any) ([]AppDefPatchOp, *AppDefReject) {
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil, &AppDefReject{Code: "no_ops", Message: "appdef.patch: missing or empty 'ops'"}
	}
	ops := make([]AppDefPatchOp, 0, len(arr))
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, &AppDefReject{Code: "bad_op", Message: appDefBadOpIndexMessage(i, "ops entry is not an object")}
		}
		op, _ := obj["op"].(string)
		path, _ := obj["path"].(string)
		content, _ := obj["content"].(string)
		ops = append(ops, AppDefPatchOp{Op: op, Path: path, Content: content})
	}
	return ops, nil
}

func appDefBadOpIndexMessage(i int, why string) string {
	return "appdef.patch: ops[" + strconv.Itoa(i) + "]: " + why
}

// appDefPatchResultWire renders an AppDefPatchResult as the wire map, mirroring
// graph_rpc.go's graphApplyResultWire: reject_reasons and reject_details are
// ALWAYS non-nil arrays (empty on success), and revision is present only on
// success.
func appDefPatchResultWire(res AppDefPatchResult) map[string]any {
	reasons := make([]any, len(res.Rejects))
	details := make([]any, len(res.Rejects))
	for i, r := range res.Rejects {
		reasons[i] = r.Message
		details[i] = r
	}
	out := map[string]any{
		"rejected":       res.Rejected,
		"reject_reasons": reasons,
		"reject_details": details,
	}
	if !res.Rejected && res.Revision != nil {
		out["revision"] = res.Revision
	} else {
		out["revision"] = nil
	}
	return out
}
