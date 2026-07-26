package host

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"kitsoki/internal/artifactjob"
)

const (
	runstatusSnapshotSchema   = "kitsoki/runstatus-snapshot/v1"
	runstatusSnapshotMaxJobs  = 200
	runstatusSnapshotMaxBytes = 256 * 1024
)

// RunstatusSnapshotStore is the read-only part of the durable artifact-job
// repository needed by host.runstatus.snapshot.
type RunstatusSnapshotStore interface {
	List(context.Context, artifactjob.ListFilter) ([]artifactjob.Job, error)
}

// NewRunstatusSnapshotHandler returns an app-scoped projection over the
// daemon's durable artifact-job store. The scope is fixed at construction so a
// story cannot use host arguments to inspect another application's jobs.
func NewRunstatusSnapshotHandler(store RunstatusSnapshotStore, appID string) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		if store == nil || appID == "" {
			return Result{Error: "host.runstatus.snapshot: durable runstatus is unavailable outside daemon mode"}, nil
		}
		if op, _ := args["op"].(string); op != "" && op != "snapshot" {
			return Result{}, fmt.Errorf("host.runstatus: unknown op %q", op)
		}

		maxJobs, err := requiredRunstatusBound(args, "max_jobs", runstatusSnapshotMaxJobs)
		if err != nil {
			return Result{}, err
		}
		maxBytes, err := requiredRunstatusBound(args, "max_bytes", runstatusSnapshotMaxBytes)
		if err != nil {
			return Result{}, err
		}

		jobs, err := store.List(ctx, artifactjob.ListFilter{
			AppID: appID,
			Limit: maxJobs + 1,
		})
		if err != nil {
			return Result{}, fmt.Errorf("host.runstatus.snapshot: list durable jobs: %w", err)
		}
		if len(jobs) > maxJobs {
			return Result{}, fmt.Errorf(
				"host.runstatus.snapshot: selected more than %d jobs; refusing to truncate",
				maxJobs,
			)
		}

		snapshot, err := buildRunstatusSnapshot(appID, jobs)
		if err != nil {
			return Result{}, err
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return Result{}, fmt.Errorf("host.runstatus.snapshot: encode size check: %w", err)
		}
		if len(encoded) > maxBytes {
			return Result{}, fmt.Errorf(
				"host.runstatus.snapshot: encoded snapshot is %d bytes, exceeds max_bytes %d; refusing to truncate",
				len(encoded), maxBytes,
			)
		}
		return Result{Data: map[string]any{"snapshot": snapshot}}, nil
	}
}

// RunstatusSnapshotHandler is the builtin sentinel. Daemon-backed application
// runtimes replace it with NewRunstatusSnapshotHandler at construction.
var RunstatusSnapshotHandler = NewRunstatusSnapshotHandler(nil, "")

func requiredRunstatusBound(args map[string]any, name string, ceiling int) (int, error) {
	raw, ok := args[name]
	if !ok {
		return 0, fmt.Errorf("host.runstatus.snapshot: missing required arg %q", name)
	}
	var value int
	switch v := raw.(type) {
	case int:
		value = v
	case int64:
		if int64(int(v)) != v {
			return 0, fmt.Errorf("host.runstatus.snapshot: %q is outside the supported integer range", name)
		}
		value = int(v)
	case float64:
		if math.Trunc(v) != v || v > float64(math.MaxInt) || v < float64(math.MinInt) {
			return 0, fmt.Errorf("host.runstatus.snapshot: %q must be an integer", name)
		}
		value = int(v)
	default:
		return 0, fmt.Errorf("host.runstatus.snapshot: %q must be an integer, got %T", name, raw)
	}
	if value < 1 || value > ceiling {
		return 0, fmt.Errorf(
			"host.runstatus.snapshot: %q must be between 1 and %d, got %d",
			name, ceiling, value,
		)
	}
	return value, nil
}

type runstatusSessionAggregate struct {
	jobCount       int
	attentionCount int
	workspaceCount int
	latest         artifactjob.Job
	hasLatest      bool
}

func buildRunstatusSnapshot(appID string, jobs []artifactjob.Job) (map[string]any, error) {
	jobs = append([]artifactjob.Job(nil), jobs...)
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })

	jobRows := make([]any, 0, len(jobs))
	workspaceRows := make([]any, 0)
	attentionRows := make([]any, 0)
	sessions := make(map[string]*runstatusSessionAggregate)
	statusCounts := make(map[string]any)

	for _, job := range jobs {
		if !validRunstatusJobStatus(job.Status) {
			return nil, fmt.Errorf(
				"host.runstatus.snapshot: job %q has unsupported status %q",
				job.ID, job.Status,
			)
		}

		status := string(job.Status)
		statusCounts[status] = runstatusCount(statusCounts[status]) + 1
		jobRow := map[string]any{
			"job_ref":            string(job.ID),
			"status":             status,
			"updated_at":         snapshotTimestamp(job.UpdatedAt),
			"workspace_attached": job.WorkspaceInstanceID != "",
		}
		sessionRef := string(job.SessionID)
		if sessionRef != "" {
			jobRow["session_ref"] = sessionRef
		}
		jobRows = append(jobRows, jobRow)

		attentionKind := runstatusAttentionKind(job.Status)
		if attentionKind != "" {
			ref := map[string]any{
				"kind":    attentionKind,
				"job_ref": string(job.ID),
				"status":  status,
			}
			if sessionRef != "" {
				ref["session_ref"] = sessionRef
			}
			attentionRows = append(attentionRows, ref)
		}

		if job.WorkspaceInstanceID != "" {
			row := map[string]any{
				"workspace_ref": string(job.WorkspaceInstanceID),
				"job_ref":       string(job.ID),
				"job_status":    status,
				"updated_at":    snapshotTimestamp(job.UpdatedAt),
			}
			if sessionRef != "" {
				row["session_ref"] = sessionRef
			}
			workspaceRows = append(workspaceRows, row)
		}

		if sessionRef == "" {
			continue
		}
		aggregate := sessions[sessionRef]
		if aggregate == nil {
			aggregate = &runstatusSessionAggregate{}
			sessions[sessionRef] = aggregate
		}
		aggregate.jobCount++
		if attentionKind != "" {
			aggregate.attentionCount++
		}
		if job.WorkspaceInstanceID != "" {
			aggregate.workspaceCount++
		}
		if !aggregate.hasLatest ||
			job.UpdatedAt.After(aggregate.latest.UpdatedAt) ||
			(job.UpdatedAt.Equal(aggregate.latest.UpdatedAt) && job.ID < aggregate.latest.ID) {
			aggregate.latest = job
			aggregate.hasLatest = true
		}
	}

	sessionRefs := make([]string, 0, len(sessions))
	for ref := range sessions {
		sessionRefs = append(sessionRefs, ref)
	}
	sort.Strings(sessionRefs)
	sessionRows := make([]any, 0, len(sessionRefs))
	for _, ref := range sessionRefs {
		aggregate := sessions[ref]
		sessionRows = append(sessionRows, map[string]any{
			"session_ref":     ref,
			"job_count":       aggregate.jobCount,
			"latest_job_ref":  string(aggregate.latest.ID),
			"latest_status":   string(aggregate.latest.Status),
			"updated_at":      snapshotTimestamp(aggregate.latest.UpdatedAt),
			"attention_count": aggregate.attentionCount,
			"workspace_count": aggregate.workspaceCount,
		})
	}

	return map[string]any{
		"schema":     runstatusSnapshotSchema,
		"app_id":     appID,
		"jobs":       jobRows,
		"sessions":   sessionRows,
		"workspaces": workspaceRows,
		"attention": map[string]any{
			"count": len(attentionRows),
			"refs":  attentionRows,
		},
		"counts": map[string]any{
			"jobs":       len(jobRows),
			"sessions":   len(sessionRows),
			"workspaces": len(workspaceRows),
			"by_status":  statusCounts,
		},
	}, nil
}

func runstatusCount(value any) int {
	count, _ := value.(int)
	return count
}

func validRunstatusJobStatus(status artifactjob.Status) bool {
	switch status {
	case artifactjob.StatusRunning,
		artifactjob.StatusAwaitingInput,
		artifactjob.StatusInterrupted,
		artifactjob.StatusDone,
		artifactjob.StatusFailed,
		artifactjob.StatusCancelled:
		return true
	default:
		return false
	}
}

func runstatusAttentionKind(status artifactjob.Status) string {
	switch status {
	case artifactjob.StatusAwaitingInput:
		return "operator_input_required"
	case artifactjob.StatusInterrupted:
		return "interrupted"
	case artifactjob.StatusFailed:
		return "failed"
	default:
		return ""
	}
}

func snapshotTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
