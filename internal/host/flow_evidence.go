package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"kitsoki/internal/clock"
)

const (
	flowEvidenceSchema        = "kitsoki.flow-evidence/v1"
	flowEvidenceMaxSuites     = 16
	flowEvidenceMaxRuns       = 200
	flowEvidenceMaxBytes      = 1024 * 1024
	flowEvidenceMaxFailures   = 200
	flowEvidenceMaxFailureLen = 8192
	flowEvidenceMaxPathLen    = 4096
)

// FlowEvidenceScope is authoritative application and catalog identity resolved
// during server registration and session construction.
type FlowEvidenceScope struct {
	ApplicationID string
	Owner         string
	Revision      string
	CatalogPath   string
}

// FlowEvidenceResolveRequest asks the registered catalog resolver for one node.
// CatalogPath is intentionally absent: the resolver receives only server scope.
type FlowEvidenceResolveRequest struct {
	Scope  FlowEvidenceScope
	Actor  string
	NodeID string
}

// FlowEvidenceSuite is one server-resolved deterministic flow suite. Its paths
// never come from host arguments and are never returned to the caller.
type FlowEvidenceSuite struct {
	ID       string
	Revision string
	AppPath  string
	FlowGlob string
}

// FlowEvidenceTarget is the catalog resolver's authoritative node projection.
// CatalogRevision and suite revisions must change whenever their executable
// content changes so durable replay cannot conceal a newer suite.
type FlowEvidenceTarget struct {
	Scope           FlowEvidenceScope
	CatalogRevision string
	NodeID          string
	Suites          []FlowEvidenceSuite
}

// FlowEvidenceLimits are hard ceilings passed into every runner invocation.
type FlowEvidenceLimits struct {
	MaxSuites        int
	MaxRuns          int
	MaxEvidenceBytes int
}

// FlowEvidenceRunResult is one privacy-safe run result preserved in evidence.
type FlowEvidenceRunResult struct {
	Ref      string   `json:"ref"`
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

// FlowEvidenceSuiteResult is the bounded result returned by a deterministic
// flow runner.
type FlowEvidenceSuiteResult struct {
	SuiteID  string                  `json:"suite_id"`
	Passed   bool                    `json:"passed"`
	RunCount int                     `json:"run_count"`
	Runs     []FlowEvidenceRunResult `json:"runs,omitempty"`
	Error    string                  `json:"error,omitempty"`
}

// FlowEvidenceRecord is the durable replay receipt. Evidence stores must retain
// both passing and failing records.
type FlowEvidenceRecord struct {
	Schema          string                    `json:"schema"`
	Key             string                    `json:"key"`
	EvidenceRef     string                    `json:"evidence_ref"`
	ApplicationID   string                    `json:"application_id"`
	Owner           string                    `json:"owner"`
	AppRevision     string                    `json:"app_revision"`
	CatalogRevision string                    `json:"catalog_revision"`
	NodeID          string                    `json:"node_id"`
	Actor           string                    `json:"actor"`
	Passed          bool                      `json:"passed"`
	RunCount        int                       `json:"run_count"`
	Suites          []FlowEvidenceSuiteResult `json:"suites"`
	RecordedAt      time.Time                 `json:"recorded_at"`
}

// FlowEvidenceCatalogResolver resolves only against its server-held app scope.
type FlowEvidenceCatalogResolver interface {
	ResolveFlowEvidence(context.Context, FlowEvidenceResolveRequest) (FlowEvidenceTarget, error)
}

// FlowEvidenceRunner executes a server-resolved suite through Kitsoki's
// deterministic flow APIs. It must honor Limits before executing any run.
type FlowEvidenceRunner interface {
	RunFlowEvidence(context.Context, FlowEvidenceSuite, FlowEvidenceLimits) (FlowEvidenceSuiteResult, error)
}

// FlowEvidenceStore provides restart-safe replay. PutIfAbsent must atomically
// retain and return the canonical first record for key.
type FlowEvidenceStore interface {
	LookupFlowEvidence(context.Context, string) (FlowEvidenceRecord, bool, error)
	PutFlowEvidenceIfAbsent(context.Context, string, FlowEvidenceRecord) (FlowEvidenceRecord, error)
}

// FlowEvidenceProvider is the complete injected dependency set for one
// application registration.
type FlowEvidenceProvider struct {
	CatalogPath string
	Resolver    FlowEvidenceCatalogResolver
	Runner      FlowEvidenceRunner
	Store       FlowEvidenceStore
	Clock       clock.Clock
	Limits      FlowEvidenceLimits
}

type flowEvidenceHandler struct {
	provider FlowEvidenceProvider
	scope    FlowEvidenceScope
	locks    flowEvidenceKeyLocks
}

type flowEvidenceKeyLocks struct {
	mu      sync.Mutex
	entries map[string]*flowEvidenceKeyLock
}

type flowEvidenceKeyLock struct {
	mu   sync.Mutex
	refs int
}

// NewFlowEvidenceHandler creates an authenticated, app-scoped record provider.
func NewFlowEvidenceHandler(provider FlowEvidenceProvider, scope FlowEvidenceScope) Handler {
	h := &flowEvidenceHandler{provider: provider, scope: scope}
	return h.handle
}

// FlowEvidenceHandler is the fail-closed builtin sentinel.
var FlowEvidenceHandler = NewFlowEvidenceHandler(FlowEvidenceProvider{}, FlowEvidenceScope{})

func (h *flowEvidenceHandler) handle(ctx context.Context, args map[string]any) (Result, error) {
	if h.provider.Resolver == nil || h.provider.Runner == nil ||
		h.provider.Store == nil || h.provider.Clock == nil {
		return Result{Error: "host.flow_evidence: backing service is unavailable"}, nil
	}
	if err := validateFlowEvidenceScope(h.scope); err != nil {
		return Result{Error: "host.flow_evidence: " + err.Error()}, nil
	}
	limits, err := resolveFlowEvidenceLimits(h.provider.Limits)
	if err != nil {
		return Result{}, err
	}
	actor := strings.TrimSpace(ActorFromContext(ctx))
	if actor == "" {
		return Result{Error: "host.flow_evidence: authenticated actor is required"}, nil
	}
	if !validFlowEvidenceText(actor, 512) {
		return Result{Error: "host.flow_evidence: authenticated actor is outside the safe evidence boundary"}, nil
	}
	op, _ := args["op"].(string)
	if strings.TrimSpace(op) != "record" {
		return Result{}, fmt.Errorf("host.flow_evidence: unknown op %q", op)
	}
	return h.record(ctx, actor, args, limits)
}

func (h *flowEvidenceHandler) record(
	ctx context.Context,
	actor string,
	args map[string]any,
	limits FlowEvidenceLimits,
) (Result, error) {
	catalogPath, err := requiredFlowEvidenceString(args, "catalog_path", flowEvidenceMaxPathLen)
	if err != nil {
		return Result{}, err
	}
	if catalogPath != h.scope.CatalogPath {
		return Result{}, fmt.Errorf("host.flow_evidence.record: catalog_path is outside the registered application scope")
	}
	nodeID, err := requiredFlowEvidenceNodeID(args, "node_id")
	if err != nil {
		return Result{}, err
	}
	target, err := h.provider.Resolver.ResolveFlowEvidence(ctx, FlowEvidenceResolveRequest{
		Scope: h.scope, Actor: actor, NodeID: nodeID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("host.flow_evidence.record: resolve catalog node: %w", err)
	}
	target, err = validateFlowEvidenceTarget(target, h.scope, nodeID, limits.MaxSuites)
	if err != nil {
		return Result{}, fmt.Errorf("host.flow_evidence.record: %w", err)
	}
	key, evidenceRef, err := flowEvidenceIdentity(target)
	if err != nil {
		return Result{}, fmt.Errorf("host.flow_evidence.record: derive evidence identity: %w", err)
	}

	unlock := h.locks.lock(key)
	defer unlock()

	if cached, ok, err := h.provider.Store.LookupFlowEvidence(ctx, key); err != nil {
		return Result{}, fmt.Errorf("host.flow_evidence.record: read durable evidence: %w", err)
	} else if ok {
		if err := validateFlowEvidenceRecord(cached, target, key, evidenceRef, limits.MaxRuns); err != nil {
			return Result{}, fmt.Errorf("host.flow_evidence.record: stored evidence: %w", err)
		}
		return flowEvidenceResult(cached), nil
	}

	record := FlowEvidenceRecord{
		Schema:          flowEvidenceSchema,
		Key:             key,
		EvidenceRef:     evidenceRef,
		ApplicationID:   h.scope.ApplicationID,
		Owner:           h.scope.Owner,
		AppRevision:     h.scope.Revision,
		CatalogRevision: target.CatalogRevision,
		NodeID:          target.NodeID,
		Actor:           actor,
		Suites:          make([]FlowEvidenceSuiteResult, 0, len(target.Suites)),
		RecordedAt:      h.provider.Clock.Now().UTC(),
	}
	allPassed := true
	for _, suite := range target.Suites {
		remaining := limits.MaxRuns - record.RunCount
		suiteResult, runErr := h.provider.Runner.RunFlowEvidence(ctx, suite, FlowEvidenceLimits{
			MaxSuites: limits.MaxSuites,
			MaxRuns:   remaining, MaxEvidenceBytes: limits.MaxEvidenceBytes,
		})
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if runErr != nil {
			suiteResult = FlowEvidenceSuiteResult{
				SuiteID: suite.ID,
				Passed:  false,
				Error:   boundedFlowEvidenceError(runErr),
			}
		}
		if err := validateFlowEvidenceSuiteResult(suiteResult, suite.ID, remaining); err != nil {
			return Result{}, fmt.Errorf("host.flow_evidence.record: runner result: %w", err)
		}
		record.Suites = append(record.Suites, suiteResult)
		record.RunCount += suiteResult.RunCount
		if !suiteResult.Passed || suiteResult.RunCount == 0 {
			allPassed = false
		}
	}
	record.Passed = allPassed && record.RunCount > 0
	if err := validateFlowEvidenceRecord(record, target, key, evidenceRef, limits.MaxRuns); err != nil {
		return Result{}, fmt.Errorf("host.flow_evidence.record: generated evidence: %w", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return Result{}, fmt.Errorf("host.flow_evidence.record: encode evidence: %w", err)
	}
	if len(encoded) > limits.MaxEvidenceBytes {
		return Result{}, fmt.Errorf(
			"host.flow_evidence.record: evidence is %d bytes, exceeds %d; refusing to truncate",
			len(encoded), limits.MaxEvidenceBytes,
		)
	}
	stored, err := h.provider.Store.PutFlowEvidenceIfAbsent(ctx, key, record)
	if err != nil {
		return Result{}, fmt.Errorf("host.flow_evidence.record: persist durable evidence: %w", err)
	}
	if err := validateFlowEvidenceRecord(stored, target, key, evidenceRef, limits.MaxRuns); err != nil {
		return Result{}, fmt.Errorf("host.flow_evidence.record: persisted evidence: %w", err)
	}
	return flowEvidenceResult(stored), nil
}

func flowEvidenceResult(record FlowEvidenceRecord) Result {
	return Result{Data: map[string]any{
		"evidence_ref": record.EvidenceRef,
		"passed":       record.Passed,
		"run_count":    record.RunCount,
	}}
}

func validateFlowEvidenceScope(scope FlowEvidenceScope) error {
	switch {
	case !validFlowEvidenceToken(scope.ApplicationID, 128):
		return fmt.Errorf("server could not resolve a privacy-safe application id")
	case !validFlowEvidenceText(scope.Owner, 512):
		return fmt.Errorf("server could not resolve the application owner")
	case !validFlowEvidenceToken(scope.Revision, 128):
		return fmt.Errorf("server could not resolve a privacy-safe application revision")
	case !validFlowEvidenceText(scope.CatalogPath, flowEvidenceMaxPathLen):
		return fmt.Errorf("server could not resolve the registered catalog path")
	default:
		return nil
	}
}

func resolveFlowEvidenceLimits(configured FlowEvidenceLimits) (FlowEvidenceLimits, error) {
	if configured.MaxSuites == 0 {
		configured.MaxSuites = flowEvidenceMaxSuites
	}
	if configured.MaxRuns == 0 {
		configured.MaxRuns = flowEvidenceMaxRuns
	}
	if configured.MaxEvidenceBytes == 0 {
		configured.MaxEvidenceBytes = flowEvidenceMaxBytes
	}
	switch {
	case configured.MaxSuites < 1 || configured.MaxSuites > flowEvidenceMaxSuites:
		return FlowEvidenceLimits{}, fmt.Errorf(
			"host.flow_evidence: max suites must be between 1 and %d", flowEvidenceMaxSuites,
		)
	case configured.MaxRuns < 1 || configured.MaxRuns > flowEvidenceMaxRuns:
		return FlowEvidenceLimits{}, fmt.Errorf(
			"host.flow_evidence: max runs must be between 1 and %d", flowEvidenceMaxRuns,
		)
	case configured.MaxEvidenceBytes < 1 || configured.MaxEvidenceBytes > flowEvidenceMaxBytes:
		return FlowEvidenceLimits{}, fmt.Errorf(
			"host.flow_evidence: max evidence bytes must be between 1 and %d", flowEvidenceMaxBytes,
		)
	}
	return configured, nil
}

func validateFlowEvidenceTarget(
	target FlowEvidenceTarget,
	scope FlowEvidenceScope,
	nodeID string,
	maxSuites int,
) (FlowEvidenceTarget, error) {
	if target.Scope != scope {
		return FlowEvidenceTarget{}, fmt.Errorf("catalog resolver returned a target outside the registered application scope")
	}
	if target.NodeID != nodeID {
		return FlowEvidenceTarget{}, fmt.Errorf("catalog resolver returned node %q instead of %q", target.NodeID, nodeID)
	}
	if !validFlowEvidenceToken(target.CatalogRevision, 128) {
		return FlowEvidenceTarget{}, fmt.Errorf("catalog resolver returned an unsafe catalog revision")
	}
	if len(target.Suites) == 0 {
		return FlowEvidenceTarget{}, fmt.Errorf("catalog node %q declares no deterministic flow suites", nodeID)
	}
	if len(target.Suites) > maxSuites {
		return FlowEvidenceTarget{}, fmt.Errorf(
			"catalog node %q declares %d flow suites, exceeds %d; refusing to truncate",
			nodeID, len(target.Suites), maxSuites,
		)
	}
	suites := append([]FlowEvidenceSuite(nil), target.Suites...)
	sort.Slice(suites, func(i, j int) bool { return suites[i].ID < suites[j].ID })
	seen := make(map[string]struct{}, len(suites))
	for _, suite := range suites {
		switch {
		case !validFlowEvidenceToken(suite.ID, 128):
			return FlowEvidenceTarget{}, fmt.Errorf("catalog resolver returned an unsafe suite id")
		case !validFlowEvidenceToken(suite.Revision, 128):
			return FlowEvidenceTarget{}, fmt.Errorf("catalog resolver returned unsafe revision for suite %q", suite.ID)
		case !validFlowEvidenceText(suite.AppPath, flowEvidenceMaxPathLen):
			return FlowEvidenceTarget{}, fmt.Errorf("catalog resolver returned unsafe app path for suite %q", suite.ID)
		case !validFlowEvidenceText(suite.FlowGlob, flowEvidenceMaxPathLen):
			return FlowEvidenceTarget{}, fmt.Errorf("catalog resolver returned unsafe flow selector for suite %q", suite.ID)
		}
		if _, duplicate := seen[suite.ID]; duplicate {
			return FlowEvidenceTarget{}, fmt.Errorf("catalog resolver returned duplicate suite id %q", suite.ID)
		}
		seen[suite.ID] = struct{}{}
	}
	target.Suites = suites
	return target, nil
}

func flowEvidenceIdentity(target FlowEvidenceTarget) (string, string, error) {
	raw, err := json.Marshal(struct {
		Schema string             `json:"schema"`
		Target FlowEvidenceTarget `json:"target"`
	}{Schema: flowEvidenceSchema, Target: target})
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:16])
	return "flow-evidence-key:" + digest, "flow-evidence:" + digest, nil
}

func validateFlowEvidenceSuiteResult(result FlowEvidenceSuiteResult, suiteID string, remaining int) error {
	switch {
	case result.SuiteID != suiteID:
		return fmt.Errorf("suite %q returned identity %q", suiteID, result.SuiteID)
	case result.RunCount < 0 || result.RunCount > remaining:
		return fmt.Errorf("suite %q returned %d runs, exceeds remaining limit %d; refusing to truncate", suiteID, result.RunCount, remaining)
	case len(result.Runs) != result.RunCount:
		return fmt.Errorf("suite %q returned %d run records for run_count %d", suiteID, len(result.Runs), result.RunCount)
	case len(result.Error) > flowEvidenceMaxFailureLen || !validFlowEvidenceMultiline(result.Error):
		return fmt.Errorf("suite %q returned an unsafe error", suiteID)
	case result.Passed && result.Error != "":
		return fmt.Errorf("suite %q returned passed with an error", suiteID)
	}
	allRunsPassed := result.RunCount > 0
	for _, run := range result.Runs {
		if !validFlowEvidenceToken(run.Ref, 180) {
			return fmt.Errorf("suite %q returned an unsafe run ref", suiteID)
		}
		if len(run.Failures) > flowEvidenceMaxFailures {
			return fmt.Errorf("suite %q returned too many failures; refusing to truncate", suiteID)
		}
		for _, failure := range run.Failures {
			if len(failure) > flowEvidenceMaxFailureLen || !validFlowEvidenceMultiline(failure) {
				return fmt.Errorf("suite %q returned an unsafe failure", suiteID)
			}
		}
		if run.Passed && len(run.Failures) > 0 {
			return fmt.Errorf("suite %q returned a passing run with failures", suiteID)
		}
		if result.Passed && !run.Passed {
			return fmt.Errorf("suite %q returned passed with a failing run", suiteID)
		}
		if !run.Passed {
			allRunsPassed = false
		}
	}
	if result.Passed != (allRunsPassed && result.Error == "") {
		return fmt.Errorf("suite %q passed does not match its run results", suiteID)
	}
	return nil
}

func validateFlowEvidenceRecord(
	record FlowEvidenceRecord,
	target FlowEvidenceTarget,
	key string,
	evidenceRef string,
	maxRuns int,
) error {
	switch {
	case record.Schema != flowEvidenceSchema:
		return fmt.Errorf("unsupported schema %q", record.Schema)
	case record.Key != key || record.EvidenceRef != evidenceRef:
		return fmt.Errorf("identity does not match the resolved target")
	case record.ApplicationID != target.Scope.ApplicationID ||
		record.Owner != target.Scope.Owner ||
		record.AppRevision != target.Scope.Revision ||
		record.CatalogRevision != target.CatalogRevision ||
		record.NodeID != target.NodeID:
		return fmt.Errorf("scope does not match the resolved target")
	case !validFlowEvidenceText(record.Actor, 512):
		return fmt.Errorf("actor is outside the safe evidence boundary")
	case record.RecordedAt.IsZero():
		return fmt.Errorf("recorded_at is required")
	case record.RunCount < 0 || record.RunCount > maxRuns:
		return fmt.Errorf("run_count %d is outside the supported boundary", record.RunCount)
	case len(record.Suites) != len(target.Suites):
		return fmt.Errorf("suite result count does not match the resolved target")
	}
	total := 0
	allPassed := true
	for i, suite := range record.Suites {
		if err := validateFlowEvidenceSuiteResult(suite, target.Suites[i].ID, maxRuns-total); err != nil {
			return err
		}
		total += suite.RunCount
		if !suite.Passed || suite.RunCount == 0 {
			allPassed = false
		}
	}
	if total != record.RunCount {
		return fmt.Errorf("suite run total %d does not match run_count %d", total, record.RunCount)
	}
	if record.Passed != (allPassed && total > 0) {
		return fmt.Errorf("passed does not match suite results")
	}
	return nil
}

func boundedFlowEvidenceError(err error) string {
	message := strings.TrimSpace(err.Error())
	if len(message) <= flowEvidenceMaxFailureLen && validFlowEvidenceMultiline(message) {
		return message
	}
	return "flow runner returned an error outside the evidence boundary"
}

func requiredFlowEvidenceString(args map[string]any, name string, max int) (string, error) {
	raw, ok := args[name]
	if !ok {
		return "", fmt.Errorf("host.flow_evidence.record: missing required arg %q", name)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("host.flow_evidence.record: %q must be a string, got %T", name, raw)
	}
	value = strings.TrimSpace(value)
	if !validFlowEvidenceText(value, max) {
		return "", fmt.Errorf("host.flow_evidence.record: %q is outside its safe boundary", name)
	}
	return value, nil
}

func requiredFlowEvidenceNodeID(args map[string]any, name string) (string, error) {
	value, err := requiredFlowEvidenceString(args, name, 128)
	if err != nil {
		return "", err
	}
	if value[0] < 'a' || value[0] > 'z' {
		return "", fmt.Errorf("host.flow_evidence.record: %q must be a catalog node id", name)
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return "", fmt.Errorf("host.flow_evidence.record: %q must be a catalog node id", name)
		}
	}
	return value, nil
}

func validFlowEvidenceToken(value string, max int) bool {
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

func validFlowEvidenceText(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validFlowEvidenceMultiline(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func (l *flowEvidenceKeyLocks) lock(key string) func() {
	l.mu.Lock()
	if l.entries == nil {
		l.entries = make(map[string]*flowEvidenceKeyLock)
	}
	entry := l.entries[key]
	if entry == nil {
		entry = &flowEvidenceKeyLock{}
		l.entries[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.entries, key)
		}
		l.mu.Unlock()
	}
}
