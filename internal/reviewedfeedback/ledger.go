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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"kitsoki/internal/applicationfeedback"
	"kitsoki/internal/host"
	"kitsoki/internal/runstatus/harscrub"
)

const (
	maxLedgerBytes   = 64 << 20
	maxLedgerRecords = 10000
	maxLedgerLine    = 1 << 20
	maxSummaryBytes  = 64 << 10
	maxTitleBytes    = 256
)

type JSONLLedger struct {
	Path string
	Home string
}

type ledgerRecord struct {
	Schema         string `json:"schema"`
	IdempotencyKey string `json:"idempotencyKey"`
	App            string `json:"app"`
	Producer       string `json:"producer"`
	Kind           string `json:"kind"`
	UserText       string `json:"userText"`
	Reviewed       bool   `json:"reviewed"`
	Application    struct {
		Schema        string `json:"schema"`
		ApplicationID string `json:"application_id"`
		FrameRevision uint64 `json:"frame_revision"`
	} `json:"application"`
	ReceivedAt string `json:"receivedAt"`
}

func (l JSONLLedger) ListReviewed(
	_ context.Context,
	scope host.FeedbackScope,
	limit int,
) ([]host.ReviewedFeedbackReport, error) {
	if limit < 1 || limit > 201 {
		return nil, fmt.Errorf("reviewed feedback ledger limit is outside 1..201")
	}
	reports, err := l.scan(scope)
	if err != nil {
		return nil, err
	}
	if len(reports) > limit {
		reports = reports[:limit]
	}
	out := make([]host.ReviewedFeedbackReport, len(reports))
	for i := range reports {
		out[i] = reports[i].Projection
	}
	return out, nil
}

func (l JSONLLedger) ResolveReviewed(
	_ context.Context,
	scope host.FeedbackScope,
	ref string,
) (ResolvedReport, error) {
	reports, err := l.scan(scope)
	if err != nil {
		return ResolvedReport{}, err
	}
	for _, report := range reports {
		if report.Projection.Ref == ref {
			return report, nil
		}
	}
	return ResolvedReport{}, fmt.Errorf("reviewed feedback report was not found in the resolved application scope")
}

func (l JSONLLedger) scan(scope host.FeedbackScope) ([]ResolvedReport, error) {
	info, err := os.Stat(l.Path)
	if errors.Is(err, os.ErrNotExist) {
		return []ResolvedReport{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open reviewed feedback ledger: %w", err)
	}
	if info.Size() > maxLedgerBytes {
		return nil, fmt.Errorf("reviewed feedback ledger exceeds %d bytes; refusing to truncate", maxLedgerBytes)
	}
	file, err := os.Open(l.Path)
	if err != nil {
		return nil, fmt.Errorf("open reviewed feedback ledger: %w", err)
	}
	defer file.Close()

	byRef := map[string]ResolvedReport{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxLedgerLine)
	lines := 0
	for scanner.Scan() {
		lines++
		if lines > maxLedgerRecords {
			return nil, fmt.Errorf("reviewed feedback ledger exceeds %d records; refusing to truncate", maxLedgerRecords)
		}
		var record ledgerRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode reviewed feedback ledger record %d: %w", lines, err)
		}
		if record.Schema != applicationfeedback.ReportSchema || !record.Reviewed {
			continue
		}
		if record.App != scope.ApplicationID ||
			record.Application.Schema != applicationfeedback.AttachmentSchema ||
			record.Application.ApplicationID != scope.ApplicationID {
			continue
		}
		report, err := l.project(scope, record)
		if err != nil {
			return nil, fmt.Errorf("validate reviewed feedback ledger record %d: %w", lines, err)
		}
		if prior, exists := byRef[report.Projection.Ref]; exists {
			if !reflect.DeepEqual(prior, report) {
				return nil, fmt.Errorf("reviewed feedback ledger contains conflicting duplicate report")
			}
			continue
		}
		byRef[report.Projection.Ref] = report
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan reviewed feedback ledger: %w", err)
	}
	out := make([]ResolvedReport, 0, len(byRef))
	for _, report := range byRef {
		out = append(out, report)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Projection.ReviewedAt.Equal(out[j].Projection.ReviewedAt) {
			return out[i].Projection.ReviewedAt.After(out[j].Projection.ReviewedAt)
		}
		return out[i].Projection.Ref < out[j].Projection.Ref
	})
	return out, nil
}

func (l JSONLLedger) project(
	scope host.FeedbackScope,
	record ledgerRecord,
) (ResolvedReport, error) {
	if !safeReference(record.IdempotencyKey, 180) {
		return ResolvedReport{}, fmt.Errorf("report reference is not privacy-safe")
	}
	if !safeToken(record.Kind, 64) || !safeToken(record.Producer, 128) {
		return ResolvedReport{}, fmt.Errorf("report classification is not privacy-safe")
	}
	if record.Application.FrameRevision == 0 {
		return ResolvedReport{}, fmt.Errorf("report has no canonical frame revision")
	}
	reviewedAt, err := time.Parse(time.RFC3339, record.ReceivedAt)
	if err != nil {
		return ResolvedReport{}, fmt.Errorf("report has no canonical review time")
	}
	summary := harscrub.ScrubString(strings.TrimSpace(record.UserText), harscrub.ScrubOptions{
		Home: l.Home, SecretPatterns: harscrub.DefaultSecretPatterns(),
	})
	if summary == "" || len(summary) > maxSummaryBytes || !safeText(summary) {
		return ResolvedReport{}, fmt.Errorf("report summary is empty or exceeds its privacy boundary")
	}
	title := strings.TrimSpace(strings.SplitN(summary, "\n", 2)[0])
	title = boundedText(title, maxTitleBytes)
	return ResolvedReport{
		Projection: host.ReviewedFeedbackReport{
			Ref: record.IdempotencyKey, Kind: record.Kind, Title: title,
			Summary: summary, Producer: record.Producer, Reviewed: true,
			AppID: scope.ApplicationID, Owner: scope.Owner,
			Revision:   strconv.FormatUint(record.Application.FrameRevision, 10),
			ReceiptRef: record.IdempotencyKey, ReviewedAt: reviewedAt.UTC(),
		},
		Frame: record.Application.FrameRevision,
	}, nil
}

func safeReference(value string, max int) bool {
	if value == "" || len(value) > max || strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || !safeToken(segment, max) {
			return false
		}
	}
	return true
}

func safeToken(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == '+', r == ':', r == '@':
		default:
			return false
		}
	}
	return true
}

func safeText(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func boundedText(value string, max int) string {
	if len(value) <= max {
		return value
	}
	for len(value) > max {
		_, size := utf8.DecodeLastRuneInString(value)
		if size == 0 {
			break
		}
		value = value[:len(value)-size]
	}
	return value
}
