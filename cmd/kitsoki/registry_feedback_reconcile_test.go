package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kitsoki/internal/applicationfeedback"
	"kitsoki/internal/host"
	"kitsoki/internal/reviewedfeedback"
	"kitsoki/internal/webconfig"
)

func TestRegistryWiresCanonicalApplicationFeedbackIntakeWithoutDuplicateLedgerLine(t *testing.T) {
	storiesDir, _ := writeStory(t, "source", []byte(minimalStory))
	cfg := webconfig.WebConfig{
		FeedbackIntake: map[string]webconfig.FeedbackIntakeBinding{
			"mini-story": {
				Source:     reviewedfeedback.ApplicationFeedbackSourceID,
				MaxRecords: 5,
			},
		},
	}
	registry := NewRegistry(cfg, []string{storiesDir}, runtimeBase{})
	t.Cleanup(registry.Close)
	if err := registry.EnableDaemon(filepath.Join(t.TempDir(), "daemon.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Rescan(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := registry.ConfigureFeedbackBackends(root); err != nil {
		t.Fatal(err)
	}

	ledger := reviewedfeedback.JSONLLedger{
		Path: filepath.Join(root, ".artifacts", "feedback", "feedback.jsonl"),
	}
	scope := host.FeedbackScope{ApplicationID: "mini-story", Revision: "0.1.0"}
	report := applicationfeedback.Report{
		Schema:         applicationfeedback.ReportSchema,
		IdempotencyKey: "feedback/api-record", App: "mini-story",
		Producer: "kitsoki.application", Kind: "bug",
		UserText: "Reviewed API feedback", Reviewed: true,
		Attachment: applicationfeedback.Attachment{
			Schema:        applicationfeedback.AttachmentSchema,
			ApplicationID: "mini-story", FrameRevision: 9,
		},
	}
	if _, _, err := ledger.AppendReviewed(
		context.Background(), scope, report,
		time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC),
	); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(ledger.Path)
	if err != nil {
		t.Fatal(err)
	}

	hostRegistry := host.NewRegistry()
	host.RegisterBuiltins(hostRegistry)
	registry.wireFeedbackReconciliation(
		&sessionRuntime{HostRegistry: hostRegistry}, "mini-story", "", "0.1.0",
	)
	result, err := hostRegistry.Invoke(
		context.Background(), host.FeedbackIntakeReconcileVerb, nil,
	)
	if err != nil || result.Error != "" || result.Data["status"] != "persisted" {
		t.Fatalf("intake result = %#v, %v", result, err)
	}
	after, err := os.ReadFile(ledger.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("canonical source ledger changed during intake")
	}
	replay, err := hostRegistry.Invoke(
		context.Background(), host.FeedbackIntakeReconcileVerb, nil,
	)
	if err != nil || replay.Error != "" || replay.Data["replayed"] != true {
		t.Fatalf("durable replay = %#v, %v", replay, err)
	}
}
