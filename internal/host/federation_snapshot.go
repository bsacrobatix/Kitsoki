package host

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"kitsoki/internal/materializationstatus"
)

const (
	federationSnapshotSchema   = "kitsoki/federation-snapshot/v1"
	federationSnapshotMaxItems = 200
	federationSnapshotMaxBytes = 256 * 1024
)

type FederationCapabilities struct {
	Placements []string
	Isolation  string
	Networks   []string
}

type FederationWorker struct {
	ID           string
	Label        string
	Placement    string
	Health       string
	Capabilities FederationCapabilities
	Enabled      bool
	Jobs         int
}

type FederationPolicy struct {
	Lane            string
	WorkerClasses   []string
	NetworkProfiles []string
}

type FederationProjection struct {
	Workers []FederationWorker
	Policy  []FederationPolicy
}

type FederationSource interface {
	FederationSnapshot(context.Context) (FederationProjection, error)
}

func NewFederationSnapshotHandler(source FederationSource, applicationID string) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		const handler = "host.federation.snapshot"
		if source == nil || !materializationstatus.Opaque(applicationID) {
			return Result{Error: handler + ": federation projection is unavailable outside configured daemon scope"}, nil
		}
		if op, _ := args["op"].(string); op != "" && op != "snapshot" {
			return Result{}, fmt.Errorf("host.federation: unknown op %q", op)
		}
		if err := rejectSnapshotArgs(args, handler, "max_workers", "max_bytes"); err != nil {
			return Result{}, err
		}
		maxItems, err := requiredSnapshotBound(args, handler, "max_workers", federationSnapshotMaxItems)
		if err != nil {
			return Result{}, err
		}
		maxBytes, err := requiredSnapshotBound(args, handler, "max_bytes", federationSnapshotMaxBytes)
		if err != nil {
			return Result{}, err
		}
		projection, err := source.FederationSnapshot(ctx)
		if err != nil {
			return Result{}, fmt.Errorf("%s: read daemon federation: %w", handler, err)
		}
		if len(projection.Workers) > maxItems {
			return Result{}, fmt.Errorf("%s: selected more than %d workers; refusing to truncate", handler, maxItems)
		}
		sort.Slice(projection.Workers, func(i, j int) bool { return projection.Workers[i].ID < projection.Workers[j].ID })
		seen := map[string]bool{}
		rows := make([]any, 0, len(projection.Workers))
		invalid := map[string]int{}
		for _, worker := range projection.Workers {
			if !validFederationWorker(worker) {
				invalid["invalid_schema"]++
				continue
			}
			if seen[worker.ID] {
				invalid["duplicate_id"]++
				continue
			}
			seen[worker.ID] = true
			rows = append(rows, map[string]any{
				"id": worker.ID, "label": worker.Label, "placement": worker.Placement,
				"health": worker.Health, "enabled": worker.Enabled, "jobs": worker.Jobs,
				"capabilities": map[string]any{
					"placements": cleanSemanticList(worker.Capabilities.Placements),
					"isolation":  worker.Capabilities.Isolation,
					"networks":   cleanSemanticList(worker.Capabilities.Networks),
				},
			})
		}
		sort.Slice(projection.Policy, func(i, j int) bool { return projection.Policy[i].Lane < projection.Policy[j].Lane })
		policyRows := make([]any, 0, len(projection.Policy))
		seenPolicy := map[string]bool{}
		for _, policy := range projection.Policy {
			if !validFederationPolicy(policy) {
				invalid["invalid_policy"]++
				continue
			}
			if seenPolicy[policy.Lane] {
				invalid["duplicate_policy"]++
				continue
			}
			seenPolicy[policy.Lane] = true
			policyRows = append(policyRows, map[string]any{
				"lane":             policy.Lane,
				"worker_classes":   cleanSemanticList(policy.WorkerClasses),
				"network_profiles": cleanSemanticList(policy.NetworkProfiles),
			})
		}
		snapshot := map[string]any{
			"schema": federationSnapshotSchema, "application_id": applicationID,
			"workers": rows, "policy": policyRows, "invalid_count": invalidTotal(invalid),
			"invalid_by_reason": stringIntMap(invalid),
		}
		return boundedSnapshotResult(handler, snapshot, maxBytes)
	}
}

var FederationSnapshotHandler = NewFederationSnapshotHandler(nil, "")

func validFederationWorker(worker FederationWorker) bool {
	if !materializationstatus.Opaque(worker.ID) || !safeSemantic(worker.Label, 256) ||
		!safeSemantic(worker.Placement, 64) || !safeSemantic(worker.Health, 64) ||
		worker.Jobs < 0 || worker.Jobs > 1_000_000 {
		return false
	}
	if worker.Capabilities.Isolation != "" && !safeSemantic(worker.Capabilities.Isolation, 128) {
		return false
	}
	return len(cleanSemanticList(worker.Capabilities.Placements)) == len(worker.Capabilities.Placements) &&
		len(cleanSemanticList(worker.Capabilities.Networks)) == len(worker.Capabilities.Networks)
}

func validFederationPolicy(policy FederationPolicy) bool {
	return safeSemantic(policy.Lane, 128) &&
		len(cleanSemanticList(policy.WorkerClasses)) == len(policy.WorkerClasses) &&
		len(cleanSemanticList(policy.NetworkProfiles)) == len(policy.NetworkProfiles)
}

func cleanSemanticList(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !safeSemantic(value, 128) || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
