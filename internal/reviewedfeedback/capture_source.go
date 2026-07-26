package reviewedfeedback

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"time"

	"kitsoki/internal/applicationfeedback"
	"kitsoki/internal/host"
)

const ApplicationFeedbackSourceID = "application-feedback"

// LedgerCaptureSource adapts the existing canonical application-feedback
// ledger written by /api/feedback/local. It adds no ingress format or path:
// the daemon injects the already-resolved JSONLLedger.
type LedgerCaptureSource struct {
	Ledger JSONLLedger
}

func (s LedgerCaptureSource) ListFeedback(
	ctx context.Context,
	request CaptureRequest,
) ([]CapturedFeedback, error) {
	if request.SourceID != ApplicationFeedbackSourceID {
		return nil, fmt.Errorf("application feedback source id is unavailable")
	}
	if request.Limit < 1 || request.Limit > MaxDrainLimit+1 {
		return nil, fmt.Errorf("application feedback source limit is outside its bound")
	}
	return s.Ledger.listCaptured(ctx, request.ApplicationID, request.Limit)
}

func (s LedgerCaptureSource) AcknowledgeFeedback(
	_ context.Context,
	ack CaptureAcknowledgement,
) error {
	if ack.SourceID != ApplicationFeedbackSourceID ||
		ack.ApplicationID == "" || ack.SourceRef == "" ||
		ack.Receipt.Schema != IntakeReceiptSchema ||
		ack.Receipt.ApplicationID != ack.ApplicationID ||
		ack.Receipt.ReportRef != ack.SourceRef {
		return fmt.Errorf("application feedback source rejected a noncanonical receipt")
	}
	return nil
}

func (l JSONLLedger) listCaptured(
	ctx context.Context,
	appID string,
	limit int,
) ([]CapturedFeedback, error) {
	info, err := os.Stat(l.Path)
	if errors.Is(err, os.ErrNotExist) {
		return []CapturedFeedback{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open application feedback source: %w", err)
	}
	if info.Size() > maxLedgerBytes {
		return nil, fmt.Errorf("application feedback source exceeds its byte bound")
	}
	file, err := os.Open(l.Path)
	if err != nil {
		return nil, fmt.Errorf("open application feedback source: %w", err)
	}
	defer file.Close()

	type row struct {
		candidate CapturedFeedback
		at        time.Time
	}
	byRef := make(map[string]row)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxLedgerLine)
	lines := 0
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		lines++
		if lines > maxLedgerRecords {
			return nil, fmt.Errorf("application feedback source exceeds its record bound")
		}
		var report applicationfeedback.Report
		var metadata struct {
			ReceivedAt string `json:"receivedAt"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &report); err != nil {
			return nil, fmt.Errorf("decode application feedback source record %d: %w", lines, err)
		}
		if err := json.Unmarshal(scanner.Bytes(), &metadata); err != nil {
			return nil, fmt.Errorf("decode application feedback source metadata %d: %w", lines, err)
		}
		if report.Schema != applicationfeedback.ReportSchema || !report.Reviewed ||
			report.App != appID || report.Attachment.ApplicationID != appID {
			continue
		}
		clean, err := l.NormalizeCaptured(
			host.FeedbackScope{ApplicationID: appID, Owner: "ledger", Revision: "ledger"},
			report,
		)
		if err != nil {
			return nil, fmt.Errorf("validate application feedback source record %d: %w", lines, err)
		}
		at, err := time.Parse(time.RFC3339, metadata.ReceivedAt)
		if err != nil {
			return nil, fmt.Errorf("application feedback source record %d has no review time", lines)
		}
		candidate := CapturedFeedback{SourceRef: clean.IdempotencyKey, Report: clean}
		if prior, ok := byRef[clean.IdempotencyKey]; ok {
			if !reflect.DeepEqual(prior.candidate, candidate) {
				return nil, fmt.Errorf("application feedback source contains a conflicting duplicate")
			}
			continue
		}
		byRef[clean.IdempotencyKey] = row{candidate: candidate, at: at.UTC()}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan application feedback source: %w", err)
	}
	rows := make([]row, 0, len(byRef))
	for _, item := range byRef {
		rows = append(rows, item)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].at.Equal(rows[j].at) {
			return rows[i].at.Before(rows[j].at)
		}
		return rows[i].candidate.SourceRef < rows[j].candidate.SourceRef
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]CapturedFeedback, len(rows))
	for i := range rows {
		out[i] = rows[i].candidate
	}
	return out, nil
}
