package host

import (
	"context"
	"fmt"
	"sort"

	"kitsoki/internal/materializationstatus"
)

const (
	materializationSnapshotMaxJobs  = 200
	materializationSnapshotMaxBytes = 256 * 1024
)

func NewMaterializationSnapshotHandler(store materializationstatus.Store, applicationID string) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		const handler = "host.materialization.snapshot"
		if store == nil || !materializationstatus.Opaque(applicationID) {
			return Result{Error: handler + ": durable materialization projection is unavailable outside configured daemon scope"}, nil
		}
		if op, _ := args["op"].(string); op != "" && op != "snapshot" {
			return Result{}, fmt.Errorf("host.materialization: unknown op %q", op)
		}
		if err := rejectSnapshotArgs(args, handler, "application_id", "max_jobs", "max_bytes"); err != nil {
			return Result{}, err
		}
		requestedApp, _ := args["application_id"].(string)
		if requestedApp != applicationID {
			return Result{}, fmt.Errorf("%s: application_id %q does not match server-bound application", handler, requestedApp)
		}
		maxJobs, err := requiredSnapshotBound(args, handler, "max_jobs", materializationSnapshotMaxJobs)
		if err != nil {
			return Result{}, err
		}
		maxBytes, err := requiredSnapshotBound(args, handler, "max_bytes", materializationSnapshotMaxBytes)
		if err != nil {
			return Result{}, err
		}
		records, err := store.List(ctx, applicationID, maxJobs+1)
		if err != nil {
			return Result{}, fmt.Errorf("%s: list durable jobs: %w", handler, err)
		}
		if len(records) > maxJobs {
			return Result{}, fmt.Errorf("%s: selected more than %d jobs; refusing to truncate", handler, maxJobs)
		}
		sort.Slice(records, func(i, j int) bool {
			if records[i].UpdatedAt.Equal(records[j].UpdatedAt) {
				return records[i].JobID < records[j].JobID
			}
			return records[i].UpdatedAt.After(records[j].UpdatedAt)
		})
		seen := map[string]bool{}
		rows := make([]any, 0, len(records))
		invalid := map[string]int{}
		for _, record := range records {
			normalized, err := materializationstatus.Normalize(record)
			if err != nil {
				invalid["invalid_schema"]++
				continue
			}
			if normalized.ApplicationID != applicationID {
				invalid["scope_mismatch"]++
				continue
			}
			if seen[normalized.JobID] {
				invalid["duplicate_id"]++
				continue
			}
			seen[normalized.JobID] = true
			stages := make([]any, len(normalized.Stages))
			for i, stage := range normalized.Stages {
				stages[i] = map[string]any{"id": stage.ID, "title": stage.Title, "status": stage.Status}
			}
			artifacts := make([]any, len(normalized.Artifacts))
			for i, artifact := range normalized.Artifacts {
				artifacts[i] = map[string]any{"kind": artifact.Kind, "title": artifact.Title, "handle": artifact.Handle}
			}
			row := map[string]any{
				"job_id": normalized.JobID, "status": normalized.Status,
				"stages": stages, "artifacts": artifacts,
				"receipt_ids": append([]string(nil), normalized.ReceiptIDs...),
			}
			if normalized.SessionID != "" {
				row["session_id"] = normalized.SessionID
			}
			rows = append(rows, row)
		}
		snapshot := map[string]any{
			"schema": materializationstatus.Schema, "application_id": applicationID,
			"jobs": rows, "invalid_count": invalidTotal(invalid),
			"invalid_by_reason": stringIntMap(invalid),
		}
		return boundedSnapshotResult(handler, snapshot, maxBytes)
	}
}

var MaterializationSnapshotHandler = NewMaterializationSnapshotHandler(nil, "")
