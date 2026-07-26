package reviewedfeedback

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/applicationfeedback"
	"kitsoki/internal/host"
)

func TestJSONLLedgerListsOnlyScopedReviewedCanonicalRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	records := []map[string]any{
		ledgerTestRecord("other", "feedback/other", true, "other", 9, "2026-07-26T03:04:09Z"),
		ledgerTestRecord("source.app", "feedback/unreviewed", false, "draft", 8, "2026-07-26T03:04:08Z"),
		ledgerTestRecord(
			"source.app", "feedback/older", true,
			"Older reviewed note in /Users/private/project", 7, "2026-07-26T03:04:07Z",
		),
		ledgerTestRecord("source.app", "feedback/newer", true, "New reviewed note", 8, "2026-07-26T03:04:08Z"),
	}
	writeLedgerRecords(t, path, records)
	ledger := JSONLLedger{Path: path, Home: "/Users/private"}
	scope := host.FeedbackScope{
		ApplicationID: "source.app", Owner: "private-owner", Revision: "1.2.3",
	}

	reports, err := ledger.ListReviewed(context.Background(), scope, 10)
	if err != nil {
		t.Fatalf("list reviewed: %v", err)
	}
	if len(reports) != 2 || reports[0].Ref != "feedback/newer" ||
		reports[1].Ref != "feedback/older" {
		t.Fatalf("reports = %#v", reports)
	}
	if strings.Contains(reports[1].Summary, "/Users/private") ||
		reports[0].Owner != scope.Owner || reports[0].AppID != scope.ApplicationID {
		t.Fatalf("privacy-scoped reports = %#v", reports)
	}
	resolved, err := ledger.ResolveReviewed(context.Background(), scope, "feedback/newer")
	if err != nil {
		t.Fatalf("resolve reviewed: %v", err)
	}
	if resolved.Frame != 8 || resolved.Projection.Revision != "8" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestJSONLLedgerRejectsConflictingDuplicateAndUnsafeRecords(t *testing.T) {
	tests := []struct {
		name    string
		records []map[string]any
		want    string
	}{
		{
			name: "conflicting duplicate",
			records: []map[string]any{
				ledgerTestRecord("source.app", "feedback/ref", true, "one", 1, "2026-07-26T03:04:05Z"),
				ledgerTestRecord("source.app", "feedback/ref", true, "two", 1, "2026-07-26T03:04:05Z"),
			},
			want: "conflicting duplicate",
		},
		{
			name: "unsafe reference",
			records: []map[string]any{
				ledgerTestRecord("source.app", "../private", true, "note", 1, "2026-07-26T03:04:05Z"),
			},
			want: "privacy-safe",
		},
		{
			name: "unsafe control text",
			records: []map[string]any{
				ledgerTestRecord("source.app", "feedback/ref", true, "note\x00secret", 1, "2026-07-26T03:04:05Z"),
			},
			want: "privacy boundary",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "feedback.jsonl")
			writeLedgerRecords(t, path, test.records)
			_, err := (JSONLLedger{Path: path}).ListReviewed(
				context.Background(),
				host.FeedbackScope{ApplicationID: "source.app", Owner: "owner", Revision: "1"},
				10,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func ledgerTestRecord(
	appID, ref string,
	reviewed bool,
	text string,
	frame uint64,
	receivedAt string,
) map[string]any {
	return map[string]any{
		"schema":         applicationfeedback.ReportSchema,
		"idempotencyKey": ref,
		"app":            appID,
		"producer":       "feedback-intake",
		"kind":           "bug",
		"userText":       text,
		"reviewed":       reviewed,
		"application": map[string]any{
			"schema":         applicationfeedback.AttachmentSchema,
			"application_id": appID,
			"frame_revision": frame,
		},
		"receivedAt": receivedAt,
	}
}

func writeLedgerRecords(t *testing.T, path string, records []map[string]any) {
	t.Helper()
	var lines []byte
	for _, record := range records {
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		lines = append(lines, raw...)
		lines = append(lines, '\n')
	}
	if err := os.WriteFile(path, lines, 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
}
