// Package host — host.queue.* : the capsule merge queue's operator surface
// exposed as a host verb family so starlark glue (ctx.host.call) can inspect
// and steer the queue without shelling out to `kitsoki queue`.
//
// Registered bare at "host.queue" (longest-prefix convention, see
// host.graph / host.demo — the registry injects the dropped suffix as
// args["op"]):
//
//	submit    {request}              -> durable_bundle admission
//	get       {id}                   -> one candidate plus projected next action
//	status    {[id]}                 -> full queue state, or one candidate when "id" is set
//	retry     {id[, actor, reason]}  -> normalize kick/resume for recoverable work
//	cancel    {id[, actor, reason]}  -> park without discarding retained evidence
//	kick      {id[, actor, reason]}  -> clear a retry_wait candidate's backoff timer
//	park      {id[, actor, reason]}  -> move a candidate to needs_input
//	resume    {id[, actor, reason]}  -> re-queue a parked candidate with a fresh attempt budget
//	emergency {id[, actor, reason]}  -> move a candidate into the priority emergency lane
//	override  {id[, actor, reason]}  -> human immediate-merge: emergency lane + durable attributed gate waiver
//	approve   {id, manifest[, actor, reason, tree, receipt_digest]} -> steward approval of current prepared tuple
//	unapprove {id[, actor, reason]}  -> withdraw a steward approval
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

	"kitsoki/internal/capsule/delivery"
	"kitsoki/internal/capsule/queue"
)

// QueueHandler implements the host.queue.* multi-op verb.
func QueueHandler(ctx context.Context, args map[string]any) (Result, error) {
	op, _ := args["op"].(string)
	store, err := queueStoreArg(args)
	if err != nil {
		return Result{}, err
	}
	switch op {
	case "submit":
		return queueDeliverySubmit(ctx, store, args)
	case "get":
		return queueDeliveryGet(store, args)
	case "status":
		return queueStatusOp(store, args)
	case "retry":
		return queueDeliveryOp(store, args, "retry", delivery.Service.Retry)
	case "cancel":
		return queueDeliveryOp(store, args, "cancel", delivery.Service.Cancel)
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
	case "approve":
		return queueApprovalOp(store, args)
	case "unapprove":
		return queueOperatorOp(store, args, "unapprove", queue.Store.Unapprove)
	case "reject":
		return queueDeliveryOp(store, args, "reject", delivery.Service.Reject)
	default:
		return Result{}, fmt.Errorf("host.queue: unknown op %q (want one of submit, get, status, retry, cancel, kick, park, resume, emergency, override, approve, unapprove, reject)", op)
	}
}

func queueDeliverySubmit(ctx context.Context, store queue.Store, args map[string]any) (Result, error) {
	raw, err := json.Marshal(args["request"])
	if err != nil {
		return Result{}, fmt.Errorf("host.queue.submit: request must be JSON serializable: %w", err)
	}
	var request delivery.SubmitRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return Result{}, fmt.Errorf("host.queue.submit: decode request: %w", err)
	}
	result, err := delivery.New(store).Submit(ctx, request)
	if err != nil {
		return Result{Error: "host.queue.submit: " + err.Error()}, nil
	}
	return deliveryHostResult(result)
}

func queueDeliveryGet(store queue.Store, args map[string]any) (Result, error) {
	id, _ := args["id"].(string)
	result, err := delivery.New(store).Get(id)
	if err != nil {
		return Result{Error: "host.queue.get: " + err.Error()}, nil
	}
	return deliveryHostResult(result)
}

func queueDeliveryOp(
	store queue.Store,
	args map[string]any,
	verb string,
	run func(delivery.Service, queue.Op) (delivery.Result, error),
) (Result, error) {
	id, _ := args["id"].(string)
	actor, _ := args["actor"].(string)
	reason, _ := args["reason"].(string)
	result, err := run(delivery.New(store), queue.Op{ID: id, Actor: actor, Reason: reason})
	if err != nil {
		return Result{Error: fmt.Sprintf("host.queue.%s: %v", verb, err)}, nil
	}
	return deliveryHostResult(result)
}

func deliveryHostResult(result delivery.Result) (Result, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return Result{}, fmt.Errorf("host.queue: encode delivery result: %w", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return Result{}, fmt.Errorf("host.queue: decode delivery result: %w", err)
	}
	return Result{Data: data}, nil
}

func queueApprovalOp(store queue.Store, args map[string]any) (Result, error) {
	id, _ := args["id"].(string)
	actor, _ := args["actor"].(string)
	reason, _ := args["reason"].(string)
	manifest, _ := args["manifest"].(string)
	tree, _ := args["tree"].(string)
	receiptDigest, _ := args["receipt_digest"].(string)
	c, err := store.Approve(queue.ApprovalOp{ID: id, Actor: actor, Reason: reason, ManifestDigest: manifest, TreeSHA: tree, ReceiptDigest: receiptDigest})
	if err != nil {
		return Result{Error: "host.queue.approve: " + err.Error()}, nil
	}
	data, err := queueJSONMap("candidate", c)
	if err != nil {
		return Result{}, err
	}
	return Result{Data: data}, nil
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
		result, err := delivery.New(store).Get(id)
		if err != nil {
			return Result{Error: err.Error()}, nil
		}
		return deliveryHostResult(result)
	}
	result, err := delivery.New(store).Status()
	if err != nil {
		return Result{Error: err.Error()}, nil
	}
	return deliveryHostResult(result)
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
