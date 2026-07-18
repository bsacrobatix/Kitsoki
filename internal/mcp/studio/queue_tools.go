package studio

import (
	"context"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

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
	ID string `json:"id,omitempty"`
}

// QueueOpInput identifies a candidate plus the human context of the operator
// action. Actor and reason are recorded in the candidate's durable evidence.
type QueueOpInput struct {
	ID     string `json:"id"`
	Actor  string `json:"actor,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// QueueStatusResult carries either the full queue state (no id) or a single
// candidate (id given), never both.
type QueueStatusResult struct {
	State     *queue.State     `json:"state,omitempty"`
	Candidate *queue.Candidate `json:"candidate,omitempty"`
}

// QueueOpResult is the candidate as it stands after the operator verb applied.
type QueueOpResult struct {
	Candidate queue.Candidate `json:"candidate"`
}

type queueToolHandlers struct {
	store queue.Store
}

func (h queueToolHandlers) status(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueStatusInput) (*mcpsdk.CallToolResult, any, error) {
	if id := strings.TrimSpace(input.ID); id != "" {
		candidate, err := h.store.Get(id)
		if err != nil {
			return queueToolError(err), nil, nil
		}
		return nil, QueueStatusResult{Candidate: &candidate}, nil
	}
	state, err := h.store.List()
	if err != nil {
		return queueToolError(err), nil, nil
	}
	return nil, QueueStatusResult{State: &state}, nil
}

func (h queueToolHandlers) operate(verb func(queue.Op) (queue.Candidate, error), input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	if strings.TrimSpace(input.ID) == "" {
		return buildToolError(ErrBadRequest, "queue: candidate id is required"), nil, nil
	}
	candidate, err := verb(queue.Op{ID: input.ID, Actor: input.Actor, Reason: input.Reason})
	if err != nil {
		return queueToolError(err), nil, nil
	}
	return nil, QueueOpResult{Candidate: candidate}, nil
}

func (h queueToolHandlers) kick(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.operate(h.store.Kick, input)
}
func (h queueToolHandlers) park(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.operate(h.store.Park, input)
}
func (h queueToolHandlers) resume(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.operate(h.store.Resume, input)
}
func (h queueToolHandlers) emergency(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.operate(h.store.MarkEmergency, input)
}
func (h queueToolHandlers) override(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.operate(h.store.Override, input)
}
func (h queueToolHandlers) reject(_ context.Context, _ *mcpsdk.CallToolRequest, input QueueOpInput) (*mcpsdk.CallToolResult, any, error) {
	return h.operate(h.store.Reject, input)
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
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.status", Description: "Read the merge queue: full ordered state, or one candidate when id is given."}, h.status)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.kick", Description: "Operator verb: clear a retry_wait candidate's backoff so the next worker pass retries immediately; audited in durable evidence."}, h.kick)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.park", Description: "Operator verb: park a non-terminal candidate as needs_input so it stops consuming worker passes; audited in durable evidence."}, h.park)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.resume", Description: "Operator verb: return a parked or retry_wait candidate to the queue with a fresh attempt budget; the prior count stays in evidence."}, h.resume)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.emergency", Description: "Operator verb: move a candidate into the priority emergency lane (FIFO within the lane); audited in durable evidence."}, h.emergency)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.override", Description: "Operator verb: human immediate-merge — emergency-lane priority plus a durable attributed gate waiver; the protected compare-and-swap still applies."}, h.override)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "queue.reject", Description: "Operator verb: terminally reject a candidate; branches, workspaces, and evidence are retained for audit."}, h.reject)
}
