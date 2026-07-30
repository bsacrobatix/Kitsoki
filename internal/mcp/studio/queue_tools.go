package studio

import (
	"context"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/capsule/delivery"
	"kitsoki/internal/capsule/queue"
)

// Queue tools expose the capsule merge queue's durable operator surface over
// MCP. These are human/operator override verbs: every mutation runs under the
// queue state lock, validates the candidate's phase, and appends audited
// evidence (actor + reason + timestamp) that survives in the state file. The
// tools add no authority the CLI does not already have — they delegate to
// queue.Store and never bypass the protected compare-and-swap on landing.

// QueueStatusInput selects the whole queue (empty id) or one candidate.
type QueueStatusInput struct {
	ID        string `json:"id,omitempty"`
	QueueRoot string `json:"queue_root,omitempty"`
}

// QueueOpInput identifies a candidate plus the human context of the operator
// action. Actor and reason are recorded in the candidate's durable evidence.
type QueueOpInput struct {
	ID        string `json:"id"`
	Actor     string `json:"actor,omitempty"`
	Reason    string `json:"reason,omitempty"`
	QueueRoot string `json:"queue_root,omitempty"`
}

type QueueSubmitInput struct {
	delivery.SubmitRequest
	QueueRoot string `json:"queue_root,omitempty"`
}

// QueueOpResult is the candidate as it stands after the operator verb applied.
type QueueOpResult struct {
	Candidate queue.Candidate `json:"candidate"`
}

type queueToolHandlers struct {
	store queue.Store
}

func (h queueToolHandlers) storeFor(queueRoot string) queue.Store {
	store := h.store
	if strings.TrimSpace(queueRoot) != "" {
		store.QueueRoot = strings.TrimSpace(queueRoot)
	}
	return store
}

func (h queueToolHandlers) status(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueStatusInput) (*mcpsdk.CallToolResult, any, error) {
	service := delivery.New(h.storeFor(input.QueueRoot))
	if id := strings.TrimSpace(input.ID); id != "" {
		result, err := service.Get(id)
		if err != nil {
			return queueToolError(err), nil, nil
		}
		return nil, result, nil
	}
	result, err := service.Status()
	if err != nil {
		return queueToolError(err), nil, nil
	}
	return nil, result, nil
}

func (h queueToolHandlers) submit(ctx context.Context, _ *mcpsdk.CallToolRequest, input QueueSubmitInput) (*mcpsdk.CallToolResult, any, error) {
	result, err := delivery.New(h.storeFor(input.QueueRoot)).Submit(ctx, input.SubmitRequest)
	if err != nil {
		return queueToolError(err), nil, nil
	}
	return nil, result, nil
}

func (h queueToolHandlers) get(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueStatusInput) (*mcpsdk.CallToolResult, any, error) {
	if strings.TrimSpace(input.ID) == "" {
		return buildToolError(ErrBadRequest, "queue: candidate id is required"), nil, nil
	}
	result, err := delivery.New(h.storeFor(input.QueueRoot)).Get(input.ID)
	if err != nil {
		return queueToolError(err), nil, nil
	}
	return nil, result, nil
}

func (h queueToolHandlers) deliveryOperate(
	verb func(delivery.Service, queue.Op) (delivery.Result, error),
	input QueueOpInput,
) (*mcpsdk.CallToolResult, any, error) {
	if strings.TrimSpace(input.ID) == "" {
		return buildToolError(ErrBadRequest, "queue: candidate id is required"), nil, nil
	}
	result, err := verb(delivery.New(h.storeFor(input.QueueRoot)), queue.Op{ID: input.ID, Actor: input.Actor, Reason: input.Reason})
	if err != nil {
		return queueToolError(err), nil, nil
	}
	return nil, result, nil
}

func (h queueToolHandlers) retry(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.deliveryOperate(delivery.Service.Retry, input)
}

func (h queueToolHandlers) cancel(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.deliveryOperate(delivery.Service.Cancel, input)
}

func (h queueToolHandlers) operate(verb func(queue.Store, queue.Op) (queue.Candidate, error), input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	if strings.TrimSpace(input.ID) == "" {
		return buildToolError(ErrBadRequest, "queue: candidate id is required"), nil, nil
	}
	candidate, err := verb(h.storeFor(input.QueueRoot), queue.Op{ID: input.ID, Actor: input.Actor, Reason: input.Reason})
	if err != nil {
		return queueToolError(err), nil, nil
	}
	return nil, QueueOpResult{Candidate: candidate}, nil
}

func (h queueToolHandlers) kick(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.operate(queue.Store.Kick, input)
}
func (h queueToolHandlers) park(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.operate(queue.Store.Park, input)
}
func (h queueToolHandlers) resume(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.operate(queue.Store.Resume, input)
}
func (h queueToolHandlers) emergency(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.operate(queue.Store.MarkEmergency, input)
}
func (h queueToolHandlers) override(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.operate(queue.Store.Override, input)
}
func (h queueToolHandlers) reject(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.deliveryOperate(delivery.Service.Reject, input)
}

func queueToolError(err error) *mcpsdk.CallToolResult {
	return buildToolError(ErrBadRequest, err.Error())
}

// RegisterQueueTools registers the merge-queue operator verbs against the
// project's durable queue state. The store's ProjectRoot anchors the state
// file under <root>/.capsules/queue; the tools hold no in-memory state of
// their own, so a CLI operator and a studio agent observe the same queue.
func RegisterQueueTools(server *mcpsdk.Server, store queue.Store) {
	if server == nil || strings.TrimSpace(store.ProjectRoot) == "" {
		panic("queue tools require a server and a project root")
	}
	h := queueToolHandlers{store: store}
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.submit", Description: "Admit one retained, verified durable_bundle request into the merge queue."}, h.submit)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.get", Description: "Read one durable delivery candidate and its projected next action."}, h.get)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.status", Description: "Read the merge queue: full ordered state, or one candidate when id is given."}, h.status)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.retry", Description: "Retry recoverable delivery work by kicking retry_wait or resuming parked work."}, h.retry)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.cancel", Description: "Park delivery work without losing its retained branch, bundle, receipts, or evidence."}, h.cancel)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.kick", Description: "Operator verb: clear a retry_wait candidate's backoff so the next worker pass retries immediately; audited in durable evidence."}, h.kick)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.park", Description: "Operator verb: park a non-terminal candidate as needs_input so it stops consuming worker passes; audited in durable evidence."}, h.park)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.resume", Description: "Operator verb: return a parked or retry_wait candidate to the queue with a fresh attempt budget; the prior count stays in evidence."}, h.resume)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.emergency", Description: "Operator verb: move a candidate into the priority emergency lane (FIFO within the lane); audited in durable evidence."}, h.emergency)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.override", Description: "Operator verb: human immediate-merge — emergency-lane priority plus a durable attributed gate waiver; the protected compare-and-swap still applies."}, h.override)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.reject", Description: "Operator verb: terminally reject a candidate; branches, workspaces, and evidence are retained for audit."}, h.reject)
}
