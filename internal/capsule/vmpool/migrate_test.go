package vmpool

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMigrateProjectStateExactJoinAndIdempotency(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	first := Store{ProjectRoot: t.TempDir()}
	second := Store{ProjectRoot: t.TempDir()}
	destination := Store{ProjectRoot: t.TempDir()}
	worker1 := Worker{ID: "worker-1", JobID: "job-1", InstanceID: "instance-1", InstanceName: "pool-1", Status: StatusRunning, Image: "img", CreatedAt: now}
	worker2 := Worker{ID: "worker-2", JobID: "job-2", InstanceID: "instance-2", InstanceName: "pool-2", Status: StatusReady, Image: "img", CreatedAt: now}
	terminal := Worker{ID: "worker-old", JobID: "job-old", InstanceID: "instance-old", InstanceName: "pool-old", Status: StatusDestroyed, Image: "img", CreatedAt: now, TerminalAt: now}
	if err := first.Update(func(state *State) error {
		state.Workers = append(state.Workers, worker1, terminal)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := second.Update(func(state *State) error {
		state.Workers = append(state.Workers, worker2, worker1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	live := []Instance{{ID: "instance-1"}, {ID: "instance-2"}}

	report, err := MigrateProjectState(context.Background(), destination, []Store{first, second}, live)
	if err != nil {
		t.Fatal(err)
	}
	if report.WorkerCount != 3 || report.ActiveCount != 2 || report.LiveInstances != 2 || report.AlreadyApplied {
		t.Fatalf("report = %+v", report)
	}
	state, err := destination.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Workers) != 3 || state.Workers[0].ID != "worker-1" || state.Workers[1].ID != "worker-2" || state.Workers[2].ID != "worker-old" {
		t.Fatalf("merged state = %+v", state)
	}

	report, err = MigrateProjectState(context.Background(), destination, []Store{first, second}, live)
	if err != nil {
		t.Fatal(err)
	}
	if !report.AlreadyApplied {
		t.Fatalf("repeat migration was not idempotent: %+v", report)
	}
}

func TestMigrateProjectStateFailsClosedOnContradictionOrJoinGap(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		first     Worker
		second    *Worker
		live      []Instance
		wantError string
	}{
		{
			name:      "live instance absent from fragments",
			first:     Worker{ID: "worker-1", JobID: "job-1", InstanceID: "instance-1", Status: StatusRunning, CreatedAt: now},
			live:      []Instance{{ID: "instance-1"}, {ID: "untracked"}},
			wantError: "absent from every legacy store",
		},
		{
			name:  "same worker identity divergent record",
			first: Worker{ID: "worker-1", JobID: "job-1", InstanceID: "instance-1", Status: StatusRunning, CreatedAt: now},
			second: &Worker{
				ID: "worker-1", JobID: "job-1", InstanceID: "instance-1", Status: StatusReady, CreatedAt: now,
			},
			live:      []Instance{{ID: "instance-1"}},
			wantError: "contradictory worker id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := Store{ProjectRoot: t.TempDir()}
			if err := first.Update(func(state *State) error {
				state.Workers = append(state.Workers, tt.first)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			fragments := []Store{first}
			if tt.second != nil {
				second := Store{ProjectRoot: t.TempDir()}
				if err := second.Update(func(state *State) error {
					state.Workers = append(state.Workers, *tt.second)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				fragments = append(fragments, second)
			}
			destination := Store{ProjectRoot: t.TempDir()}
			_, err := MigrateProjectState(context.Background(), destination, fragments, tt.live)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("err = %v, want %q", err, tt.wantError)
			}
			state, loadErr := destination.Load()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if len(state.Workers) != 0 {
				t.Fatalf("failed migration mutated destination: %+v", state)
			}
		})
	}
}

func TestMigrateProjectStateReconcilesTrackedMissingWorkersToTerminal(t *testing.T) {
	legacy := Store{ProjectRoot: t.TempDir()}
	destination := Store{ProjectRoot: t.TempDir()}
	worker := Worker{
		ID: "worker-stale", JobID: "job-stale", InstanceID: "instance-gone",
		Status: StatusRunning, CreatedAt: time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC),
	}
	if err := legacy.Update(func(state *State) error {
		state.Workers = append(state.Workers, worker)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	report, err := MigrateProjectState(context.Background(), destination, []Store{legacy}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.ActiveCount != 0 || report.LiveInstances != 0 || len(report.LostWorkers) != 1 || report.LostWorkers[0] != worker.ID {
		t.Fatalf("report = %+v", report)
	}
	state, err := destination.Load()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := state.WorkerByID(worker.ID)
	if !ok || got.Status != StatusFailed || got.TerminalAt.IsZero() || !strings.Contains(got.Error, "authoritative live tagged inventory") {
		t.Fatalf("reconciled worker = %+v", got)
	}
	report, err = MigrateProjectState(context.Background(), destination, []Store{legacy}, nil)
	if err != nil || !report.AlreadyApplied {
		t.Fatalf("repeat reconcile report=%+v err=%v", report, err)
	}
}
