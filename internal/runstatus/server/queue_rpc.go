// queue_rpc.go — the bare "queue.*" JSON-RPC method family: thin wire
// adapters over internal/capsule/queue's operator verbs (Kick/Park/Resume/
// MarkEmergency/Override/Reject) plus a read-only status view, wired into
// Server.dispatch through the chained-helper convention (dispatchEditor /
// dispatchKit / dispatchMaterialize). The queue package's semantics are the
// contract; this file only maps params on and errors off the wire.
package server

import (
	"context"

	"kitsoki/internal/capsule/queue"
)

// queueStore resolves the project root a queue.* call operates on: an
// explicit "project" param wins (same caller-supplied-path posture as
// graph.propose's catalog_path), else s.materializeRoot — the server's "home
// repo" root, the same fallback graphAllowlist uses — else ".".
func (s *Server) queueStore(params map[string]any) queue.Store {
	root := graphStringParam(params, "project")
	if root == "" {
		root = s.materializeRoot
	}
	if root == "" {
		root = "."
	}
	return queue.Store{ProjectRoot: root}
}

// queueOpParam builds the queue.Op (candidate id + human attribution) shared
// by every operator verb.
func queueOpParam(params map[string]any) queue.Op {
	return queue.Op{
		ID:     graphStringParam(params, "id"),
		Actor:  graphStringParam(params, "actor"),
		Reason: graphStringParam(params, "reason"),
	}
}

// dispatchQueue handles the queue.* method family. It returns (result, nil,
// true) when it handled the method, or (nil, nil, false) when the method is
// not one of this family's, so the caller can fall through to the next
// dispatcher — same convention as dispatchEditor / dispatchKit /
// dispatchMaterialize.
func (s *Server) dispatchQueue(_ context.Context, method string, params map[string]any) (any, *rpcError, bool) {
	switch method {
	case "queue.status":
		result, rerr := s.queueStatus(params)
		return result, rerr, true
	case "queue.kick":
		result, rerr := s.queueOp(method, params, queue.Store.Kick)
		return result, rerr, true
	case "queue.park":
		result, rerr := s.queueOp(method, params, queue.Store.Park)
		return result, rerr, true
	case "queue.resume":
		result, rerr := s.queueOp(method, params, queue.Store.Resume)
		return result, rerr, true
	case "queue.emergency":
		result, rerr := s.queueOp(method, params, queue.Store.MarkEmergency)
		return result, rerr, true
	case "queue.override":
		result, rerr := s.queueOp(method, params, queue.Store.Override)
		return result, rerr, true
	case "queue.reject":
		result, rerr := s.queueOp(method, params, queue.Store.Reject)
		return result, rerr, true
	default:
		return nil, nil, false
	}
}

// queueStatus: {} -> full queue.State, or {"id": <candidate>} -> that single
// queue.Candidate. The queue package's JSON shapes are returned directly —
// they are already the durable wire format (.capsules/queue/state.json).
func (s *Server) queueStatus(params map[string]any) (any, *rpcError) {
	store := s.queueStore(params)
	if id := graphStringParam(params, "id"); id != "" {
		c, err := store.Get(id)
		if err != nil {
			return nil, &rpcError{Code: codeServerError, Message: "queue.status: " + err.Error()}
		}
		return c, nil
	}
	state, err := store.List()
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "queue.status: " + err.Error()}
	}
	return state, nil
}

// queueOp: {id, actor, reason} -> the mutated queue.Candidate. verb is a
// queue.Store method expression (queue.Store.Kick etc.); every queue error —
// unknown candidate, wrong-phase verb, lock contention — comes back as
// codeServerError with the method name prefixed, matching graph_rpc's error
// mapping.
func (s *Server) queueOp(method string, params map[string]any, verb func(queue.Store, queue.Op) (queue.Candidate, error)) (any, *rpcError) {
	c, err := verb(s.queueStore(params), queueOpParam(params))
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: method + ": " + err.Error()}
	}
	return c, nil
}
