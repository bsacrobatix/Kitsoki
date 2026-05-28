// Package host — oracle dispatcher (proposal §2 B-2).
//
// OracleDispatch is the shared dispatcher that routes oracle handler calls
// through the Oracle plugin interface. It:
//
//  1. Resolves the oracle plugin from the registry injected in context.
//  2. Writes OracleCalled to the EventSink.
//  3. Calls oracle.Ask(ctx, req).
//  4. Appends any SubEvents verbatim between OracleCalled and OracleReturned.
//  5. Validates resp.Submission against req.SchemaJSON (kitsoki is validation authority).
//  6. Writes OracleReturned or OracleError.
//  7. Returns (submission, meta, error).
//
// Backwards compat: when no oracle registry is wired in context (all existing
// call sites before B-2), Dispatch returns errNoRegistry so the caller falls
// through to its existing direct handler logic. This lets B-2 land without
// touching every handler call site — only code paths that opt in to the new
// registry use Dispatch.
//
// The per-verb handlers remain unchanged for B-2; they continue to call their
// direct claude logic.  New call sites (tests, B-3 external transports) go
// through Dispatch.
package host

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"kitsoki/internal/oracle"
	"kitsoki/internal/store"
)

// oracleRegistryKey is the context key for an oracle.Registry injected by
// the orchestrator. The registry is optional — nil means "no registry wired,
// fall through to direct handler logic" (backwards compat).
type oracleRegistryKey struct{}

// WithOracleRegistry returns a child context carrying reg. Oracle handlers
// call OracleRegistryFromCtx to retrieve it.
func WithOracleRegistry(ctx context.Context, reg *oracle.Registry) context.Context {
	if reg == nil {
		return ctx
	}
	return context.WithValue(ctx, oracleRegistryKey{}, reg)
}

// OracleRegistryFromCtx returns the oracle.Registry previously injected with
// WithOracleRegistry, or nil if none was injected.
func OracleRegistryFromCtx(ctx context.Context) *oracle.Registry {
	reg, _ := ctx.Value(oracleRegistryKey{}).(*oracle.Registry)
	return reg
}

// errNoRegistry is returned by Dispatch when no registry is wired in context.
// Handlers use this to fall through to their existing direct logic.
var errNoRegistry = fmt.Errorf("oracle: no registry in context")

// IsNoRegistryError returns true when err is the sentinel returned when no
// registry is wired. Used by handlers to decide whether to fall through.
func IsNoRegistryError(err error) bool {
	return err == errNoRegistry
}

// OracleDispatchRequest carries everything the dispatcher needs to route one
// oracle call through the plugin interface.
type OracleDispatchRequest struct {
	// Req is the fully constructed AskRequest — session, turn, state, verb,
	// prompt, schema, with-args, world, deadline, and call_id are all set.
	Req oracle.AskRequest

	// PluginName is the oracle alias to resolve (e.g. "oracle.claude",
	// "oracle.autofix_fixer"). Empty resolves to the default "oracle.claude".
	PluginName string

	// Verb is the handler verb (ask / decide / extract / task / converse).
	// Copied to the event payload. Should equal Req.Verb.
	Verb string

	// Agent is the agent name resolved from the handler args. Written to
	// the event payload; opaque to the dispatcher.
	Agent string

	// Model is the model name from the resolved agent. Written to the event
	// payload; opaque to the dispatcher.
	Model string

	// PromptText is the rendered prompt (same as Req.PromptText). Split out
	// for event payload clarity.
	PromptText string

	// SystemPrompt is the effective system prompt. Written to OracleCalled.
	SystemPrompt string

	// InputDesc is verb-specific metadata written to the OracleCalled event
	// (e.g. {schema_path: "..."} for decide; {} for ask). Marshalled to JSON.
	InputDesc map[string]any
}

// OracleDispatchResult is returned by Dispatch on success.
type OracleDispatchResult struct {
	// Submission is the validated oracle response. Bound to world by the handler.
	Submission json.RawMessage
	// Meta is opaque oracle metadata (tokens, cost, model).
	Meta map[string]any
	// DurationMS is the round-trip duration in milliseconds.
	DurationMS int64
}

// Dispatch routes an oracle call through the plugin registry. Returns
// errNoRegistry when no registry is wired — callers should fall through to
// their existing direct handler logic in that case.
//
// On oracle error, Dispatch writes an OracleError event and returns a non-nil
// error (an *oracle.AskError or wrapped version).
// On schema validation failure, Dispatch writes OracleError and returns
// *oracle.AskError{Kind: "schema_invalid"}.
func Dispatch(ctx context.Context, dr OracleDispatchRequest) (OracleDispatchResult, error) {
	reg := OracleRegistryFromCtx(ctx)
	if reg == nil {
		return OracleDispatchResult{}, errNoRegistry
	}

	plug, err := reg.Resolve(dr.PluginName)
	if err != nil {
		return OracleDispatchResult{}, fmt.Errorf("oracle dispatch: %w", err)
	}

	callStart := time.Now()
	callID := dr.Req.CallID
	if callID == "" {
		callID = newUUID()
		dr.Req.CallID = callID
	}

	// Write OracleCalled to the JSONL sink.
	appendOracleCalledEvent(ctx, callStart, callID, OracleCalledPayload{
		Verb:         dr.Verb,
		Agent:        dr.Agent,
		Model:        dr.Model,
		Prompt:       dr.PromptText,
		SystemPrompt: dr.SystemPrompt,
		Input:        marshalInput(dr.InputDesc),
	})

	resp, askErr := plug.Ask(ctx, dr.Req)
	durationMS := time.Since(callStart).Milliseconds()

	if askErr != nil {
		callEnd := time.Now()
		appendOracleErrorEvent(ctx, callEnd, callID, OracleErrorPayload{
			Verb:       dr.Verb,
			Agent:      dr.Agent,
			DurationMS: durationMS,
			Error:      askErr.Error(),
		})
		// Also write to journal (belt-and-braces until B-5 deletes oracle_journal).
		appendOracleCallJournal(ctx, callStart, 0, OracleCallBody{
			CallID:       callID,
			Verb:         dr.Verb,
			Agent:        dr.Agent,
			Model:        dr.Model,
			DurationMS:   durationMS,
			SystemPrompt: dr.SystemPrompt,
			Prompt:       dr.PromptText,
			Input:        marshalInput(dr.InputDesc),
			Error:        askErr.Error(),
		})
		return OracleDispatchResult{}, askErr
	}

	// Append SubEvents verbatim between OracleCalled and OracleReturned.
	// B-4 will add namespace + size validation; B-2 appends in order.
	if len(resp.SubEvents) > 0 {
		sink := EventSinkFromOracleCtx(ctx)
		if sink != nil {
			for _, se := range resp.SubEvents {
				_ = sink.Append(se)
			}
		}
	}

	// Validate submission against schema (kitsoki is validation authority).
	if validErr := oracle.ValidateSubmission(dr.Req.SchemaJSON, resp.Submission); validErr != nil {
		callEnd := time.Now()
		appendOracleErrorEvent(ctx, callEnd, callID, OracleErrorPayload{
			Verb:       dr.Verb,
			Agent:      dr.Agent,
			DurationMS: durationMS,
			Error:      validErr.Error(),
		})
		appendOracleCallJournal(ctx, callStart, 0, OracleCallBody{
			CallID:       callID,
			Verb:         dr.Verb,
			Agent:        dr.Agent,
			Model:        dr.Model,
			DurationMS:   durationMS,
			SystemPrompt: dr.SystemPrompt,
			Prompt:       dr.PromptText,
			Input:        marshalInput(dr.InputDesc),
			Error:        validErr.Error(),
		})
		return OracleDispatchResult{}, validErr
	}

	callEnd := time.Now()
	responseDesc := map[string]any{}
	if resp.Submission != nil {
		var parsed any
		if json.Unmarshal(resp.Submission, &parsed) == nil {
			responseDesc["submission"] = parsed
		}
	}
	if resp.Meta != nil {
		responseDesc["meta"] = resp.Meta
	}

	appendOracleReturnedEvent(ctx, callEnd, callID, OracleReturnedPayload{
		Verb:       dr.Verb,
		Agent:      dr.Agent,
		Model:      dr.Model,
		DurationMS: durationMS,
		Response:   marshalResponse(responseDesc),
		Meta:       resp.Meta,
	})

	appendOracleCallJournal(ctx, callStart, 0, OracleCallBody{
		CallID:       callID,
		Verb:         dr.Verb,
		Agent:        dr.Agent,
		Model:        dr.Model,
		DurationMS:   durationMS,
		SystemPrompt: dr.SystemPrompt,
		Prompt:       dr.PromptText,
		Input:        marshalInput(dr.InputDesc),
		Response:     marshalResponse(responseDesc),
	})

	return OracleDispatchResult{
		Submission: resp.Submission,
		Meta:       resp.Meta,
		DurationMS: durationMS,
	}, nil
}

// appendSubEventsToSink writes a slice of store.Events to the EventSink in ctx.
// This is the B-2 implementation: no validation, just sequential append.
// B-4 will add namespace + size checks.
func appendSubEventsToSink(ctx context.Context, events []store.Event) {
	if len(events) == 0 {
		return
	}
	sink := EventSinkFromOracleCtx(ctx)
	if sink == nil {
		return
	}
	for _, e := range events {
		_ = sink.Append(e)
	}
}
