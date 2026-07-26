// Package applicationmaintenance provides daemon-owned, application-scoped
// reconciliation services for durable sessions, worker observations, and
// campaign outcomes.
package applicationmaintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"kitsoki/internal/app"
	"kitsoki/internal/campaign"
	"kitsoki/internal/jobs"
	"kitsoki/internal/store"
)

const (
	SessionReceiptSchema  = "kitsoki/session-reconciliation-receipt/v1"
	WorkerReceiptSchema   = "kitsoki/worker-fleet-receipt/v1"
	CampaignReceiptSchema = "kitsoki/campaign-supervision-receipt/v1"

	DefaultMaxSessions     = 200
	DefaultMaxSessionJobs  = 1000
	DefaultMaxWorkers      = 200
	DefaultMaxCampaigns    = campaign.DefaultMaxDefinitions
	DefaultMaxRemediations = 50
	DefaultMaxReceiptBytes = 256 * 1024
)

// SessionSource is the application-scoped durable session read surface.
type SessionSource interface {
	ListSessions(context.Context, string, int) ([]store.SessionSummary, error)
}

// SessionJobReconciler interrupts only process-bound jobs whose recorded
// owner is no longer live. Cross-host ownership is reported as deferred.
type SessionJobReconciler interface {
	ReconcileProcessBoundJobs(
		context.Context,
		[]app.SessionID,
		int,
	) (jobs.ProcessBoundReconcileResult, error)
}

type SessionService struct {
	ApplicationID string
	Sessions      SessionSource
	Jobs          SessionJobReconciler
	MaxSessions   int
	MaxJobs       int
}

func (s SessionService) Reconcile(ctx context.Context) (map[string]any, error) {
	if !opaqueIdentity(s.ApplicationID) {
		return nil, fmt.Errorf("session reconciliation: invalid application id")
	}
	if s.Sessions == nil || s.Jobs == nil {
		return nil, fmt.Errorf("session reconciliation: durable services are unavailable")
	}
	if s.MaxSessions < 1 || s.MaxSessions > DefaultMaxSessions {
		return nil, fmt.Errorf("session reconciliation: max sessions must be between 1 and %d", DefaultMaxSessions)
	}
	if s.MaxJobs < 1 || s.MaxJobs > DefaultMaxSessionJobs {
		return nil, fmt.Errorf("session reconciliation: max jobs must be between 1 and %d", DefaultMaxSessionJobs)
	}
	sessions, err := s.Sessions.ListSessions(ctx, s.ApplicationID, s.MaxSessions+1)
	if err != nil {
		return nil, fmt.Errorf("session reconciliation: list application sessions: %w", err)
	}
	if len(sessions) > s.MaxSessions {
		return nil, fmt.Errorf(
			"session reconciliation: selected more than %d sessions; refusing to truncate",
			s.MaxSessions,
		)
	}
	sessionIDs := make([]app.SessionID, 0, len(sessions))
	for _, session := range sessions {
		if session.AppID != s.ApplicationID {
			return nil, fmt.Errorf("session reconciliation: durable source returned a foreign application session")
		}
		sessionIDs = append(sessionIDs, session.ID)
	}
	result, err := s.Jobs.ReconcileProcessBoundJobs(ctx, sessionIDs, s.MaxJobs)
	if err != nil {
		return nil, fmt.Errorf("session reconciliation: reconcile process-bound jobs: %w", err)
	}
	status := "reconciled"
	if result.Deferred > 0 {
		status = "deferred"
	}
	return map[string]any{
		"schema":                      SessionReceiptSchema,
		"operation":                   "session_reconciliation",
		"application_id":              s.ApplicationID,
		"status":                      status,
		"sessions_examined":           len(sessions),
		"process_bound_jobs_examined": result.Examined,
		"interrupted_jobs":            result.Interrupted,
		"deferred_jobs":               result.Deferred,
		"restart_truth":               result.RestartTruth,
	}, nil
}

type WorkerCapabilities struct {
	Placements []string
	Isolation  string
	Networks   []string
}

// WorkerObservation deliberately has no endpoint, tunnel, credential, URL, or
// error-text fields. Sources cannot accidentally project transport authority.
type WorkerObservation struct {
	ID           string
	Placement    string
	Health       string
	Enabled      bool
	Jobs         int
	Capabilities WorkerCapabilities
}

type WorkerSource interface {
	ObserveWorkers(context.Context) ([]WorkerObservation, error)
}

type WorkerService struct {
	ApplicationID string
	Source        WorkerSource
	MaxWorkers    int
	MaxBytes      int
}

func (s WorkerService) Reconcile(ctx context.Context) (map[string]any, error) {
	if !opaqueIdentity(s.ApplicationID) {
		return nil, fmt.Errorf("worker fleet: invalid application id")
	}
	if s.Source == nil {
		return nil, fmt.Errorf("worker fleet: observation service is unavailable")
	}
	if s.MaxWorkers < 1 || s.MaxWorkers > DefaultMaxWorkers {
		return nil, fmt.Errorf("worker fleet: max workers must be between 1 and %d", DefaultMaxWorkers)
	}
	if s.MaxBytes < 1 || s.MaxBytes > DefaultMaxReceiptBytes {
		return nil, fmt.Errorf("worker fleet: max bytes must be between 1 and %d", DefaultMaxReceiptBytes)
	}
	workers, err := s.Source.ObserveWorkers(ctx)
	if err != nil {
		return nil, fmt.Errorf("worker fleet: observe: %w", err)
	}
	if len(workers) > s.MaxWorkers {
		return nil, fmt.Errorf("worker fleet: selected more than %d workers; refusing to truncate", s.MaxWorkers)
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].ID < workers[j].ID })
	seen := make(map[string]bool, len(workers))
	rows := make([]any, 0, len(workers))
	counts := map[string]int{"workers": 0, "enabled": 0, "online": 0, "degraded": 0, "offline": 0, "unknown": 0}
	for _, worker := range workers {
		if !validWorker(worker) || seen[worker.ID] {
			return nil, fmt.Errorf("worker fleet: source returned an invalid or duplicate worker observation")
		}
		seen[worker.ID] = true
		health := worker.Health
		switch health {
		case "online", "degraded", "offline":
		default:
			health = "unknown"
		}
		counts["workers"]++
		counts[health]++
		if worker.Enabled {
			counts["enabled"]++
		}
		rows = append(rows, map[string]any{
			"worker_ref": worker.ID,
			"placement":  worker.Placement,
			"health":     health,
			"enabled":    worker.Enabled,
			"job_count":  worker.Jobs,
			"capabilities": map[string]any{
				"placements": semanticList(worker.Capabilities.Placements),
				"isolation":  worker.Capabilities.Isolation,
				"networks":   semanticList(worker.Capabilities.Networks),
			},
		})
	}
	return boundedReceipt("worker fleet", map[string]any{
		"schema":         WorkerReceiptSchema,
		"operation":      "worker_fleet",
		"application_id": s.ApplicationID,
		"status":         "observed",
		"observe_only":   true,
		"workers":        rows,
		"counts":         stringIntMap(counts),
	}, s.MaxBytes)
}

type CampaignStore interface {
	List(context.Context, string, int) ([]campaign.Schedule, error)
}

type CampaignPolicy struct {
	MaxProposals int
	Statuses     []string
}

type CampaignService struct {
	ApplicationID string
	Store         CampaignStore
	MaxCampaigns  int
	MaxBytes      int
	Policy        CampaignPolicy
}

func (s CampaignService) Reconcile(ctx context.Context) (map[string]any, error) {
	if !opaqueIdentity(s.ApplicationID) {
		return nil, fmt.Errorf("campaign supervision: invalid application id")
	}
	if s.Store == nil {
		return nil, fmt.Errorf("campaign supervision: durable campaign store is unavailable")
	}
	if s.MaxCampaigns < 1 || s.MaxCampaigns > DefaultMaxCampaigns {
		return nil, fmt.Errorf("campaign supervision: max campaigns must be between 1 and %d", DefaultMaxCampaigns)
	}
	if s.MaxBytes < 1 || s.MaxBytes > DefaultMaxReceiptBytes {
		return nil, fmt.Errorf(
			"campaign supervision: max bytes must be between 1 and %d",
			DefaultMaxReceiptBytes,
		)
	}
	if s.Policy.MaxProposals < 1 || s.Policy.MaxProposals > DefaultMaxRemediations {
		return nil, fmt.Errorf(
			"campaign supervision: max remediation proposals must be between 1 and %d",
			DefaultMaxRemediations,
		)
	}
	statuses, err := remediationStatuses(s.Policy.Statuses)
	if err != nil {
		return nil, err
	}
	schedules, err := s.Store.List(ctx, s.ApplicationID, s.MaxCampaigns+1)
	if err != nil {
		return nil, fmt.Errorf("campaign supervision: list durable outcomes: %w", err)
	}
	if len(schedules) > s.MaxCampaigns {
		return nil, fmt.Errorf(
			"campaign supervision: selected more than %d campaigns; refusing to truncate",
			s.MaxCampaigns,
		)
	}
	sort.Slice(schedules, func(i, j int) bool { return schedules[i].ID < schedules[j].ID })
	counts := map[string]int{
		"campaigns": len(schedules), "enabled": 0, "paused": 0, "running": 0,
		"healthy": 0, "attention": 0,
	}
	proposals := make([]any, 0, min(len(schedules), s.Policy.MaxProposals))
	omitted := 0
	for _, schedule := range schedules {
		if schedule.AppID != s.ApplicationID || !opaqueIdentity(schedule.ID) {
			return nil, fmt.Errorf("campaign supervision: store returned a foreign or invalid campaign")
		}
		if schedule.Enabled {
			counts["enabled"]++
		}
		if schedule.Paused {
			counts["paused"]++
		}
		counts["running"] += schedule.Running
		issue, recommendation := campaignIssue(schedule, statuses)
		if issue == "" {
			counts["healthy"]++
			continue
		}
		counts["attention"]++
		if len(proposals) >= s.Policy.MaxProposals {
			omitted++
			continue
		}
		proposals = append(proposals, map[string]any{
			"campaign_ref":   schedule.ID,
			"issue":          issue,
			"recommendation": recommendation,
		})
	}
	status := "healthy"
	if counts["attention"] > 0 {
		status = "attention"
	}
	return boundedReceipt("campaign supervision", map[string]any{
		"schema":         CampaignReceiptSchema,
		"operation":      "campaign_supervision",
		"application_id": s.ApplicationID,
		"status":         status,
		"counts":         stringIntMap(counts),
		"remediation_policy": map[string]any{
			"mode":          "propose",
			"max_proposals": s.Policy.MaxProposals,
			"statuses":      stringListAny(statuses),
		},
		"remediation_proposals": proposals,
		"omitted_proposals":     omitted,
		"proposal_only":         true,
	}, s.MaxBytes)
}

func campaignIssue(schedule campaign.Schedule, statuses []string) (string, string) {
	if schedule.Paused {
		return "paused", "review_definition"
	}
	for _, status := range statuses {
		if schedule.LastStatus == status {
			return "last_" + status, "review_outcome"
		}
	}
	return "", ""
}

func remediationStatuses(values []string) ([]string, error) {
	if len(values) == 0 {
		values = []string{"failed", "interrupted"}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		switch value {
		case "failed", "interrupted":
		default:
			return nil, fmt.Errorf("campaign supervision: remediation status %q is not supported", value)
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out, nil
}

func validWorker(worker WorkerObservation) bool {
	return opaqueIdentity(worker.ID) &&
		semantic(worker.Placement, 64) &&
		semantic(worker.Health, 64) &&
		worker.Jobs >= 0 && worker.Jobs <= 1_000_000 &&
		(worker.Capabilities.Isolation == "" || semantic(worker.Capabilities.Isolation, 128)) &&
		len(semanticList(worker.Capabilities.Placements)) == len(worker.Capabilities.Placements) &&
		len(semanticList(worker.Capabilities.Networks)) == len(worker.Capabilities.Networks)
}

func opaqueIdentity(value string) bool {
	return semantic(value, 128) && !strings.Contains(value, "/")
}

func semantic(value string, max int) bool {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	return value != "" && len(value) <= max &&
		!strings.ContainsAny(value, "/\\\r\n\x00") &&
		!strings.Contains(lower, "://") &&
		!strings.Contains(lower, "%2f") &&
		!strings.Contains(lower, "%5c")
}

func semanticList(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !semantic(value, 128) || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func stringIntMap(values map[string]int) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func stringListAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func boundedReceipt(operation string, receipt map[string]any, maxBytes int) (map[string]any, error) {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return nil, fmt.Errorf("%s: encode receipt size check: %w", operation, err)
	}
	if len(raw) > maxBytes {
		return nil, fmt.Errorf(
			"%s: receipt encodes to %d bytes, exceeding %d; refusing to truncate",
			operation, len(raw), maxBytes,
		)
	}
	return receipt, nil
}
