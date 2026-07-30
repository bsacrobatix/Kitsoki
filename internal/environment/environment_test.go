package environment

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"kitsoki/internal/clock"
)

func TestFakeSubmitIsIdempotentAndPollsOnControlledTime(t *testing.T) {
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	virtual := clock.NewFake(start)
	fake := NewFake(WithClock(virtual))
	fake.Enqueue(Program{ActionKind: "provision", Steps: []FakeStep{
		{After: time.Minute, State: OperationRunning, Outcome: Blocked(ReasonOperationPending, "provider operation is running", "poll the operation receipt")},
		{After: 2 * time.Minute, State: OperationSucceeded, Outcome: Passed(), Resource: &Resource{Ref: "web-1", Kind: "host", Healthy: true, Current: false}},
	}})
	request := SubmitRequest{Action: Action{Kind: "provision", TargetRef: "web-1"}, IdempotencyKey: "deployment:abc"}
	first, err := fake.Submit(context.Background(), request)
	require.NoError(t, err)
	require.False(t, first.Receipt.Replayed)
	require.Equal(t, OperationPending, mustPoll(t, fake, first.Receipt.OperationRef).Operation.State)

	second, err := fake.Submit(context.Background(), request)
	require.NoError(t, err)
	require.True(t, second.Receipt.Replayed)
	require.Equal(t, first.Receipt.OperationRef, second.Receipt.OperationRef)
	require.Len(t, fake.Journal(), 1)

	virtual.Advance(2 * time.Minute)
	poll := mustPoll(t, fake, first.Receipt.OperationRef)
	require.Equal(t, OperationSucceeded, poll.Operation.State)
	require.Equal(t, OutcomePassed, poll.Outcome.Status)

	verify, err := fake.Verify(context.Background(), VerifyRequest{Postcondition: Postcondition{ResourceRef: "web-1", RequireHealthy: true, RequireCurrent: true}})
	require.NoError(t, err)
	require.Equal(t, OutcomeBlocked, verify.Outcome.Status)
	require.Equal(t, ReasonNotCurrent, verify.Outcome.Reason.Code)
}

func TestFakeVerifyRequiresExactPostconditions(t *testing.T) {
	fake := NewFake(WithResource(Resource{Ref: "database", Healthy: true, Current: true, Attributes: map[string]string{"backend": "postgres"}}))
	result, err := fake.Verify(context.Background(), VerifyRequest{Postcondition: Postcondition{ResourceRef: "database", RequireHealthy: true, RequireCurrent: true, Attributes: map[string]string{"backend": "mysql"}}})
	require.NoError(t, err)
	require.Equal(t, ReasonAttributeMismatch, result.Outcome.Reason.Code)
}

func mustPoll(t *testing.T, fake *Fake, ref string) PollResult {
	t.Helper()
	result, err := fake.Poll(context.Background(), PollRequest{OperationRef: ref})
	require.NoError(t, err)
	return result
}
