package main

import (
	"os"
	"strconv"
	"strings"

	"kitsoki/internal/orchestrator"
)

// semanticRoutingFlag backs the global --semantic-routing persistent flag and
// semanticRoutingFlagSet records whether the operator passed it (set in the
// root PersistentPreRunE). Together with KITSOKI_SEMANTIC_ROUTING they drive an
// optional orchestrator-level override for the deterministic semantic-routing
// stack.
var (
	semanticRoutingFlag    bool
	semanticRoutingFlagSet bool
)

// semanticRoutingOptions resolves the semantic-routing toggle and returns the
// orchestrator option that wires the process-level default.
// Precedence (highest first):
//
//  1. --semantic-routing (when the operator passed it explicitly)
//  2. KITSOKI_SEMANTIC_ROUTING (1/true/on / 0/false/off, case-insensitive)
//  3. default: disabled; keep only exact deterministic routing, then LLM
//
// The per-app routing.enabled config is still available to lower-level
// orchestrator callers that do not pass this option. The CLI defaults to the
// simpler deterministic-then-LLM route until the semantic/default-intent stack
// is stable enough to opt back in by default.
func semanticRoutingOptions() []orchestrator.Option {
	enabled, ok := semanticRoutingOverride()
	if !ok {
		enabled = false
	}
	return []orchestrator.Option{orchestrator.WithSemanticRouting(enabled)}
}

// semanticRoutingOverride resolves the command/env override. ok=false means
// "unset; defer to per-app routing.enabled".
func semanticRoutingOverride() (enabled bool, ok bool) {
	if semanticRoutingFlagSet {
		return semanticRoutingFlag, true
	}
	if v, ok := os.LookupEnv("KITSOKI_SEMANTIC_ROUTING"); ok {
		if b, err := parseEnvBool(v); err == nil {
			return b, true
		}
	}
	return false, false
}

// parseEnvBool accepts the usual truthy/falsey spellings plus on/off.
func parseEnvBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "yes":
		return true, nil
	case "off", "no":
		return false, nil
	}
	return strconv.ParseBool(strings.TrimSpace(v))
}
