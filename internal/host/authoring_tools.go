// Package host — authoring tool handlers for meta-mode story-author agent.
//
// Three handlers wrap internal/authoring for use inside a meta-mode
// conversation:
//
//   - host.authoring.propose   — run claude inside a shadow copy and
//     produce a draft diff.
//   - host.authoring.apply     — apply a previously-drafted proposal
//     from the session ledger.
//   - host.authoring.discard   — drop a draft without applying.
//
// # Path-Y wiring (chosen for WS-A4)
//
// The meta-mode brief described two paths for surfacing these handlers
// to claude: (Path X) register them as MCP tools claude can call
// natively, or (Path Y) have claude emit structured tokens in its
// reply and the metamode controller parses them and invokes the
// handlers in-process. Path X was rejected during scoping because the
// per-session ledger lookup (chat_id → *metamode.ProposalLedger) would
// require cross-process IPC against the claude-spawned MCP subprocess,
// which is >300 LOC of plumbing for a workstream the brief budgets at
// ~150 LOC. Path Y keeps the handlers as plain Go functions, with a
// package-level chat-id-keyed AuthoringRegistrar holding the per-
// session ledger pointers the controller registers before each
// Oracle.Ask and de-registers afterwards.
//
// # Argument shapes
//
// chat_id is the FIRST required field on all three argument structs:
// the handlers cannot look up the right ProposalLedger without it.
// The metamode controller is the only legitimate caller and always
// supplies it.
//
// # Result shapes
//
// All three handlers return their typed Result structs marshalled into
// the host.Result.Data map under the documented field names so the
// metamode controller / future MCP wrappers can decode without
// importing this package's types.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"kitsoki/internal/authoring"
)

// ─── argument & result shapes ────────────────────────────────────────────────

// AuthoringProposeArgs is the claude-facing argument shape for
// host.authoring.propose. JSON tags are load-bearing: they're the
// schema claude sees when called as an MCP tool, and the token-parser
// uses the same names.
type AuthoringProposeArgs struct {
	ChatID       string `json:"chat_id"`
	Text         string `json:"text"`
	AppFile      string `json:"app_file"`
	CurrentState string `json:"current_state,omitempty"`
	CurrentView  string `json:"current_view,omitempty"`
}

// AuthoringProposeResult is the typed return from a successful propose.
type AuthoringProposeResult struct {
	ProposalID string `json:"proposal_id"`
	Summary    string `json:"summary"`
	Diff       string `json:"diff"`
}

// AuthoringApplyArgs is the claude-facing argument shape for
// host.authoring.apply.
type AuthoringApplyArgs struct {
	ChatID     string `json:"chat_id"`
	ProposalID string `json:"proposal_id"`
}

// AuthoringApplyResult reports whether the apply landed and (when so)
// echoes the proposal's summary line.
type AuthoringApplyResult struct {
	Applied bool   `json:"applied"`
	Summary string `json:"summary,omitempty"`
}

// AuthoringDiscardArgs is the claude-facing argument shape for
// host.authoring.discard.
type AuthoringDiscardArgs struct {
	ChatID     string `json:"chat_id"`
	ProposalID string `json:"proposal_id"`
}

// AuthoringDiscardResult reports whether the discard removed a draft.
type AuthoringDiscardResult struct {
	Discarded bool `json:"discarded"`
}

// ─── ledger seam (avoids an import cycle metamode→host→metamode) ────────────

// AuthoringLedger is the tiny subset of *metamode.ProposalLedger the
// authoring handlers mutate. Declared here as an interface so the
// host package doesn't have to import internal/metamode (which would
// be a cycle: metamode already imports host via the OracleCaller
// adapter).
//
// *metamode.ProposalLedger satisfies this interface via structural
// typing — no explicit "implements" declaration on the metamode side.
type AuthoringLedger interface {
	// Add registers p as a new draft and returns its short id.
	Add(p *authoring.Proposal) string
	// Get returns the entry with the given id; ok=false if unknown.
	Get(id string) (LedgerEntry, bool)
	// Discard cleans up the shadow dir and marks the entry as
	// discarded. Unknown id → non-nil error.
	Discard(id string) error
	// RecordApplied marks the entry as applied AND flips the
	// reload-pending flag on the ledger.
	RecordApplied(proposalID string)
}

// LedgerEntry is the subset of *metamode.PendingProposal the host
// handlers read. Defined here for the same import-cycle reason as
// AuthoringLedger.
type LedgerEntry interface {
	ProposalID() string
	Underlying() *authoring.Proposal
}

// ─── chat-id-keyed registrar ─────────────────────────────────────────────────

// authoringRegistrar is a package-level chat-id → AuthoringLedger map.
// The metamode controller registers a session's ledger keyed by
// session.Chat.ID() before calling Oracle.Ask and de-registers in a
// defer after Ask returns; the handlers look up the ledger by the
// chat_id arg.
//
// Concurrency: the handlers may be invoked from claude's MCP
// subprocess on different goroutines than the controller; the
// underlying map is protected by a RWMutex. The ledger itself has its
// own mutex so the registrar only needs to gate map mutation.
type authoringRegistrar struct {
	mu      sync.RWMutex
	ledgers map[string]AuthoringLedger
}

// Global registrar instance. Package-level singleton because the
// handler functions are top-level (no per-Controller binding) and
// would otherwise need to be wrapped at registration time. Reset is
// exposed for tests.
var registrar = &authoringRegistrar{ledgers: make(map[string]AuthoringLedger)}

// RegisterAuthoringLedger associates a chat id with a ledger so the
// three authoring.* handlers can find it. Overwrites on collision —
// the metamode controller is the only registrant and re-registers on
// every Send for safety.
func RegisterAuthoringLedger(chatID string, ledger AuthoringLedger) {
	if chatID == "" || ledger == nil {
		return
	}
	registrar.mu.Lock()
	defer registrar.mu.Unlock()
	registrar.ledgers[chatID] = ledger
}

// UnregisterAuthoringLedger removes the ledger for chatID. No-op if
// the chat id is unknown. The metamode controller defers this after
// Oracle.Ask returns.
func UnregisterAuthoringLedger(chatID string) {
	if chatID == "" {
		return
	}
	registrar.mu.Lock()
	defer registrar.mu.Unlock()
	delete(registrar.ledgers, chatID)
}

// lookupAuthoringLedger fetches the registered ledger for chatID.
func lookupAuthoringLedger(chatID string) (AuthoringLedger, bool) {
	registrar.mu.RLock()
	defer registrar.mu.RUnlock()
	l, ok := registrar.ledgers[chatID]
	return l, ok
}

// ─── propose runner seam (for tests) ─────────────────────────────────────────

// proposeRunner is the function called by AuthoringPropose to actually
// run authoring.Propose. Tests swap this for a fake so they don't have
// to shell out to claude. The default points at the real function.
var proposeRunner = func(ctx context.Context, appPath, text string, runCtx *authoring.Context) (*authoring.Proposal, error) {
	return authoring.Propose(ctx, appPath, text, runCtx)
}

// applyRunner is the seam tests use to substitute the destructive
// authoring.Apply call. Defaults to the real one.
var applyRunner = func(p *authoring.Proposal) error {
	return authoring.Apply(p)
}

// ─── handlers ────────────────────────────────────────────────────────────────

// AuthoringPropose runs `authoring.Propose` inside the per-session
// ledger context: it materialises a shadow diff and parks the
// resulting *authoring.Proposal on the ledger as a draft so a later
// AuthoringApply can find it by id.
func AuthoringPropose(ctx context.Context, in AuthoringProposeArgs) (AuthoringProposeResult, error) {
	if strings.TrimSpace(in.ChatID) == "" {
		return AuthoringProposeResult{}, fmt.Errorf("host.authoring.propose: chat_id is required")
	}
	if strings.TrimSpace(in.AppFile) == "" {
		return AuthoringProposeResult{}, fmt.Errorf("host.authoring.propose: app_file is required")
	}
	if strings.TrimSpace(in.Text) == "" {
		return AuthoringProposeResult{}, fmt.Errorf("host.authoring.propose: text is required")
	}

	ledger, ok := lookupAuthoringLedger(in.ChatID)
	if !ok {
		return AuthoringProposeResult{}, fmt.Errorf("host.authoring.propose: no ledger registered for chat_id %q (controller did not register session)", in.ChatID)
	}

	var runCtx *authoring.Context
	if in.CurrentState != "" || in.CurrentView != "" {
		runCtx = &authoring.Context{State: in.CurrentState, View: in.CurrentView}
	}

	p, err := proposeRunner(ctx, in.AppFile, in.Text, runCtx)
	if err != nil {
		return AuthoringProposeResult{}, fmt.Errorf("host.authoring.propose: %w", err)
	}
	if p == nil {
		return AuthoringProposeResult{}, errors.New("host.authoring.propose: authoring returned nil proposal")
	}

	id := ledger.Add(p)
	return AuthoringProposeResult{
		ProposalID: id,
		Summary:    p.Summary,
		Diff:       p.UnifiedDiff,
	}, nil
}

// AuthoringApply commits the proposal identified by ProposalID from
// the per-session ledger, then records the apply on the ledger so the
// metamode controller's post-Ask drain (ConsumeReload) returns true.
//
// Unknown proposal IDs return AuthoringApplyResult{Applied:false} with
// a NON-NIL Go error — this is the signal the brief asked for: a
// structured negative result rather than a panic.
func AuthoringApply(ctx context.Context, in AuthoringApplyArgs) (AuthoringApplyResult, error) {
	_ = ctx
	if strings.TrimSpace(in.ChatID) == "" {
		return AuthoringApplyResult{}, fmt.Errorf("host.authoring.apply: chat_id is required")
	}
	if strings.TrimSpace(in.ProposalID) == "" {
		return AuthoringApplyResult{}, fmt.Errorf("host.authoring.apply: proposal_id is required")
	}
	ledger, ok := lookupAuthoringLedger(in.ChatID)
	if !ok {
		return AuthoringApplyResult{}, fmt.Errorf("host.authoring.apply: no ledger registered for chat_id %q", in.ChatID)
	}
	entry, found := ledger.Get(in.ProposalID)
	if !found {
		return AuthoringApplyResult{Applied: false}, fmt.Errorf("host.authoring.apply: unknown proposal_id %q", in.ProposalID)
	}
	p := entry.Underlying()
	if p == nil {
		return AuthoringApplyResult{Applied: false}, fmt.Errorf("host.authoring.apply: proposal_id %q has no underlying authoring proposal", in.ProposalID)
	}
	if err := applyRunner(p); err != nil {
		return AuthoringApplyResult{Applied: false}, fmt.Errorf("host.authoring.apply: %w", err)
	}
	ledger.RecordApplied(in.ProposalID)
	return AuthoringApplyResult{Applied: true, Summary: p.Summary}, nil
}

// AuthoringDiscard drops the draft identified by ProposalID, cleaning
// up the shadow directory via authoring.Discard (called inside
// ledger.Discard).
func AuthoringDiscard(ctx context.Context, in AuthoringDiscardArgs) (AuthoringDiscardResult, error) {
	_ = ctx
	if strings.TrimSpace(in.ChatID) == "" {
		return AuthoringDiscardResult{}, fmt.Errorf("host.authoring.discard: chat_id is required")
	}
	if strings.TrimSpace(in.ProposalID) == "" {
		return AuthoringDiscardResult{}, fmt.Errorf("host.authoring.discard: proposal_id is required")
	}
	ledger, ok := lookupAuthoringLedger(in.ChatID)
	if !ok {
		return AuthoringDiscardResult{}, fmt.Errorf("host.authoring.discard: no ledger registered for chat_id %q", in.ChatID)
	}
	if _, found := ledger.Get(in.ProposalID); !found {
		return AuthoringDiscardResult{Discarded: false}, fmt.Errorf("host.authoring.discard: unknown proposal_id %q", in.ProposalID)
	}
	if err := ledger.Discard(in.ProposalID); err != nil {
		return AuthoringDiscardResult{Discarded: false}, fmt.Errorf("host.authoring.discard: %w", err)
	}
	return AuthoringDiscardResult{Discarded: true}, nil
}

// ─── host.Handler adapters (for RegisterBuiltins) ────────────────────────────

// authoringProposeHandler is the host.Handler shape registered in the
// builtin host registry under "host.authoring.propose". Translates the
// untyped args map into AuthoringProposeArgs by JSON round-trip so the
// chat_id / text / app_file fields are populated regardless of how
// the call site phrased them.
func authoringProposeHandler(ctx context.Context, args map[string]any) (Result, error) {
	var in AuthoringProposeArgs
	if err := decodeArgs(args, &in); err != nil {
		return Result{Error: fmt.Sprintf("host.authoring.propose: %v", err)}, nil
	}
	out, err := AuthoringPropose(ctx, in)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}
	return Result{Data: map[string]any{
		"proposal_id": out.ProposalID,
		"summary":     out.Summary,
		"diff":        out.Diff,
	}}, nil
}

func authoringApplyHandler(ctx context.Context, args map[string]any) (Result, error) {
	var in AuthoringApplyArgs
	if err := decodeArgs(args, &in); err != nil {
		return Result{Error: fmt.Sprintf("host.authoring.apply: %v", err)}, nil
	}
	out, err := AuthoringApply(ctx, in)
	if err != nil {
		return Result{Error: err.Error(), Data: map[string]any{
			"applied": out.Applied,
		}}, nil
	}
	return Result{Data: map[string]any{
		"applied": out.Applied,
		"summary": out.Summary,
	}}, nil
}

func authoringDiscardHandler(ctx context.Context, args map[string]any) (Result, error) {
	var in AuthoringDiscardArgs
	if err := decodeArgs(args, &in); err != nil {
		return Result{Error: fmt.Sprintf("host.authoring.discard: %v", err)}, nil
	}
	out, err := AuthoringDiscard(ctx, in)
	if err != nil {
		return Result{Error: err.Error(), Data: map[string]any{
			"discarded": out.Discarded,
		}}, nil
	}
	return Result{Data: map[string]any{
		"discarded": out.Discarded,
	}}, nil
}

// decodeArgs round-trips an untyped map through JSON into a typed
// struct. Fine for the handful of small args structs the authoring
// handlers take; we keep it inline rather than dragging in mapstructure.
func decodeArgs(args map[string]any, out any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal args: %w", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode args: %w", err)
	}
	return nil
}

// RegisterAuthoringBuiltins registers the three authoring handlers in
// the given host Registry under their host.authoring.* names. Called
// from host.RegisterBuiltins.
func RegisterAuthoringBuiltins(r *Registry) {
	r.Register("host.authoring.propose", authoringProposeHandler)
	r.Register("host.authoring.apply", authoringApplyHandler)
	r.Register("host.authoring.discard", authoringDiscardHandler)
}

// ─── structured-token parser (Path Y surface for claude replies) ─────────────
//
// Claude emits tool intents as fenced tokens inside its prose reply.
// The metamode controller scans the reply with ParseAuthoringCalls
// and invokes the matching handlers in order. Tokens that appear:
//
//   <<<propose>>>{"app_file": "/abs/path", "text": "…", ...}<<<endpropose>>>
//   <<<apply abcd1234>>>
//   <<<discard abcd1234>>>
//
// The propose form carries a JSON object body whose fields match the
// AuthoringProposeArgs JSON tags except chat_id (the controller
// injects that before dispatching). The apply/discard forms carry the
// proposal id inline.
//
// Tokens are intentionally distinctive (triple-angle braces) to keep
// the parser robust against accidental matches inside markdown code
// fences. The story-author.md prompt teaches claude the grammar.

// AuthoringCallKind enumerates the three token types.
type AuthoringCallKind string

const (
	AuthoringCallPropose AuthoringCallKind = "propose"
	AuthoringCallApply   AuthoringCallKind = "apply"
	AuthoringCallDiscard AuthoringCallKind = "discard"
)

// AuthoringCall is one parsed structured-token call from claude's
// reply. For propose, Payload carries the JSON object; for apply and
// discard, ProposalID is set and Payload is empty.
type AuthoringCall struct {
	Kind       AuthoringCallKind
	Payload    string // raw JSON body, propose only
	ProposalID string // apply / discard only
}

// proposeBlockRE matches <<<propose>>>...<<<endpropose>>>. The lazy
// .*? inside (?s)-multiline keeps two adjacent propose blocks from
// glomming together.
var proposeBlockRE = regexp.MustCompile(`(?s)<<<propose>>>(.*?)<<<endpropose>>>`)

// applyRE matches <<<apply <id>>>>. The id is a single token of
// non-whitespace, non-">" characters so the closing brace can't be
// part of it.
var applyRE = regexp.MustCompile(`<<<apply\s+([^>\s]+)>>>`)

// discardRE mirrors applyRE for discard.
var discardRE = regexp.MustCompile(`<<<discard\s+([^>\s]+)>>>`)

// ParseAuthoringCalls scans reply for the three structured tokens and
// returns them in document order. Unmatched / malformed tokens are
// silently skipped; the parser is permissive on purpose so a stray
// "<<<propose>>>" inside a code fence claude wrote for explanation
// doesn't crash the controller.
func ParseAuthoringCalls(reply string) []AuthoringCall {
	if reply == "" {
		return nil
	}

	type indexed struct {
		offset int
		call   AuthoringCall
	}
	var found []indexed

	for _, m := range proposeBlockRE.FindAllStringSubmatchIndex(reply, -1) {
		// m[0]=start of full match, m[2]/m[3]=group 1 (payload)
		payload := strings.TrimSpace(reply[m[2]:m[3]])
		if payload == "" {
			continue
		}
		found = append(found, indexed{
			offset: m[0],
			call: AuthoringCall{
				Kind:    AuthoringCallPropose,
				Payload: payload,
			},
		})
	}
	for _, m := range applyRE.FindAllStringSubmatchIndex(reply, -1) {
		id := strings.TrimSpace(reply[m[2]:m[3]])
		if id == "" {
			continue
		}
		found = append(found, indexed{
			offset: m[0],
			call: AuthoringCall{
				Kind:       AuthoringCallApply,
				ProposalID: id,
			},
		})
	}
	for _, m := range discardRE.FindAllStringSubmatchIndex(reply, -1) {
		id := strings.TrimSpace(reply[m[2]:m[3]])
		if id == "" {
			continue
		}
		found = append(found, indexed{
			offset: m[0],
			call: AuthoringCall{
				Kind:       AuthoringCallDiscard,
				ProposalID: id,
			},
		})
	}

	// Sort by document order so the controller dispatches calls in
	// the sequence claude emitted them.
	for i := 1; i < len(found); i++ {
		for j := i; j > 0 && found[j-1].offset > found[j].offset; j-- {
			found[j-1], found[j] = found[j], found[j-1]
		}
	}
	out := make([]AuthoringCall, len(found))
	for i, f := range found {
		out[i] = f.call
	}
	return out
}
