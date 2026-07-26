package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	Description          string            `json:"description"`
	SemanticRef          string            `json:"semantic_ref"`
	InputSchema          json.RawMessage   `json:"input_schema,omitempty"`
	OutputSchema         json.RawMessage   `json:"output_schema,omitempty"`
	Session              SessionPolicy     `json:"session"`
	Effect               EffectClass       `json:"effect"`
	RoutingMode          RoutingMode       `json:"routing_mode"`
	Outcomes             []string          `json:"outcomes"`
	Expose               []Transport       `json:"expose,omitempty"`
	Idempotency          IdempotencyPolicy `json:"idempotency,omitempty"`
	Retryable            bool              `json:"retryable,omitempty"`
	CompensationHandler  string            `json:"compensation_handler,omitempty"`
	NoCompensationReason string            `json:"no_compensation_reason,omitempty"`
}

type EventDefinition struct {
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Session     SessionPolicy   `json:"session"`
	Mode        EventMode       `json:"mode"`
	Handler     string          `json:"handler"`
}

type Invocation struct {
	HandlerID      string
	Input          json.RawMessage
	SessionID      string
	Actor          string
	Transport      Transport
	RoutingMode    RoutingMode
	IdempotencyKey string
	EventID        string
	FrameRevision  uint64
}

type HandlerResult struct {
	Outcome         string
	Output          json.RawMessage
	Frame           *Frame
	Children        []ChildRun
	Join            *JoinState
	RoutingResolved RoutingMode
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

type Authorizer interface {
	Authorize(context.Context, HandlerDefinition, Invocation) error
}

type BudgetGovernor interface {
	Decide(context.Context, HandlerDefinition, Invocation) (BudgetDecision, error)
}

type ReceiptSink interface {
	Record(context.Context, Receipt) error
}

type Dependencies struct {
	Schemas  SchemaValidator
	Auth     Authorizer
	Budget   BudgetGovernor
	Receipts ReceiptSink
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
	if handler.def.Session == SessionRequired && def.Session == SessionNone {
		return fmt.Errorf("application: event %q cannot supply session policy required by handler %q", def.ID, def.Handler)
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
	if def.Session == SessionRequired && invocation.SessionID == "" {
		return OutcomeEnvelope{}, fmt.Errorf("application: handler %q requires a session", def.ID)
	}
	if def.Idempotency == IdempotencyRequired && invocation.IdempotencyKey == "" {
		return OutcomeEnvelope{}, fmt.Errorf("application: handler %q requires an idempotency key", def.ID)
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
	if r.deps.Schemas != nil && len(def.InputSchema) > 0 {
		if err := r.deps.Schemas.Validate(ctx, def.InputSchema, input); err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: validate input for %q: %w", def.ID, err)
		}
	}
	if r.deps.Auth != nil {
		if err := r.deps.Auth.Authorize(ctx, def, invocation); err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
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

	result, invokeErr := entry.handler.Invoke(ctx, invocation)
	if result.RoutingResolved == "" {
		result.RoutingResolved = requested
	}
	if err := ValidateRoutingPin(def.RoutingMode, requested, result.RoutingResolved); err != nil {
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
		if err := r.deps.Schemas.Validate(ctx, def.OutputSchema, output); err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: validate output for %q: %w", def.ID, err)
		}
	}
	inputDigest, _ := DigestJSON(input)
	outputDigest, _ := DigestJSON(output)
	receipt, err := FinalizeReceipt(Receipt{
		HandlerID:      def.ID,
		SemanticRef:    def.SemanticRef,
		SessionID:      invocation.SessionID,
		Actor:          invocation.Actor,
		Effect:         def.Effect,
		Routing:        RoutingReceipt{Requested: requested, Resolved: result.RoutingResolved},
		Budget:         budget,
		IdempotencyKey: invocation.IdempotencyKey,
		InputDigest:    inputDigest,
		OutputDigest:   outputDigest,
		Transport:      invocation.Transport,
		EventID:        invocation.EventID,
		FrameRevision:  invocation.FrameRevision,
		Outcome:        result.Outcome,
	})
	if err != nil {
		return OutcomeEnvelope{}, err
	}
	envelope := OutcomeEnvelope{
		Schema: OutcomeSchema, Handler: def.ID, Outcome: result.Outcome,
		Output: output, Frame: result.Frame, Children: result.Children, Join: result.Join,
		Error: outcomeError, Receipt: receipt,
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
	if event.Session == SessionRequired && sessionID == "" {
		return OutcomeEnvelope{}, fmt.Errorf("application: event %q requires a session", event.ID)
	}
	normalized, err := NormalizeJSON(input)
	if err != nil {
		return OutcomeEnvelope{}, err
	}
	if r.deps.Schemas != nil && len(event.InputSchema) > 0 {
		if err := r.deps.Schemas.Validate(ctx, event.InputSchema, normalized); err != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: validate event %q: %w", event.ID, err)
		}
	}
	return r.Invoke(ctx, Invocation{
		HandlerID: event.Handler, Input: normalized, SessionID: sessionID,
		Actor: actor, Transport: TransportEvent, EventID: event.ID,
	})
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
