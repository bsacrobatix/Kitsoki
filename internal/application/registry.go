package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

var (
	ErrHandlerNotFound = errors.New("application: handler not found")
	ErrEventNotFound   = errors.New("application: event not found")
	ErrNotExposed      = errors.New("application: handler is not exposed on transport")
	ErrUnauthorized    = errors.New("application: invocation is not authorized")
	ErrBudgetDenied    = errors.New("application: invocation denied by budget policy")
)

type HandlerDefinition struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	Description           string            `json:"description"`
	SemanticRef           string            `json:"semantic_ref"`
	InputSchema           json.RawMessage   `json:"input_schema,omitempty"`
	OutputSchema          json.RawMessage   `json:"output_schema,omitempty"`
	InputSchemaReference  SchemaReference   `json:"-"`
	OutputSchemaReference SchemaReference   `json:"-"`
	Session               SessionPolicy     `json:"session"`
	Effect                EffectClass       `json:"effect"`
	RoutingMode           RoutingMode       `json:"routing_mode"`
	Outcomes              []string          `json:"outcomes"`
	Expose                []Transport       `json:"expose,omitempty"`
	Idempotency           IdempotencyPolicy `json:"idempotency,omitempty"`
	IdempotencyKeyField   string            `json:"idempotency_key_field,omitempty"`
	IdempotencyScope      string            `json:"idempotency_scope,omitempty"`
	Retryable             bool              `json:"retryable,omitempty"`
	CompensationHandler   string            `json:"compensation_handler,omitempty"`
	NoCompensationReason  string            `json:"no_compensation_reason,omitempty"`
}

type EventDefinition struct {
	ID                   string          `json:"id"`
	Source               string          `json:"source"`
	InputSchema          json.RawMessage `json:"input_schema,omitempty"`
	InputSchemaReference SchemaReference `json:"-"`
	Session              SessionPolicy   `json:"session"`
	Mode                 EventMode       `json:"mode"`
	RoutingMode          RoutingMode     `json:"routing_mode,omitempty"`
	Handler              string          `json:"handler"`
}

type Invocation struct {
	HandlerID       string
	Input           json.RawMessage
	SessionID       string
	Actor           string
	Transport       Transport
	RoutingMode     RoutingMode
	IdempotencyKey  string
	EventID         string
	EventMode       EventMode
	EventSessionID  string
	SessionPrepared bool
	FrameRevision   uint64
}

type HandlerResult struct {
	Outcome             string
	Output              json.RawMessage
	Frame               *Frame
	Children            []ChildRun
	Join                *JoinState
	RoutingResolved     RoutingMode
	SelectedImplementor string
}

type Handler interface {
	Invoke(context.Context, Invocation) (HandlerResult, error)
}

type HandlerFunc func(context.Context, Invocation) (HandlerResult, error)

func (f HandlerFunc) Invoke(ctx context.Context, invocation Invocation) (HandlerResult, error) {
	return f(ctx, invocation)
}

type SchemaValidator interface {
	Validate(context.Context, json.RawMessage, json.RawMessage) error
}

// ReferenceAwareSchemaValidator is an optional extension. Existing
// SchemaValidator implementations remain valid; filesystem-aware validators
// can use the owning schema path without exposing it on wire types.
type ReferenceAwareSchemaValidator interface {
	ValidateReference(
		context.Context,
		SchemaReference,
		json.RawMessage,
		json.RawMessage,
	) error
}

type Authorizer interface {
	Authorize(context.Context, HandlerDefinition, Invocation) error
}

type EffectPolicy interface {
	AuthorizeEffect(context.Context, HandlerDefinition, Invocation) error
}

type BudgetGovernor interface {
	Decide(context.Context, HandlerDefinition, Invocation) (BudgetDecision, error)
}

type SessionManager interface {
	CreateSession(context.Context, HandlerDefinition, Invocation) (string, error)
}

type EventRun func(context.Context) (OutcomeEnvelope, error)

// EventRuntime owns behavioral event modes. Background work must be submitted
// to a scheduler instead of running under the foreground request context, and
// interrupt mode must cancel active work before the handler is invoked.
type EventRuntime interface {
	Enqueue(context.Context, EventDefinition, HandlerDefinition, Invocation, EventRun) (OutcomeEnvelope, error)
	Interrupt(context.Context, EventDefinition, Invocation) error
}

type ReceiptSink interface {
	Record(context.Context, Receipt) error
}

type Dependencies struct {
	Schemas  SchemaValidator
	Auth     Authorizer
	Effects  EffectPolicy
	Budget   BudgetGovernor
	Sessions SessionManager
	Events   EventRuntime
	Receipts ReceiptSink
	Replay   ReplayStore
}

type registeredHandler struct {
	def     HandlerDefinition
	handler Handler
}

type Registry struct {
	mu       sync.RWMutex
	handlers map[string]registeredHandler
	events   map[string]EventDefinition
	deps     Dependencies
}

func NewRegistry(deps Dependencies) *Registry {
	return &Registry{
		handlers: make(map[string]registeredHandler),
		events:   make(map[string]EventDefinition),
		deps:     deps,
	}
}

func (r *Registry) RegisterHandler(def HandlerDefinition, handler Handler) error {
	if err := ValidateHandlerDefinition(def); err != nil {
		return err
	}
	if handler == nil {
		return fmt.Errorf("application: handler %q implementation is required", def.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[def.ID]; exists {
		return fmt.Errorf("application: handler %q is already registered", def.ID)
	}
	r.handlers[def.ID] = registeredHandler{def: cloneHandlerDefinition(def), handler: handler}
	return nil
}

func (r *Registry) RegisterEvent(def EventDefinition) error {
	if err := ValidateEventDefinition(def); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.events[def.ID]; exists {
		return fmt.Errorf("application: event %q is already registered", def.ID)
	}
	handler, ok := r.handlers[def.Handler]
	if !ok {
		return fmt.Errorf("%w: event %q targets %q", ErrHandlerNotFound, def.ID, def.Handler)
	}
	if err := validateEventHandlerSession(def, handler.def); err != nil {
		return err
	}
	r.events[def.ID] = cloneEventDefinition(def)
	return nil
}

func (r *Registry) Handler(id string) (HandlerDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.handlers[id]
	if !ok {
		return HandlerDefinition{}, false
	}
	return cloneHandlerDefinition(entry.def), true
}

func (r *Registry) Event(id string) (EventDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.events[id]
	if !ok {
		return EventDefinition{}, false
	}
	return cloneEventDefinition(def), true
}

func (r *Registry) Discover(transport Transport) []HandlerDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]HandlerDefinition, 0)
	for _, entry := range r.handlers {
		if exposed(entry.def, transport) {
			defs = append(defs, cloneHandlerDefinition(entry.def))
		}
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	return defs
}

func (r *Registry) DiscoverEvents() []EventDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]EventDefinition, 0, len(r.events))
	for _, def := range r.events {
		defs = append(defs, cloneEventDefinition(def))
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	return defs
}

// Validate checks references that cannot be proved while registrations are
// arriving, notably compensation handlers. Loaders should call it after
// registering one complete application contract.
func (r *Registry) Validate() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, entry := range r.handlers {
		def := entry.def
		if def.CompensationHandler == "" {
			continue
		}
		compensation, ok := r.handlers[def.CompensationHandler]
		if !ok {
			return fmt.Errorf("application: handler %q has unknown compensation handler %q", def.ID, def.CompensationHandler)
		}
		if compensation.def.Effect != EffectWrite && compensation.def.Effect != EffectExternal {
			return fmt.Errorf("application: compensation handler %q for %q must have write or external effect", compensation.def.ID, def.ID)
		}
	}
	for _, event := range r.events {
		if _, ok := r.handlers[event.Handler]; !ok {
			return fmt.Errorf("application: event %q has unknown handler %q", event.ID, event.Handler)
		}
	}
	return nil
}

func (r *Registry) Invoke(ctx context.Context, invocation Invocation) (OutcomeEnvelope, error) {
	r.mu.RLock()
	entry, ok := r.handlers[invocation.HandlerID]
	r.mu.RUnlock()
	if !ok {
		return OutcomeEnvelope{}, fmt.Errorf("%w: %q", ErrHandlerNotFound, invocation.HandlerID)
	}
	def := cloneHandlerDefinition(entry.def)
	if !exposed(def, invocation.Transport) && invocation.Transport != TransportEvent {
		return OutcomeEnvelope{}, fmt.Errorf("%w: handler %q on %q", ErrNotExposed, def.ID, invocation.Transport)
	}
	if !validTransport(invocation.Transport) {
		return OutcomeEnvelope{}, fmt.Errorf("application: invalid transport %q", invocation.Transport)
	}
	switch def.Session {
	case SessionNone:
		invocation.SessionID = ""
	case SessionRequired:
		if invocation.SessionID == "" {
			return OutcomeEnvelope{}, fmt.Errorf("application: handler %q requires a session", def.ID)
		}
	case SessionCreate:
		if r.deps.Sessions == nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: handler %q requires a session manager", def.ID)
		}
		if invocation.SessionPrepared && invocation.SessionID == "" {
			return OutcomeEnvelope{}, fmt.Errorf("application: handler %q received an empty prepared session", def.ID)
		}
	}
	requested := invocation.RoutingMode
	if requested == "" {
		requested = def.RoutingMode
	}
	if err := ValidateRoutingPin(def.RoutingMode, requested, requested); err != nil {
		return OutcomeEnvelope{}, err
	}
	input, err := NormalizeJSON(invocation.Input)
	if err != nil {
		return OutcomeEnvelope{}, err
	}
	invocation.Input = input
	invocation.RoutingMode = requested
	if invocation.IdempotencyKey == "" && def.IdempotencyKeyField != "" {
		invocation.IdempotencyKey, err = idempotencyKeyFromInput(input, def.IdempotencyKeyField)
		if err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: handler %q idempotency key: %w", def.ID, err)
		}
	}
	if def.Idempotency == IdempotencyRequired && invocation.IdempotencyKey == "" {
		return OutcomeEnvelope{}, fmt.Errorf("application: handler %q requires an idempotency key", def.ID)
	}
	if r.deps.Schemas != nil && len(def.InputSchema) > 0 {
		if err := validateSchema(
			ctx, r.deps.Schemas, def.InputSchemaReference, def.InputSchema, input,
		); err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: validate input for %q: %w", def.ID, err)
		}
	}
	if r.deps.Auth != nil {
		if err := r.deps.Auth.Authorize(ctx, def, invocation); err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
		}
	}
	if r.deps.Effects != nil {
		if err := r.deps.Effects.AuthorizeEffect(ctx, def, invocation); err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: effect policy denied %q: %w", def.ID, err)
		}
	}
	budget := BudgetDecision{Allowed: true}
	if r.deps.Budget != nil {
		budget, err = r.deps.Budget.Decide(ctx, def, invocation)
		if err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: budget policy for %q: %w", def.ID, err)
		}
		if !budget.Allowed {
			return OutcomeEnvelope{}, fmt.Errorf("%w: %s", ErrBudgetDenied, budget.Reason)
		}
	}

	inputDigest, _ := DigestJSON(input)
	execute := func() (OutcomeEnvelope, error) {
		effective := invocation
		if def.Session == SessionCreate && !invocation.SessionPrepared {
			created, createErr := r.deps.Sessions.CreateSession(ctx, def, invocation)
			if createErr != nil {
				return OutcomeEnvelope{}, fmt.Errorf("application: create session for %q: %w", def.ID, createErr)
			}
			if created == "" {
				return OutcomeEnvelope{}, fmt.Errorf("application: session manager returned an empty session for %q", def.ID)
			}
			effective.SessionID = created
		}
		return r.invokeHandler(ctx, entry.handler, def, effective, input, inputDigest, budget)
	}
	if invocation.IdempotencyKey != "" && r.deps.Replay != nil {
		outcome, replayErr, replayed := r.deps.Replay.Do(ctx, ReplayKey{
			HandlerID:   def.ID,
			SessionID:   replaySessionID(def, invocation),
			Key:         invocation.IdempotencyKey,
			InputDigest: inputDigest,
		}, execute)
		if !replayed {
			return outcome, replayErr
		}
		originalID := outcome.Receipt.ID
		replayReceipt := outcome.Receipt
		replayReceipt.ID = ""
		replayReceipt.Transport = invocation.Transport
		replayReceipt.Actor = invocation.Actor
		replayReceipt.EventID = invocation.EventID
		replayReceipt.EventMode = invocation.EventMode
		replayReceipt.FrameRevision = invocation.FrameRevision
		replayReceipt.Replayed = true
		replayReceipt.ReplayOf = originalID
		replayReceipt, err = FinalizeReceipt(replayReceipt)
		if err != nil {
			return OutcomeEnvelope{}, err
		}
		outcome.Receipt = replayReceipt
		if r.deps.Receipts != nil {
			if err := r.deps.Receipts.Record(ctx, replayReceipt); err != nil {
				return OutcomeEnvelope{}, fmt.Errorf("application: record replay receipt: %w", err)
			}
		}
		return outcome, replayErr
	}
	return execute()
}

func (r *Registry) invokeHandler(
	ctx context.Context,
	handler Handler,
	def HandlerDefinition,
	invocation Invocation,
	input json.RawMessage,
	inputDigest string,
	budget BudgetDecision,
) (OutcomeEnvelope, error) {
	result, invokeErr := handler.Invoke(ctx, invocation)
	if result.RoutingResolved == "" {
		result.RoutingResolved = invocation.RoutingMode
	}
	if err := ValidateRoutingPin(def.RoutingMode, invocation.RoutingMode, result.RoutingResolved); err != nil {
		return OutcomeEnvelope{}, err
	}
	outcomeError := (*OutcomeError)(nil)
	if invokeErr != nil {
		result.Outcome = "error"
		result.Output = json.RawMessage(`null`)
		outcomeError = &OutcomeError{Code: "handler_failed", Message: invokeErr.Error()}
	} else if !declaresOutcome(def, result.Outcome) {
		return OutcomeEnvelope{}, fmt.Errorf("application: handler %q returned undeclared outcome %q", def.ID, result.Outcome)
	}
	output, err := normalizeOutput(result.Output)
	if err != nil {
		return OutcomeEnvelope{}, fmt.Errorf("application: normalize output for %q: %w", def.ID, err)
	}
	if invokeErr == nil && r.deps.Schemas != nil && len(def.OutputSchema) > 0 {
		if err := validateSchema(
			ctx, r.deps.Schemas, def.OutputSchemaReference, def.OutputSchema, output,
		); err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: validate output for %q: %w", def.ID, err)
		}
	}
	outputDigest, _ := DigestJSON(output)
	receipt, err := FinalizeReceipt(Receipt{
		HandlerID:           def.ID,
		SemanticRef:         def.SemanticRef,
		SessionID:           invocationReceiptSession(invocation),
		Actor:               invocation.Actor,
		Effect:              def.Effect,
		Routing:             RoutingReceipt{Requested: invocation.RoutingMode, Resolved: result.RoutingResolved},
		Budget:              budget,
		IdempotencyKey:      invocation.IdempotencyKey,
		InputDigest:         inputDigest,
		OutputDigest:        outputDigest,
		Transport:           invocation.Transport,
		EventID:             invocation.EventID,
		EventMode:           invocation.EventMode,
		FrameRevision:       invocation.FrameRevision,
		Outcome:             result.Outcome,
		SelectedImplementor: result.SelectedImplementor,
	})
	if err != nil {
		return OutcomeEnvelope{}, err
	}
	envelope := OutcomeEnvelope{
		Schema: OutcomeSchema, Handler: def.ID, Outcome: result.Outcome,
		Output: output, Frame: result.Frame, Children: result.Children, Join: result.Join,
		Error: outcomeError, Receipt: receipt,
		SelectedImplementor: result.SelectedImplementor,
	}
	if r.deps.Receipts != nil {
		if err := r.deps.Receipts.Record(ctx, receipt); err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: record receipt: %w", err)
		}
	}
	return envelope, invokeErr
}

func (r *Registry) DispatchEvent(ctx context.Context, eventID string, input json.RawMessage, sessionID, actor string) (OutcomeEnvelope, error) {
	r.mu.RLock()
	event, ok := r.events[eventID]
	r.mu.RUnlock()
	if !ok {
		return OutcomeEnvelope{}, fmt.Errorf("%w: %q", ErrEventNotFound, eventID)
	}
	event = cloneEventDefinition(event)
	r.mu.RLock()
	target := cloneHandlerDefinition(r.handlers[event.Handler].def)
	r.mu.RUnlock()
	if actor == "" {
		actor = "event:" + event.Source
	}
	normalized, err := NormalizeJSON(input)
	if err != nil {
		return OutcomeEnvelope{}, err
	}
	if r.deps.Schemas != nil && len(event.InputSchema) > 0 {
		if err := validateSchema(
			ctx, r.deps.Schemas, event.InputSchemaReference, event.InputSchema, normalized,
		); err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: validate event %q: %w", event.ID, err)
		}
	}
	switch event.Session {
	case SessionNone:
		sessionID = ""
	case SessionRequired:
		if sessionID == "" {
			return OutcomeEnvelope{}, fmt.Errorf("application: event %q requires a session", event.ID)
		}
	case SessionCreate:
		if r.deps.Sessions == nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: event %q requires a session manager", event.ID)
		}
		createInvocation := Invocation{
			HandlerID: event.Handler, Input: normalized, SessionID: sessionID,
			Actor: actor, Transport: TransportEvent, RoutingMode: event.RoutingMode,
			EventID: event.ID, EventMode: event.Mode,
		}
		created, createErr := r.deps.Sessions.CreateSession(ctx, target, createInvocation)
		if createErr != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: create session for event %q: %w", event.ID, createErr)
		}
		if created == "" {
			return OutcomeEnvelope{}, fmt.Errorf("application: session manager returned an empty session for event %q", event.ID)
		}
		sessionID = created
	}
	invocation := Invocation{
		HandlerID: event.Handler, Input: normalized, SessionID: sessionID,
		Actor: actor, Transport: TransportEvent, RoutingMode: event.RoutingMode,
		EventID: event.ID, EventMode: event.Mode,
		EventSessionID: sessionID, SessionPrepared: event.Session == SessionCreate,
	}
	run := func(runCtx context.Context) (OutcomeEnvelope, error) {
		return r.Invoke(runCtx, invocation)
	}
	switch event.Mode {
	case EventBackground:
		if r.deps.Events == nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: event %q requires a background scheduler", event.ID)
		}
		outcome, enqueueErr := r.deps.Events.Enqueue(ctx, event, target, invocation, run)
		if enqueueErr == nil && r.deps.Receipts != nil && outcome.Receipt.ID != "" {
			if err := r.deps.Receipts.Record(ctx, outcome.Receipt); err != nil {
				return OutcomeEnvelope{}, fmt.Errorf("application: record event receipt: %w", err)
			}
		}
		return outcome, enqueueErr
	case EventInterrupt:
		if r.deps.Events == nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: event %q requires an interrupt runtime", event.ID)
		}
		if err := r.deps.Events.Interrupt(ctx, event, invocation); err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: interrupt event %q: %w", event.ID, err)
		}
	}
	return run(ctx)
}

func validateEventHandlerSession(event EventDefinition, handler HandlerDefinition) error {
	switch {
	case event.Session == SessionNone && handler.Session == SessionRequired:
		return fmt.Errorf("application: event %q with session:none cannot target required-session handler %q", event.ID, handler.ID)
	case event.Session == SessionRequired && handler.Session == SessionCreate:
		return fmt.Errorf("application: event %q with session:required cannot replace its bound session through create-session handler %q", event.ID, handler.ID)
	default:
		return nil
	}
}

func invocationReceiptSession(invocation Invocation) string {
	if invocation.EventID != "" && invocation.EventSessionID != "" {
		return invocation.EventSessionID
	}
	return invocation.SessionID
}

func idempotencyKeyFromInput(input json.RawMessage, field string) (string, error) {
	var values map[string]any
	if err := json.Unmarshal(input, &values); err != nil {
		return "", err
	}
	path := strings.Split(strings.TrimPrefix(field, "input."), ".")
	var value any = values
	for _, segment := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return "", fmt.Errorf("input path %q crosses a non-object value", field)
		}
		value, ok = object[segment]
		if !ok || value == nil {
			return "", nil
		}
	}
	switch typed := value.(type) {
	case string:
		return typed, nil
	case float64, bool:
		raw, _ := json.Marshal(typed)
		return string(raw), nil
	default:
		return "", fmt.Errorf("input field %q must be a scalar string, number, or boolean", field)
	}
}

func replaySessionID(def HandlerDefinition, invocation Invocation) string {
	switch def.IdempotencyScope {
	case "application":
		return ""
	case "request":
		return "request:" + string(invocation.Transport) + ":" + invocation.Actor
	default:
		return "session:" + invocation.SessionID
	}
}

func normalizeOutput(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`null`)
	}
	return NormalizeJSON(raw)
}

func declaresOutcome(def HandlerDefinition, outcome string) bool {
	for _, declared := range def.Outcomes {
		if declared == outcome {
			return true
		}
	}
	return false
}

func exposed(def HandlerDefinition, transport Transport) bool {
	for _, exposedTransport := range def.Expose {
		if exposedTransport == transport {
			return true
		}
	}
	return false
}

func cloneHandlerDefinition(def HandlerDefinition) HandlerDefinition {
	def.InputSchema = append(json.RawMessage(nil), def.InputSchema...)
	def.OutputSchema = append(json.RawMessage(nil), def.OutputSchema...)
	def.Outcomes = append([]string(nil), def.Outcomes...)
	def.Expose = append([]Transport(nil), def.Expose...)
	return def
}

func cloneEventDefinition(def EventDefinition) EventDefinition {
	def.InputSchema = append(json.RawMessage(nil), def.InputSchema...)
	return def
}

func validateSchema(
	ctx context.Context,
	validator SchemaValidator,
	reference SchemaReference,
	schema json.RawMessage,
	value json.RawMessage,
) error {
	if aware, ok := validator.(ReferenceAwareSchemaValidator); ok {
		return aware.ValidateReference(ctx, reference, schema, value)
	}
	return validator.Validate(ctx, schema, value)
}
