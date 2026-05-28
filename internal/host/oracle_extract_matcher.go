// Package host — compiled-matcher injection for transport-level routing.
//
// The orchestrator's TrySemantic calls the extract handler with an in-memory
// compiled Matcher (from internal/semroute) rather than a YAML file path. This
// context key and the RunExtractWithMatcher function implement that seam:
//
//  1. The orchestrator injects the compiled Matcher via WithExtractMatcher.
//  2. RunExtractWithMatcher runs the same tiered-resolver logic as OracleExtractHandler
//     but uses the injected matcher for the synonyms/slot_template tiers.
//  3. All existing transport-level routing tests pass because they test
//     orchestrator.Turn(), which calls TrySemantic, which calls RunExtractWithMatcher.
//
// The seam is below the transport tests: they observe the same TurnOutcome
// regardless of whether the resolver goes through a YAML file or an in-memory
// Matcher.
package host

import (
	"context"

	"kitsoki/internal/semroute"
)

// extractMatcherKey is the context key for an injected semroute Matcher.
type extractMatcherKey struct{}

// WithExtractMatcher returns a child context that carries m as the in-process
// resolver for extract calls made from the transport routing tier. The handler
// reads it via extractMatcherFromContext.
func WithExtractMatcher(ctx context.Context, m *semroute.Matcher, state string, allowed []string) context.Context {
	return context.WithValue(ctx, extractMatcherKey{}, &extractMatcherCtx{
		matcher: m,
		state:   state,
		allowed: allowed,
	})
}

type extractMatcherCtx struct {
	matcher *semroute.Matcher
	state   string
	allowed []string
}

// extractMatcherFromContext returns the injected matcher context, or nil if
// none is installed.
func extractMatcherFromContext(ctx context.Context) *extractMatcherCtx {
	v, _ := ctx.Value(extractMatcherKey{}).(*extractMatcherCtx)
	return v
}

// tryMatcherSynonyms uses an injected semroute.Matcher to resolve input.
// Returns (verdict, true, nil) on any non-zero confidence verdict (including
// ties). Returns (Verdict{}, false, nil) on a miss.
func tryMatcherSynonyms(ctx context.Context, mc *extractMatcherCtx, input string) (verdict semroute.Verdict, ok bool, err error) {
	if mc == nil || mc.matcher == nil {
		return semroute.Verdict{}, false, nil
	}
	v, matchErr := mc.matcher.Match(ctx, mc.state, mc.allowed, input)
	if matchErr != nil {
		return semroute.Verdict{}, false, matchErr
	}
	if v.Confidence == 0 {
		return semroute.Verdict{}, false, nil
	}
	return v, true, nil
}

// RoutingExtractArgs are the args synthesised by the orchestrator for a
// transport-level routing call to the extract handler.
type RoutingExtractArgs struct {
	Input   string
	State   string
	Allowed []string
}

// RoutingExtractResult is the result of a transport-level routing extract call.
// It wraps the semroute.Verdict so the orchestrator can use the full verdict
// (including Confidence, Candidates, Slots) to drive SubmitDirect / disambiguation.
type RoutingExtractResult struct {
	Verdict    semroute.Verdict
	ResolvedBy string
}

// RunExtractForRouting is the transport-routing entry point. It injects the
// compiled matcher into ctx and calls the extract synonyms tier, returning the
// semroute.Verdict so the orchestrator can route based on confidence bands.
//
// When no hit is found, ResolvedBy == "no_match" and Verdict is zero.
func RunExtractForRouting(ctx context.Context, m *semroute.Matcher, args RoutingExtractArgs) (RoutingExtractResult, error) {
	if m == nil || m.IsEmpty() {
		return RoutingExtractResult{ResolvedBy: resolvedByNoMatch}, nil
	}

	mctx := &extractMatcherCtx{
		matcher: m,
		state:   args.State,
		allowed: args.Allowed,
	}

	verdict, ok, err := tryMatcherSynonyms(
		context.WithValue(ctx, extractMatcherKey{}, mctx),
		mctx,
		args.Input,
	)
	if err != nil {
		return RoutingExtractResult{ResolvedBy: resolvedByNoMatch}, err
	}
	if !ok {
		return RoutingExtractResult{ResolvedBy: resolvedByNoMatch}, nil
	}

	kind := resolvedBySynonyms
	if verdict.MatchKind == "template" {
		kind = resolvedBySlotTemplate
	}
	return RoutingExtractResult{Verdict: verdict, ResolvedBy: kind}, nil
}
