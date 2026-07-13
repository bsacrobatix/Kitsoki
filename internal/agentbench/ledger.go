package agentbench

// Immutable request-ledger and friction sidecars are deliberately trace-only:
// they must be usable to audit a completed run without contacting a provider.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const ProviderRequestLedgerV1 = "provider-request-ledger/v1"
const FrictionV1 = "friction/v1"

type RequestIdentity struct {
	RunID     string `json:"run_id"`
	AttemptID string `json:"attempt_id"`
}

// ProviderRequest is append-only evidence for one provider invocation. Money
// remains a decimal string: float conversion is forbidden at this boundary.
type ProviderRequest struct {
	Schema       string       `json:"schema"`
	RequestID    string       `json:"request_id"`
	RunID        string       `json:"run_id"`
	AttemptID    string       `json:"attempt_id"`
	Stage        string       `json:"stage"`
	Requested    ModelRequest `json:"requested"`
	Resolved     ModelRequest `json:"resolved"`
	StartedNS    int64        `json:"started_monotonic_ns"`
	FinishedNS   int64        `json:"finished_monotonic_ns"`
	Status       string       `json:"status"`
	Error        string       `json:"error,omitempty"`
	Usage        TokenUsage   `json:"usage"`
	Money        Money        `json:"money"`
	EvidenceHash string       `json:"evidence_hash"`
}

type ModelRequest struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}
type TokenUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_tokens"`
	CacheRead  int64 `json:"cache_read_tokens"`
	CacheWrite int64 `json:"cache_write_tokens"`
}
type Money struct {
	Amount   string `json:"amount_decimal,omitempty"`
	Currency string `json:"currency"`
	Basis    string `json:"basis,omitempty"`
}

type ledgerEvent struct {
	TS        time.Time      `json:"ts"`
	Kind      string         `json:"kind"`
	StatePath string         `json:"state_path"`
	CallID    string         `json:"call_id"`
	Payload   map[string]any `json:"payload"`
}

// BuildProviderRequestLedger reconstructs one immutable row per terminal
// provider call. A duplicate terminal event is rejected rather than silently
// becoming a last-write-wins update. Failed calls remain rows with zero/unknown
// usage, which is materially different from a zero-cost success.
func BuildProviderRequestLedger(trace string, identity RequestIdentity) ([]ProviderRequest, error) {
	f, err := os.Open(trace)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	starts := map[string]ledgerEvent{}
	terminal := map[string]bool{}
	var rows []ProviderRequest
	var n int64
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for s.Scan() {
		n++
		var ev ledgerEvent
		if err := json.Unmarshal(s.Bytes(), &ev); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", trace, n, err)
		}
		if ev.CallID == "" {
			continue
		}
		switch ev.Kind {
		case "agent.call.start":
			starts[ev.CallID] = ev
		case "agent.call.complete", "agent.call.error":
			start, ok := starts[ev.CallID]
			if !ok {
				continue
			}
			if terminal[ev.CallID] {
				return nil, fmt.Errorf("%s:%d: duplicate terminal evidence for request %q", trace, n, ev.CallID)
			}
			terminal[ev.CallID] = true
			requested := modelFrom(start.Payload)
			resolved := modelFrom(ev.Payload)
			if resolved.Model == "" {
				resolved = requested
			}
			usage, money := usageFrom(ev.Payload)
			status := "completed"
			if ev.Kind == "agent.call.error" {
				status = "failed"
			}
			row := ProviderRequest{Schema: ProviderRequestLedgerV1, RequestID: ev.CallID, RunID: identity.RunID, AttemptID: identity.AttemptID, Stage: start.StatePath, Requested: requested, Resolved: resolved, StartedNS: start.TS.UnixNano(), FinishedNS: ev.TS.UnixNano(), Status: status, Usage: usage, Money: money}
			if status == "failed" {
				row.Error, _ = ev.Payload["error"].(string)
			}
			raw, _ := json.Marshal(struct {
				Start    ledgerEvent `json:"start"`
				Terminal ledgerEvent `json:"terminal"`
			}{start, ev})
			sum := sha256.Sum256(raw)
			row.EvidenceHash = hex.EncodeToString(sum[:])
			rows = append(rows, row)
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].RequestID < rows[j].RequestID })
	return rows, nil
}

func modelFrom(p map[string]any) ModelRequest {
	return ModelRequest{Provider: stringField(p, "provider", "backend"), Model: stringField(p, "model", "resolved_model"), Effort: stringField(p, "effort", "reasoning_effort")}
}
func stringField(p map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := p[k].(string); ok {
			return v
		}
	}
	return ""
}
func usageFrom(p map[string]any) (TokenUsage, Money) {
	if m, ok := p["meta"].(map[string]any); ok {
		p = m
	}
	u, _ := p["usage"].(map[string]any)
	if u == nil {
		u = p
	}
	return TokenUsage{Input: int64Value(u, "input_tokens"), Output: int64Value(u, "output_tokens"), Reasoning: int64Value(u, "reasoning_tokens"), CacheRead: int64Value(u, "cache_read_input_tokens"), CacheWrite: int64Value(u, "cache_creation_input_tokens")}, Money{Amount: stringField(p, "cost_decimal", "cost_usd_decimal"), Currency: "USD", Basis: stringField(p, "monetary_basis")}
}

type FrictionReport struct {
	Schema                    string   `json:"schema"`
	Trace                     string   `json:"trace"`
	ToolErrors                Metric   `json:"tool_errors"`
	SchemaFailures            Metric   `json:"schema_failures"`
	Retries                   Metric   `json:"retries"`
	NoStateChangeCalls        Metric   `json:"no_state_change_calls"`
	CrawlTokens               Metric   `json:"crawl_tokens"`
	TimeToFirstSuccessfulCall Metric   `json:"time_to_first_successful_call_ms"`
	Evidence                  []string `json:"evidence"`
}
type Metric struct {
	Available bool   `json:"available"`
	Value     int64  `json:"value,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// AnalyzeFriction reports only facts that trace evidence supports. In
// particular token-attribution metrics are unavailable when provider usage is
// absent; they are never fabricated as zero.
func AnalyzeFriction(trace string) (FrictionReport, error) {
	f, err := os.Open(trace)
	if err != nil {
		return FrictionReport{}, err
	}
	defer f.Close()
	r := FrictionReport{Schema: FrictionV1, Trace: trace}
	var first time.Time
	var success time.Time
	var toolErrors, schemaFailures, retries, noChange int64
	var crawlTokens int64
	usageSeen := false
	lastTool := ""
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for line := 1; s.Scan(); line++ {
		var ev ledgerEvent
		if err := json.Unmarshal(s.Bytes(), &ev); err != nil {
			return r, fmt.Errorf("%s:%d: %w", trace, line, err)
		}
		if first.IsZero() {
			first = ev.TS
		}
		if ev.Kind != "agent.stream" {
			continue
		}
		tool := stringField(ev.Payload, "tool", "name")
		if tool == "" {
			continue
		}
		r.Evidence = append(r.Evidence, fmt.Sprintf("%s:%d", trace, line))
		if tool == lastTool {
			retries++
		}
		lastTool = tool
		text := strings.ToLower(stringField(ev.Payload, "error", "text", "preview"))
		if strings.Contains(text, "error") {
			toolErrors++
		}
		if strings.Contains(text, "schema") || strings.Contains(text, "validation") {
			schemaFailures++
		}
		if strings.Contains(text, "no state change") {
			noChange++
		}
		if success.IsZero() && !strings.Contains(text, "error") {
			success = ev.TS
		}
		if strings.Contains(strings.ToLower(tool), "read") || strings.Contains(strings.ToLower(tool), "search") {
			if u, ok := ev.Payload["usage"].(map[string]any); ok {
				crawlTokens += int64Value(u, "input_tokens")
				usageSeen = true
			}
		}
	}
	if err := s.Err(); err != nil {
		return r, err
	}
	r.ToolErrors = Metric{Available: true, Value: toolErrors}
	r.SchemaFailures = Metric{Available: true, Value: schemaFailures}
	r.Retries = Metric{Available: true, Value: retries}
	r.NoStateChangeCalls = Metric{Available: true, Value: noChange}
	if usageSeen {
		r.CrawlTokens = Metric{Available: true, Value: crawlTokens}
	} else {
		r.CrawlTokens = Metric{Reason: "trace has no per-tool token attribution"}
	}
	if !first.IsZero() && !success.IsZero() {
		r.TimeToFirstSuccessfulCall = Metric{Available: true, Value: success.Sub(first).Milliseconds()}
	} else {
		r.TimeToFirstSuccessfulCall = Metric{Reason: "trace has no successful tool call"}
	}
	return r, nil
}
