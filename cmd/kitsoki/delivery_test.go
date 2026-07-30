package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"kitsoki/internal/capsule/delivery"
	"kitsoki/internal/capsule/queue"
)

func TestDeliveryCLIUsesSharedLifecycle(t *testing.T) {
	project := t.TempDir()
	store := queue.Store{ProjectRoot: project}
	candidate, err := store.Submit(queue.Submit{
		Branch: "delivery", SHA: strings.Repeat("d", 40),
		Admission: queue.EmergencySkipTestsAdmission,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		verb       string
		wantPhase  queue.Status
		wantAction delivery.Action
	}{
		{"cancel", queue.NeedsInput, delivery.ActionRepair},
		{"retry", queue.Queued, delivery.ActionProcessing},
		{"reject", queue.Rejected, delivery.ActionTerminal},
	}
	for _, test := range tests {
		cmd := deliveryCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{test.verb, candidate.ID, "--project", project, "--actor", "test"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%s: %v\n%s", test.verb, err, out.String())
		}
		var result delivery.Result
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("%s decode: %v\n%s", test.verb, err, out.String())
		}
		if result.Candidate == nil || result.Candidate.Phase != test.wantPhase || result.Action != test.wantAction {
			t.Fatalf("%s result=%+v", test.verb, result)
		}
	}
}
