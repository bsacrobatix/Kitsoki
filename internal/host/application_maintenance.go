package host

import (
	"context"
	"fmt"
)

const (
	SessionReconciliationVerb = "host.session_reconciliation.reconcile"
	WorkerFleetVerb           = "host.worker_fleet.reconcile"
	CampaignSupervisionVerb   = "host.campaign_supervision.reconcile"
)

type ApplicationMaintenanceInvoker func(context.Context) (map[string]any, error)

func NewApplicationMaintenanceHandler(
	verb string,
	invoke ApplicationMaintenanceInvoker,
) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		if len(args) != 0 {
			return Result{}, fmt.Errorf("%s accepts no caller authority", verb)
		}
		if invoke == nil {
			return Result{Error: verb + ": configured service is unavailable"}, nil
		}
		receipt, err := invoke(ctx)
		if err != nil {
			return Result{Error: verb + ": " + err.Error()}, nil
		}
		return Result{Data: map[string]any{"receipt": receipt}}, nil
	}
}

var (
	SessionReconciliationHandler = NewApplicationMaintenanceHandler(SessionReconciliationVerb, nil)
	WorkerFleetHandler           = NewApplicationMaintenanceHandler(WorkerFleetVerb, nil)
	CampaignSupervisionHandler   = NewApplicationMaintenanceHandler(CampaignSupervisionVerb, nil)
)
