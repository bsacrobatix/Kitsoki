package application

import (
	"fmt"
	"strings"
)

type SessionPolicy string
type EffectClass string
type RoutingMode string
type IdempotencyPolicy string
type EventMode string
type Transport string

const (
	SessionNone     SessionPolicy = "none"
	SessionRequired SessionPolicy = "required"
	SessionCreate   SessionPolicy = "create"

	EffectPure     EffectClass = "pure"
	EffectRead     EffectClass = "read"
	EffectWrite    EffectClass = "write"
	EffectExternal EffectClass = "external"

	RoutingOff      RoutingMode = "off"
	RoutingExact    RoutingMode = "exact"
	RoutingSynonym  RoutingMode = "synonym"
	RoutingSemantic RoutingMode = "semantic"
	RoutingLLM      RoutingMode = "llm"

	IdempotencyOptional IdempotencyPolicy = "optional"
	IdempotencyRequired IdempotencyPolicy = "required"

	EventBackground EventMode = "background"
	EventInterrupt  EventMode = "interrupt"

	TransportJSONRPC Transport = "jsonrpc"
	TransportMCP     Transport = "mcp"
	TransportCLI     Transport = "cli"
	TransportWeb     Transport = "web"
	TransportVSCode  Transport = "vscode"
	TransportTUI     Transport = "tui"
	TransportEvent   Transport = "event"
)

var routingStrength = map[RoutingMode]int{
	RoutingOff: 0, RoutingExact: 1, RoutingSynonym: 2, RoutingSemantic: 3, RoutingLLM: 4,
}

func ValidateHandlerDefinition(def HandlerDefinition) error {
	if strings.TrimSpace(def.ID) == "" {
		return fmt.Errorf("application: handler id is required")
	}
	if strings.TrimSpace(def.Name) == "" || strings.TrimSpace(def.Description) == "" {
		return fmt.Errorf("application: handler %q requires name and description", def.ID)
	}
	if strings.TrimSpace(def.SemanticRef) == "" {
		return fmt.Errorf("application: handler %q requires semantic_ref", def.ID)
	}
	switch def.Session {
	case SessionNone, SessionRequired, SessionCreate:
	default:
		return fmt.Errorf("application: handler %q has invalid session policy %q", def.ID, def.Session)
	}
	switch def.Effect {
	case EffectPure, EffectRead, EffectWrite, EffectExternal:
	default:
		return fmt.Errorf("application: handler %q has invalid effect %q", def.ID, def.Effect)
	}
	if _, ok := routingStrength[def.RoutingMode]; !ok {
		return fmt.Errorf("application: handler %q has invalid routing mode %q", def.ID, def.RoutingMode)
	}
	if len(def.Outcomes) == 0 {
		return fmt.Errorf("application: handler %q must declare outcomes", def.ID)
	}
	outcomes := map[string]struct{}{}
	for _, outcome := range def.Outcomes {
		if strings.TrimSpace(outcome) == "" {
			return fmt.Errorf("application: handler %q has an empty outcome", def.ID)
		}
		if _, ok := outcomes[outcome]; ok {
			return fmt.Errorf("application: handler %q repeats outcome %q", def.ID, outcome)
		}
		outcomes[outcome] = struct{}{}
	}
	exposure := map[Transport]struct{}{}
	for _, transport := range def.Expose {
		if !validTransport(transport) || transport == TransportEvent {
			return fmt.Errorf("application: handler %q has invalid exposure %q", def.ID, transport)
		}
		if _, ok := exposure[transport]; ok {
			return fmt.Errorf("application: handler %q repeats exposure %q", def.ID, transport)
		}
		exposure[transport] = struct{}{}
	}
	if def.Effect == EffectWrite || def.Effect == EffectExternal {
		switch def.Idempotency {
		case IdempotencyOptional, IdempotencyRequired:
		default:
			return fmt.Errorf("application: %s handler %q must declare idempotency policy", def.Effect, def.ID)
		}
	} else if def.Idempotency != "" {
		return fmt.Errorf("application: %s handler %q may not declare idempotency policy", def.Effect, def.ID)
	}
	if def.Retryable {
		if def.Effect != EffectExternal {
			return fmt.Errorf("application: retryable handler %q must have external effect", def.ID)
		}
		if def.Idempotency != IdempotencyRequired {
			return fmt.Errorf("application: retryable external handler %q requires idempotency", def.ID)
		}
		if def.CompensationHandler == "" && strings.TrimSpace(def.NoCompensationReason) == "" {
			return fmt.Errorf("application: retryable external handler %q requires compensation or a no-compensation reason", def.ID)
		}
	}
	if def.CompensationHandler != "" && def.CompensationHandler == def.ID {
		return fmt.Errorf("application: handler %q cannot compensate itself", def.ID)
	}
	return nil
}

func ValidateEventDefinition(def EventDefinition) error {
	if strings.TrimSpace(def.ID) == "" || strings.TrimSpace(def.Source) == "" {
		return fmt.Errorf("application: event id and source are required")
	}
	if strings.TrimSpace(def.Handler) == "" {
		return fmt.Errorf("application: event %q requires handler", def.ID)
	}
	switch def.Session {
	case SessionNone, SessionRequired, SessionCreate:
	default:
		return fmt.Errorf("application: event %q has invalid session policy %q", def.ID, def.Session)
	}
	switch def.Mode {
	case EventBackground, EventInterrupt:
	default:
		return fmt.Errorf("application: event %q has invalid mode %q", def.ID, def.Mode)
	}
	return nil
}

func ValidateRoutingPin(declared, requested, resolved RoutingMode) error {
	if requested == "" {
		requested = declared
	}
	declaredStrength, ok := routingStrength[declared]
	if !ok {
		return fmt.Errorf("application: invalid declared routing mode %q", declared)
	}
	requestedStrength, ok := routingStrength[requested]
	if !ok {
		return fmt.Errorf("application: invalid requested routing mode %q", requested)
	}
	if requestedStrength > declaredStrength {
		return fmt.Errorf("application: requested routing mode %q weakens handler pin %q", requested, declared)
	}
	if resolved == "" {
		resolved = requested
	}
	resolvedStrength, ok := routingStrength[resolved]
	if !ok {
		return fmt.Errorf("application: invalid resolved routing mode %q", resolved)
	}
	if resolvedStrength > requestedStrength {
		return fmt.Errorf("application: routing resolved as %q beyond requested pin %q", resolved, requested)
	}
	return nil
}

func validTransport(transport Transport) bool {
	switch transport {
	case TransportJSONRPC, TransportMCP, TransportCLI, TransportWeb, TransportVSCode, TransportTUI, TransportEvent:
		return true
	default:
		return false
	}
}
