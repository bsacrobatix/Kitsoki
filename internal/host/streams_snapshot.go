package host

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"kitsoki/internal/materializationstatus"
)

const (
	streamSnapshotSchema   = "kitsoki/stream-snapshot/v1"
	streamSnapshotMaxItems = 200
	streamSnapshotMaxBytes = 256 * 1024
)

var immutableHeadPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64}|sha256:[a-f0-9]{64})$`)

type DeliveryStreamRecord struct {
	ID            string
	Lane          string
	State         string
	ImmutableHead string
	TargetRef     string
	ProposalState string
	GateStatus    string
	NextAction    string
	ReceiptRef    string
}

type DeliveryStreamSource interface {
	ListDeliveryStreams(context.Context, int) ([]DeliveryStreamRecord, error)
}

func NewStreamsSnapshotHandler(source DeliveryStreamSource, applicationID, scope string) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		const handler = "host.streams.snapshot"
		if source == nil || !materializationstatus.Opaque(applicationID) || !materializationstatus.Opaque(scope) {
			return Result{Error: handler + ": delivery stream projection is unavailable outside configured daemon scope"}, nil
		}
		if op, _ := args["op"].(string); op != "" && op != "snapshot" {
			return Result{}, fmt.Errorf("host.streams: unknown op %q", op)
		}
		if err := rejectSnapshotArgs(args, handler, "scope", "max_streams", "max_bytes"); err != nil {
			return Result{}, err
		}
		requestedScope, _ := args["scope"].(string)
		if requestedScope != scope {
			return Result{}, fmt.Errorf("%s: scope %q does not match server-bound scope", handler, requestedScope)
		}
		maxItems, err := requiredSnapshotBound(args, handler, "max_streams", streamSnapshotMaxItems)
		if err != nil {
			return Result{}, err
		}
		maxBytes, err := requiredSnapshotBound(args, handler, "max_bytes", streamSnapshotMaxBytes)
		if err != nil {
			return Result{}, err
		}
		records, err := source.ListDeliveryStreams(ctx, maxItems+1)
		if err != nil {
			return Result{}, fmt.Errorf("%s: list durable streams: %w", handler, err)
		}
		if len(records) > maxItems {
			return Result{}, fmt.Errorf("%s: selected more than %d streams; refusing to truncate", handler, maxItems)
		}
		sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
		seen := map[string]bool{}
		rows := make([]any, 0, len(records))
		invalid := map[string]int{}
		for _, record := range records {
			record = normalizeDeliveryStream(record)
			reason := validateDeliveryStream(record, seen)
			if reason != "" {
				invalid[reason]++
				continue
			}
			seen[record.ID] = true
			rows = append(rows, map[string]any{
				"id": record.ID, "lane": record.Lane, "state": record.State,
				"immutable_head": record.ImmutableHead, "target_ref": record.TargetRef,
				"proposal_state": record.ProposalState, "gate_status": record.GateStatus,
				"next_action": record.NextAction, "receipt_ref": record.ReceiptRef,
			})
		}
		snapshot := map[string]any{
			"schema": streamSnapshotSchema, "application_id": applicationID,
			"scope": scope, "streams": rows, "invalid_count": invalidTotal(invalid),
			"invalid_by_reason": stringIntMap(invalid),
		}
		return boundedSnapshotResult(handler, snapshot, maxBytes)
	}
}

var StreamsSnapshotHandler = NewStreamsSnapshotHandler(nil, "", "")

func normalizeDeliveryStream(record DeliveryStreamRecord) DeliveryStreamRecord {
	record.ID = strings.TrimSpace(record.ID)
	record.Lane = strings.TrimSpace(record.Lane)
	record.State = strings.TrimSpace(record.State)
	record.ImmutableHead = strings.TrimSpace(record.ImmutableHead)
	record.TargetRef = strings.TrimSpace(record.TargetRef)
	record.ProposalState = strings.TrimSpace(record.ProposalState)
	record.GateStatus = strings.TrimSpace(record.GateStatus)
	record.NextAction = strings.TrimSpace(record.NextAction)
	record.ReceiptRef = strings.TrimSpace(record.ReceiptRef)
	return record
}

func validateDeliveryStream(record DeliveryStreamRecord, seen map[string]bool) string {
	if !materializationstatus.Opaque(record.ID) || !safeSemantic(record.Lane, 64) ||
		!safeSemantic(record.State, 64) || !safeSemantic(record.TargetRef, 256) ||
		!safeSemantic(record.ProposalState, 64) || !safeSemantic(record.GateStatus, 64) ||
		!safeSemantic(record.NextAction, 128) || !materializationstatus.Opaque(record.ReceiptRef) ||
		!immutableHeadPattern.MatchString(record.ImmutableHead) {
		return "invalid_schema"
	}
	if seen[record.ID] {
		return "duplicate_id"
	}
	return ""
}

func invalidTotal(values map[string]int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func stringIntMap(values map[string]int) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
