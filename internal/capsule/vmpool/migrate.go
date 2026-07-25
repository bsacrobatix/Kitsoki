package vmpool

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// MigrationReport is the fail-closed result of joining legacy workspace
// fragments with the live provider inventory into one project authority.
type MigrationReport struct {
	Schema          string   `json:"schema"`
	DestinationRoot string   `json:"destination_root"`
	LegacyRoots     []string `json:"legacy_roots"`
	WorkerCount     int      `json:"worker_count"`
	ActiveCount     int      `json:"active_count"`
	LiveInstances   int      `json:"live_instances"`
	LostWorkers     []string `json:"lost_workers,omitempty"`
	AlreadyApplied  bool     `json:"already_applied,omitempty"`
}

const MigrationReportSchema = "capsule-vmpool-migration/v1"

// MigrateProjectState atomically creates one project-authoritative store from
// legacy workspace-local fragments, but only after an exact join against the
// provider's live tagged instance inventory. It never removes or rewrites a
// legacy fragment and uses Store.Update, preserving the process-owned
// serializer inode and atomic-write contract.
func MigrateProjectState(ctx context.Context, destination Store, legacy []Store, live []Instance) (MigrationReport, error) {
	if len(legacy) == 0 {
		return MigrationReport{}, fmt.Errorf("vmpool migration: at least one legacy store is required")
	}
	if err := ctx.Err(); err != nil {
		return MigrationReport{}, err
	}
	destinationRoot, err := filepath.Abs(destination.ProjectRoot)
	if err != nil {
		return MigrationReport{}, fmt.Errorf("vmpool migration: resolve destination: %w", err)
	}
	report := MigrationReport{
		Schema:          MigrationReportSchema,
		DestinationRoot: destinationRoot,
		LegacyRoots:     make([]string, 0, len(legacy)),
		LiveInstances:   len(live),
	}

	merged := State{Schema: StateSchema, Workers: []Worker{}}
	byID := map[string]Worker{}
	byJob := map[string]Worker{}
	byInstance := map[string]Worker{}
	for _, fragment := range legacy {
		if err := ctx.Err(); err != nil {
			return MigrationReport{}, err
		}
		root, err := filepath.Abs(fragment.ProjectRoot)
		if err != nil {
			return MigrationReport{}, fmt.Errorf("vmpool migration: resolve legacy root: %w", err)
		}
		if root == destinationRoot {
			return MigrationReport{}, fmt.Errorf("vmpool migration: destination cannot also be a legacy fragment: %s", root)
		}
		report.LegacyRoots = append(report.LegacyRoots, root)
		state, err := fragment.Load()
		if err != nil {
			return MigrationReport{}, fmt.Errorf("vmpool migration: load legacy store %s: %w", root, err)
		}
		if state.Schema != StateSchema {
			return MigrationReport{}, fmt.Errorf("vmpool migration: legacy store %s has schema %q, want %q", root, state.Schema, StateSchema)
		}
		for _, worker := range state.Workers {
			if worker.ID == "" || worker.JobID == "" {
				return MigrationReport{}, fmt.Errorf("vmpool migration: legacy store %s contains a worker with incomplete identity", root)
			}
			if prior, ok := byID[worker.ID]; ok {
				if prior != worker {
					return MigrationReport{}, fmt.Errorf("vmpool migration: contradictory worker id %q across legacy stores", worker.ID)
				}
				continue
			}
			if prior, ok := byJob[worker.JobID]; ok && prior.ID != worker.ID {
				return MigrationReport{}, fmt.Errorf("vmpool migration: contradictory job id %q maps to workers %q and %q", worker.JobID, prior.ID, worker.ID)
			}
			if worker.InstanceID != "" {
				if prior, ok := byInstance[worker.InstanceID]; ok && prior.ID != worker.ID {
					return MigrationReport{}, fmt.Errorf("vmpool migration: contradictory instance id %q maps to workers %q and %q", worker.InstanceID, prior.ID, worker.ID)
				}
				byInstance[worker.InstanceID] = worker
			}
			byID[worker.ID] = worker
			byJob[worker.JobID] = worker
			merged.Workers = append(merged.Workers, worker)
		}
	}
	sort.Strings(report.LegacyRoots)
	sort.Slice(merged.Workers, func(i, j int) bool {
		if merged.Workers[i].ID != merged.Workers[j].ID {
			return merged.Workers[i].ID < merged.Workers[j].ID
		}
		return merged.Workers[i].JobID < merged.Workers[j].JobID
	})

	liveByID := map[string]Instance{}
	for _, instance := range live {
		if instance.ID == "" {
			return MigrationReport{}, fmt.Errorf("vmpool migration: provider returned a live instance with no id")
		}
		if _, exists := liveByID[instance.ID]; exists {
			return MigrationReport{}, fmt.Errorf("vmpool migration: provider returned duplicate live instance %q", instance.ID)
		}
		liveByID[instance.ID] = instance
		worker, ok := byInstance[instance.ID]
		if !ok {
			return MigrationReport{}, fmt.Errorf("vmpool migration: live tagged instance %q is absent from every legacy store", instance.ID)
		}
		if worker.Status.Terminal() && !worker.Preserved {
			return MigrationReport{}, fmt.Errorf("vmpool migration: live tagged instance %q belongs to terminal non-preserved worker %q", instance.ID, worker.ID)
		}
	}
	for i := range merged.Workers {
		worker := &merged.Workers[i]
		if worker.Status.Terminal() && !worker.Preserved {
			continue
		}
		if worker.InstanceID == "" {
			return MigrationReport{}, fmt.Errorf("vmpool migration: active worker %q has no provider instance id", worker.ID)
		}
		if _, ok := liveByID[worker.InstanceID]; !ok {
			report.LostWorkers = append(report.LostWorkers, worker.ID)
			worker.Status = StatusFailed
			worker.Preserved = false
			worker.TerminalAt = migrationTerminalAt(*worker)
			worker.Error = fmt.Sprintf("vmpool migration reconcile: provider instance %s is absent from the authoritative live tagged inventory", worker.InstanceID)
		}
	}
	sort.Strings(report.LostWorkers)

	report.WorkerCount = len(merged.Workers)
	report.ActiveCount = merged.ActiveCount()
	err = destination.Update(func(current *State) error {
		if current.Schema != StateSchema {
			return fmt.Errorf("vmpool migration: destination has schema %q, want %q", current.Schema, StateSchema)
		}
		if len(current.Workers) > 0 {
			if statesEqual(*current, merged) {
				report.AlreadyApplied = true
				return nil
			}
			return fmt.Errorf("vmpool migration: destination is not empty and does not exactly match the proposed merged state")
		}
		*current = merged
		return nil
	})
	if err != nil {
		return MigrationReport{}, err
	}
	return report, nil
}

func migrationTerminalAt(worker Worker) time.Time {
	terminal := worker.CreatedAt
	for _, candidate := range []time.Time{worker.ReadyAt, worker.LastActivityAt, worker.TerminalAt} {
		if candidate.After(terminal) {
			terminal = candidate
		}
	}
	if terminal.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return terminal.UTC()
}

func statesEqual(left, right State) bool {
	if left.Schema != right.Schema || len(left.Workers) != len(right.Workers) {
		return false
	}
	for i := range left.Workers {
		if left.Workers[i] != right.Workers[i] {
			return false
		}
	}
	return true
}
