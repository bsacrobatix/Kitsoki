package host

import (
	"context"
	"fmt"
	"math"
)

// CampaignController is the daemon-owned campaign runtime used by the generic
// host adapter. Implementations fix graph and application scope at
// construction; host arguments cannot select either.
type CampaignController interface {
	Watch(context.Context, string, int) (map[string]any, error)
	Snapshot(context.Context, string, int, int) (map[string]any, error)
}

// NewCampaignHandler exposes the daemon-owned campaign controller to one
// application.
func NewCampaignHandler(controller CampaignController, appID string) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		if controller == nil || appID == "" {
			return Result{
				Error: "host.campaign: durable campaign runtime is unavailable outside daemon mode",
			}, nil
		}
		op, _ := args["op"].(string)
		switch op {
		case "watch":
			pollSeconds, err := campaignIntArg(args, "poll_seconds")
			if err != nil {
				return Result{}, fmt.Errorf("host.campaign.watch: %w", err)
			}
			data, err := controller.Watch(ctx, appID, pollSeconds)
			if err != nil {
				return Result{}, fmt.Errorf("host.campaign.watch: %w", err)
			}
			return Result{Data: data}, nil
		case "snapshot":
			maxCampaigns, err := campaignIntArg(args, "max_campaigns")
			if err != nil {
				return Result{}, fmt.Errorf("host.campaign.snapshot: %w", err)
			}
			maxBytes, err := campaignIntArg(args, "max_bytes")
			if err != nil {
				return Result{}, fmt.Errorf("host.campaign.snapshot: %w", err)
			}
			snapshot, err := controller.Snapshot(ctx, appID, maxCampaigns, maxBytes)
			if err != nil {
				return Result{}, fmt.Errorf("host.campaign.snapshot: %w", err)
			}
			return Result{Data: map[string]any{"snapshot": snapshot}}, nil
		default:
			return Result{}, fmt.Errorf("host.campaign: unknown op %q (want watch or snapshot)", op)
		}
	}
}

// CampaignHandler is the builtin sentinel. SessionRegistry replaces it only
// for daemon-backed application runtimes with a configured graph catalog.
var CampaignHandler = NewCampaignHandler(nil, "")

func campaignIntArg(args map[string]any, name string) (int, error) {
	raw, ok := args[name]
	if !ok {
		return 0, fmt.Errorf("missing required arg %q", name)
	}
	switch value := raw.(type) {
	case int:
		return value, nil
	case int64:
		if int64(int(value)) != value {
			return 0, fmt.Errorf("%q is outside the supported integer range", name)
		}
		return int(value), nil
	case float64:
		if math.Trunc(value) != value || value > float64(math.MaxInt) || value < float64(math.MinInt) {
			return 0, fmt.Errorf("%q must be an integer", name)
		}
		return int(value), nil
	default:
		return 0, fmt.Errorf("%q must be an integer, got %T", name, raw)
	}
}
