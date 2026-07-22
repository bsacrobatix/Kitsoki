// Package testrunner_test — G-FLOW gate tests for the no-on_error: sibling
// of the never-silent-bounce gate (Task A,
// .context/troubleshooting-agent-and-error-integrity.md Part 1, Leak 1).
//
// assertNoSilentUnhandledHostError (flows.go) is the counterpart to
// assertNoSilentOnErrorBounce for a host call that fails with NO on_error:
// declared — dispatch continues rather than redirecting, so no
// intent="on_error" TransitionApplied fires for the other gate to key off.
// These tests prove it has teeth using the testdata/apps/error_banner
// fixtures (unhandled_domain_error_banners_and_logs.yaml /
// unhandled_infra_error_banners_and_logs.yaml), both of which drive
// do_thing_unhandled → working_unhandled with no `on:` handlers and no
// on_error: declared on the invoke.
package testrunner_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/store"
	"kitsoki/internal/testrunner"
)

// TestGFlowUnhandledGate_PassesWhenBannerPresent proves the positive case:
// dispatchHostCalls (host_dispatch.go) now runs the post-loop view through
// applyErrorBannerSeam whenever a call failed with no on_error: declared
// (the unhandledFailure flag), so both the domain- and infra-failure
// fixtures pass the gate without an opt-in assertion doing the work.
func TestGFlowUnhandledGate_PassesWhenBannerPresent(t *testing.T) {
	const appPath = "../../testdata/apps/error_banner/app.yaml"

	for _, glob := range []string{
		"../../testdata/apps/error_banner/flows/unhandled_domain_error_banners_and_logs.yaml",
		"../../testdata/apps/error_banner/flows/unhandled_infra_error_banners_and_logs.yaml",
	} {
		t.Run(glob, func(t *testing.T) {
			report, err := testrunner.RunFlows(t.Context(), appPath, glob, testrunner.FlowOptions{})
			require.NoError(t, err, "RunFlows should not return a fatal error")
			require.Equal(t, 1, report.Passed, "fixture should pass the G-FLOW unhandled-error gate")
			require.Equal(t, 0, report.Failed)

			for _, r := range report.Results {
				for _, tr := range r.Turns {
					require.True(t, tr.Passed, "turn %d failures: %v", tr.TurnIndex+1, tr.Failures)
				}
			}
		})
	}
}

// TestGFlowUnhandledGate_FailureShapeIsHostReturnedNotRedirect confirms the
// fixture actually exercises the gate's target shape — a HostReturned error
// with NO accompanying on_error TransitionApplied — rather than accidentally
// passing because the gate's precondition scan never found anything to
// check. Without this, TestGFlowUnhandledGate_PassesWhenBannerPresent could
// pass vacuously (e.g. if the stub never actually errored).
func TestGFlowUnhandledGate_FailureShapeIsHostReturnedNotRedirect(t *testing.T) {
	const appPath = "../../testdata/apps/error_banner/app.yaml"
	const glob = "../../testdata/apps/error_banner/flows/unhandled_domain_error_banners_and_logs.yaml"

	report, err := testrunner.RunFlows(t.Context(), appPath, glob, testrunner.FlowOptions{})
	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	require.Len(t, report.Results[0].Turns, 1)

	tr := report.Results[0].Turns[0]
	var sawHostReturnedError, sawOnErrorRedirect bool
	for _, ev := range tr.Events {
		switch ev.Kind {
		case store.HostReturned:
			var p map[string]any
			if ev.Payload != nil && json.Unmarshal(ev.Payload, &p) == nil {
				if errMsg, _ := p["error"].(string); errMsg != "" {
					sawHostReturnedError = true
				}
			}
		case store.TransitionApplied:
			var p map[string]any
			if ev.Payload != nil && json.Unmarshal(ev.Payload, &p) == nil {
				if intentName, _ := p["intent"].(string); intentName == "on_error" {
					sawOnErrorRedirect = true
				}
			}
		}
	}
	require.True(t, sawHostReturnedError, "fixture must actually produce a HostReturned error event")
	require.False(t, sawOnErrorRedirect, "this fixture must NOT redirect — it is the no-on_error: leak-1 shape")
}
