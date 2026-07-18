// Package host — host.queue.* : the capsule merge queue's operator surface
// exposed as a host verb family so starlark glue (ctx.host.call) can inspect
// and steer the queue without shelling out to `kitsoki queue`.
//
// Seven ops, registered bare at "host.queue" (longest-prefix convention, see
// host.graph / host.demo — the registry injects the dropped suffix as
// args["op"]):
//
//	status    {[id]}                 -> full queue state, or one candidate when "id" is set
//	kick      {id[, actor, reason]}  -> clear a retry_wait candidate's backoff timer
//	park      {id[, actor, reason]}  -> move a candidate to needs_input
//	resume    {id[, actor, reason]}  -> re-queue a parked candidate with a fresh attempt budget
//	emergency {id[, actor, reason]}  -> move a candidate into the priority emergency lane
//	override  {id[, actor, reason]}  -> human immediate-merge: emergency lane + durable attributed gate waiver
//	reject    {id[, actor, reason]}  -> terminal removal; branches and evidence retained
//
// Every op resolves the queue from args["project"] (project root, default
// "."), mirroring the `kitsoki queue --project` CLI convention. Queue-level
// failures (unknown candidate, phase violations) are domain errors surfaced
// via Result.Error; only argument/infra problems return a Go error.
package host

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"kitsoki/internal/capsule/queue"
)

// QueueHandler implements the host.queue.* multi-op verb.
func QueueHandler(_ context.Context, args map[string]any) (Result, error) {
	op, _ := args["op"].(string)
	store, err := queueStoreArg(args)
	if err != nil {
		return Result{}, err
	}
	switch op {
	case "status":
		return queueStatusOp(store, args)
	case "kick":
		return queueOperatorOp(store, args, "kick", queue.Store.Kick)
	case "park":
		return queueOperatorOp(store, args, "park", queue.Store.Park)
	case "resume":
		return queueOperatorOp(store, args, "resume", queue.Store.Resume)
	case "emergency":
		return queueOperatorOp(store, args, "emergency", queue.Store.MarkEmergency)
	case "override":
		return queueOperatorOp(store, args, "override", queue.Store.Override)
	case "reject":
		return queueOperatorOp(store, args, "reject", queue.Store.Reject)
	default:
		return Result{}, fmt.Errorf("host.queue: unknown op %q (want one of status, kick, park, resume, emergency, override, reject)", op)
	}
}

// queueStoreArg resolves the queue store from args["project"] (default "."),
// the same project-root convention as the `kitsoki queue --project` CLI.
func queueStoreArg(args map[string]any) (queue.Store, error) {
	project, _ := args["project"].(string)
	if project == "" {
		project = "."
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return queue.Store{}, fmt.Errorf("host.queue: resolve project root %q: %w", project, err)
	}
	return queue.Store{ProjectRoot: abs}, nil
}

// queueStatusOp implements host.queue.status: the full durable state, or a
// single candidate when "id" is provided.
func queueStatusOp(store queue.Store, args map[string]any) (Result, error) {
	if id, _ := args["id"].(string); id != "" {
		c, err := store.Get(id)
		if err != nil {
			return Result{Error: err.Error()}, nil
		}
		data, err := queueJSONMap("candidate", c)
		if err != nil {
			return Result{}, err
		}
		return Result{Data: data}, nil
	}
	state, err := store.List()
	if err != nil {
		return Result{Error: err.Error()}, nil
	}
	data, err := queueJSONMap("state", state)
	if err != nil {
		return Result{}, err
	}
	return Result{Data: data}, nil
}

// queueOperatorOp is the shared shape of the six human-override verbs,
// mirroring cmd/kitsoki's queueOpCmd: every verb is audited (actor + reason
// land in the candidate's durable evidence) and returns the mutated
// candidate. A missing id or a phase violation is a domain error
// (Result.Error), matching the deterministic-verb convention elsewhere in
// this package.
func queueOperatorOp(store queue.Store, args map[string]any, verb string, run func(queue.Store, queue.Op) (queue.Candidate, error)) (Result, error) {
	id, _ := args["id"].(string)
	actor, _ := args["actor"].(string)
	reason, _ := args["reason"].(string)
	c, err := run(store, queue.Op{ID: id, Actor: actor, Reason: reason})
	if err != nil {
		return Result{Error: fmt.Sprintf("host.queue.%s: %v", verb, err)}, nil
	}
	data, err := queueJSONMap("candidate", c)
	if err != nil {
		return Result{}, err
	}
	return Result{Data: data}, nil
}

// queueJSONMap round-trips v through its JSON encoding into a Result.Data
// payload under key, so the host boundary carries only JSON-able primitives
// (the same shape `kitsoki queue` prints).
func queueJSONMap(key string, v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("host.queue: encode %s: %w", key, err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("host.queue: decode %s: %w", key, err)
	}
	return map[string]any{key: decoded}, nil
}
