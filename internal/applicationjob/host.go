package applicationjob

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"kitsoki/internal/host"
)

// NewHandler exposes only submit {template,input}, status {job_ref}, and cancel
// {job_ref}. Target application/event/session/child authority never crosses the
// host boundary.
func NewHandler(service *Service, callerApplicationID string) host.Handler {
	return func(ctx context.Context, args map[string]any) (host.Result, error) {
		op, _ := args["op"].(string)
		switch op {
		case "submit":
			if err := rejectUnknownArgs(args, "op", "template", "input"); err != nil {
				return host.Result{}, err
			}
			template, ok := args["template"].(string)
			if !ok || strings.TrimSpace(template) == "" {
				return host.Result{}, fmt.Errorf("host.application_job.submit: template is required")
			}
			input := args["input"]
			if input == nil {
				input = map[string]any{}
			}
			raw, err := json.Marshal(input)
			if err != nil {
				return host.Result{}, fmt.Errorf("host.application_job.submit: input must be JSON: %w", err)
			}
			result, err := service.Submit(ctx, callerApplicationID, template, raw)
			return hostResult(result, err)
		case "status":
			if err := rejectUnknownArgs(args, "op", "job_ref"); err != nil {
				return host.Result{}, err
			}
			jobRef, ok := args["job_ref"].(string)
			if !ok || strings.TrimSpace(jobRef) == "" {
				return host.Result{}, fmt.Errorf("host.application_job.status: job_ref is required")
			}
			result, err := service.Status(ctx, callerApplicationID, jobRef)
			return hostResult(result, err)
		case "cancel":
			if err := rejectUnknownArgs(args, "op", "job_ref"); err != nil {
				return host.Result{}, err
			}
			jobRef, ok := args["job_ref"].(string)
			if !ok || strings.TrimSpace(jobRef) == "" {
				return host.Result{}, fmt.Errorf("host.application_job.cancel: job_ref is required")
			}
			result, err := service.Cancel(ctx, callerApplicationID, jobRef)
			return hostResult(result, err)
		default:
			return host.Result{}, fmt.Errorf("host.application_job: unknown op %q", op)
		}
	}
}

func hostResult(result Result, err error) (host.Result, error) {
	if err != nil {
		return host.Result{}, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.application_job: encode result: %w", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return host.Result{}, fmt.Errorf("host.application_job: decode result: %w", err)
	}
	return host.Result{Data: data}, nil
}

func rejectUnknownArgs(args map[string]any, allowed ...string) error {
	allow := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		allow[key] = true
	}
	for key := range args {
		if !allow[key] {
			return fmt.Errorf("host.application_job: unknown input key %q", key)
		}
	}
	return nil
}
