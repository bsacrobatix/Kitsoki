package host

import (
	"context"
	"testing"

	"kitsoki/internal/authoring"
)

// OverrideProposeRunner replaces the proposeRunner package variable
// with a test stub for the duration of a test. Returns a function
// callers must invoke to restore the original. Provided as an
// exported helper so the test package (which lives in
// internal/host_test by convention) can swap the runner without
// poking package internals.
//
// The *testing.T argument is unused — its presence prevents callers
// from accidentally calling this outside a test binary.
func OverrideProposeRunner(_ *testing.T, fn func(ctx context.Context, appPath, text string, runCtx *authoring.Context) (*authoring.Proposal, error)) func() {
	prev := proposeRunner
	proposeRunner = fn
	return func() { proposeRunner = prev }
}

// OverrideApplyRunner is the apply-runner analogue of
// OverrideProposeRunner.
func OverrideApplyRunner(_ *testing.T, fn func(p *authoring.Proposal) error) func() {
	prev := applyRunner
	applyRunner = fn
	return func() { applyRunner = prev }
}
