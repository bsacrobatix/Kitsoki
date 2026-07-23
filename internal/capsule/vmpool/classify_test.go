package vmpool

import (
	"reflect"
	"testing"
	"time"
)

// TestClassify is the table-driven contract for the shared classification
// helper: every ReconcilePlan bucket (known-active, preserved-protected,
// preserved-expired-reclaimable, lost, orphan), including the
// PreserveFailedTTL boundary, is exercised here once so Pool.Reconcile and
// any plan-only caller (the CLI's vmpoolPlanReconcile) can trust they will
// never see two different answers for the same input.
func TestClassify(t *testing.T) {
	cfg := Config{PreserveFailedTTL: time.Hour}.WithDefaults()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		workers   []Worker
		instances []Instance
		want      ReconcilePlan
	}{
		{
			name: "known active: non-terminal worker with a matching live instance",
			workers: []Worker{
				{ID: "vm-active", InstanceID: "inst-active", Status: StatusReady},
			},
			instances: []Instance{{ID: "inst-active"}},
			want:      ReconcilePlan{Active: []string{"inst-active"}},
		},
		{
			name: "preserved protected: within TTL, reports remaining time",
			workers: []Worker{
				{ID: "vm-protected", InstanceID: "inst-protected", Status: StatusFailed, Preserved: true, TerminalAt: now.Add(-30 * time.Minute)},
			},
			instances: []Instance{{ID: "inst-protected"}},
			want: ReconcilePlan{
				PreservedProtected: []PreservedWorker{{WorkerID: "vm-protected", InstanceID: "inst-protected", TTLRemaining: 30 * time.Minute}},
			},
		},
		{
			name: "preserved expired reclaimable: past TTL",
			workers: []Worker{
				{ID: "vm-expired", InstanceID: "inst-expired", Status: StatusFailed, Preserved: true, TerminalAt: now.Add(-90 * time.Minute)},
			},
			instances: []Instance{{ID: "inst-expired"}},
			want: ReconcilePlan{
				PreservedExpired: []PreservedWorker{{WorkerID: "vm-expired", InstanceID: "inst-expired", TTLRemaining: -30 * time.Minute}},
			},
		},
		{
			name: "TTL boundary: exactly at the TTL is still protected, not expired",
			workers: []Worker{
				{ID: "vm-boundary", InstanceID: "inst-boundary", Status: StatusFailed, Preserved: true, TerminalAt: now.Add(-time.Hour)},
			},
			instances: []Instance{{ID: "inst-boundary"}},
			want: ReconcilePlan{
				PreservedProtected: []PreservedWorker{{WorkerID: "vm-boundary", InstanceID: "inst-boundary", TTLRemaining: 0}},
			},
		},
		{
			name: "TTL boundary: one second past the TTL is expired",
			workers: []Worker{
				{ID: "vm-past-boundary", InstanceID: "inst-past-boundary", Status: StatusFailed, Preserved: true, TerminalAt: now.Add(-time.Hour - time.Second)},
			},
			instances: []Instance{{ID: "inst-past-boundary"}},
			want: ReconcilePlan{
				PreservedExpired: []PreservedWorker{{WorkerID: "vm-past-boundary", InstanceID: "inst-past-boundary", TTLRemaining: -time.Second}},
			},
		},
		{
			name: "lost: tracked non-terminal worker whose instance is missing from the tag sweep",
			workers: []Worker{
				{ID: "vm-lost", InstanceID: "inst-gone", Status: StatusReady},
			},
			instances: nil,
			want:      ReconcilePlan{Lost: []string{"vm-lost"}},
		},
		{
			name:      "orphan: a live tagged instance matching no worker record at all",
			workers:   nil,
			instances: []Instance{{ID: "inst-untracked"}},
			want:      ReconcilePlan{Orphans: []string{"inst-untracked"}},
		},
		{
			name: "a plain terminal, non-preserved worker's leftover instance is an orphan too",
			workers: []Worker{
				{ID: "vm-destroyed", InstanceID: "inst-destroyed", Status: StatusDestroyed},
			},
			instances: []Instance{{ID: "inst-destroyed"}},
			want:      ReconcilePlan{Orphans: []string{"inst-destroyed"}},
		},
		{
			name: "a worker with no InstanceID yet is neither lost nor active",
			workers: []Worker{
				{ID: "vm-creating", Status: StatusCreating},
			},
			instances: nil,
			want:      ReconcilePlan{},
		},
		{
			name: "all five classes present in a single pass, each bucketed exactly once",
			workers: []Worker{
				{ID: "vm-active", InstanceID: "inst-active", Status: StatusRunning},
				{ID: "vm-protected", InstanceID: "inst-protected", Status: StatusFailed, Preserved: true, TerminalAt: now.Add(-10 * time.Minute)},
				{ID: "vm-expired", InstanceID: "inst-expired", Status: StatusFailed, Preserved: true, TerminalAt: now.Add(-2 * time.Hour)},
				{ID: "vm-lost", InstanceID: "inst-lost-gone", Status: StatusProvisioning},
				{ID: "vm-destroyed", InstanceID: "inst-destroyed-gone", Status: StatusDestroyed},
			},
			instances: []Instance{
				{ID: "inst-active"},
				{ID: "inst-protected"},
				{ID: "inst-expired"},
				{ID: "inst-untracked"},
			},
			want: ReconcilePlan{
				Active:             []string{"inst-active"},
				PreservedProtected: []PreservedWorker{{WorkerID: "vm-protected", InstanceID: "inst-protected", TTLRemaining: 50 * time.Minute}},
				PreservedExpired:   []PreservedWorker{{WorkerID: "vm-expired", InstanceID: "inst-expired", TTLRemaining: -time.Hour}},
				Lost:               []string{"vm-lost"},
				Orphans:            []string{"inst-untracked"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(State{Workers: tt.workers}, tt.instances, cfg, now)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Classify() =\n  %#v\nwant\n  %#v", got, tt.want)
			}
		})
	}
}

// TestReportFromPlan checks the ReconcileReport derived from a ReconcilePlan
// preserves every bucket in the legacy report shape (Active/Orphans/Lost as
// before, ExpiredPreserved reduced to worker IDs, PreservedProtected passed
// through as the new additive field).
func TestReportFromPlan(t *testing.T) {
	plan := ReconcilePlan{
		Active:             []string{"inst-active"},
		PreservedProtected: []PreservedWorker{{WorkerID: "vm-protected", InstanceID: "inst-protected", TTLRemaining: 30 * time.Minute}},
		PreservedExpired:   []PreservedWorker{{WorkerID: "vm-expired", InstanceID: "inst-expired", TTLRemaining: -time.Minute}},
		Lost:               []string{"vm-lost"},
		Orphans:            []string{"inst-untracked"},
	}
	got := ReportFromPlan(plan)
	want := ReconcileReport{
		Active:             []string{"inst-active"},
		Orphans:            []string{"inst-untracked"},
		Lost:               []string{"vm-lost"},
		PreservedProtected: []PreservedWorker{{WorkerID: "vm-protected", InstanceID: "inst-protected", TTLRemaining: 30 * time.Minute}},
		ExpiredPreserved:   []string{"vm-expired"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReportFromPlan() =\n  %#v\nwant\n  %#v", got, want)
	}
}
