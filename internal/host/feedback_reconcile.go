package host

import (
	"context"
	"fmt"
)

const (
	ReviewedFeedbackCampaignReconcileVerb = "host.reviewed_feedback_campaign.reconcile"
	FeedbackIntakeReconcileVerb           = "host.feedback_intake.reconcile"
	FeedbackFederationReconcileVerb       = "host.feedback_federation.reconcile"
)

// FeedbackReconcileInvoker is injected during daemon application wiring. It
// captures all application scope and configured source/target authority.
type FeedbackReconcileInvoker func(context.Context) (Result, error)

func NewFeedbackReconcileHandler(
	verb string,
	invoke FeedbackReconcileInvoker,
) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		if len(args) != 0 {
			return Result{}, fmt.Errorf("%s accepts no caller authority", verb)
		}
		if invoke == nil {
			return Result{Error: verb + ": configured service is unavailable"}, nil
		}
		result, err := invoke(ctx)
		if err != nil {
			return Result{Error: verb + ": " + err.Error()}, nil
		}
		return result, nil
	}
}

var (
	ReviewedFeedbackCampaignReconcileHandler = NewFeedbackReconcileHandler(
		ReviewedFeedbackCampaignReconcileVerb, nil,
	)
	FeedbackIntakeReconcileHandler = NewFeedbackReconcileHandler(
		FeedbackIntakeReconcileVerb, nil,
	)
	FeedbackFederationReconcileHandler = NewFeedbackReconcileHandler(
		FeedbackFederationReconcileVerb, nil,
	)
)
