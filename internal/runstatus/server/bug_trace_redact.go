package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"kitsoki/internal/runstatus"
	"kitsoki/internal/runstatus/harscrub"
)

const redactedTraceKind = "TEXT"

var traceSafeStringKeys = map[string]bool{
	"agent":            true,
	"call_id":          true,
	"chosen_intent":    true,
	"component":        true,
	"decider":          true,
	"error_code":       true,
	"format":           true,
	"from":             true,
	"from_decision_id": true,
	"from_state":       true,
	"handler":          true,
	"harness":          true,
	"id":               true,
	"intent":           true,
	"kind":             true,
	"level":            true,
	"match_type":       true,
	"model":            true,
	"msg":              true,
	"parent_trace_id":  true,
	"phase":            true,
	"profile":          true,
	"provider":         true,
	"reason":           true,
	"role":             true,
	"route":            true,
	"routed_by":        true,
	"schema_version":   true,
	"state":            true,
	"state_path":       true,
	"status":           true,
	"target":           true,
	"task_trace_id":    true,
	"to":               true,
	"to_decision_id":   true,
	"to_state":         true,
	"trace_id":         true,
	"type":             true,
	"verb":             true,
}

var traceSafeStringListKeys = map[string]bool{
	"available_intents": true,
	"intents":           true,
	"removed":           true,
}

// decodeTraceEvidence resolves the reported session and exports a redacted JSONL
// trace attachment. It never reads a client-supplied path; trace_ref is treated
// only as a server-side session id. Unknown ids and snapshot failures simply
// omit the optional attachment so bug filing still works.
func (s *Server) decodeTraceEvidence(params map[string]any, opts harscrub.ScrubOptions) []byte {
	sessionID := strings.TrimSpace(stringParam(params, "trace_ref"))
	if sessionID == "" || s.provider == nil {
		return nil
	}
	entry, rerr := s.resolve(map[string]any{"session_id": sessionID})
	if rerr != nil {
		return nil
	}
	snap, err := entry.Source.Snapshot()
	if err != nil || len(snap.Events) == 0 {
		return nil
	}
	return RedactedTraceJSONL(snap.Events, opts)
}

// RedactedTraceJSONL renders trace events as JSONL with user free text aliased
// and credential/home-path patterns scrubbed. It is shared by web-filed bug
// reports and MCP issue filing so every bug path applies the same trace privacy
// rules.
func RedactedTraceJSONL(events []runstatus.TraceEvent, opts harscrub.ScrubOptions) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	redactor := newTraceRedactor(opts)
	for _, ev := range events {
		line := redactor.redactedTraceEvent(ev)
		if err := enc.Encode(line); err != nil {
			continue
		}
	}
	return buf.Bytes()
}

// RedactedTraceValue applies the trace redaction rules to a structured value.
// It is useful for sibling evidence such as a session world snapshot, where
// arbitrary string fields should not be published verbatim.
func RedactedTraceValue(key string, v any, opts harscrub.ScrubOptions) any {
	return newTraceRedactor(opts).redactTraceValue(key, v)
}

type traceRedactor struct {
	opts    harscrub.ScrubOptions
	aliases map[string]string
	next    int
}

func newTraceRedactor(opts harscrub.ScrubOptions) *traceRedactor {
	return &traceRedactor{
		opts:    opts,
		aliases: make(map[string]string),
	}
}

func (r *traceRedactor) redactedTraceEvent(ev runstatus.TraceEvent) map[string]any {
	out := map[string]any{
		"level":      ev.Level,
		"msg":        ev.Msg,
		"session_id": scrubSafeTraceString(ev.SessionID, r.opts),
		"turn":       ev.Turn,
		"state_path": scrubSafeTraceString(ev.StatePath, r.opts),
	}
	if !ev.Time.IsZero() {
		out["time"] = ev.Time.Format(time.RFC3339Nano)
	}
	if ev.ParentTurn != 0 {
		out["parent_turn"] = ev.ParentTurn
	}
	for k, v := range ev.Attrs {
		if _, exists := out[k]; exists {
			continue
		}
		out[k] = r.redactTraceValue(k, v)
	}
	return out
}

func (r *traceRedactor) redactTraceValue(key string, v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case bool, float64, int, int64, json.Number:
		return x
	case string:
		if traceSafeStringKeys[key] {
			return scrubSafeTraceString(x, r.opts)
		}
		return r.aliasString(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			if s, ok := item.(string); ok && traceSafeStringListKeys[key] {
				out[i] = scrubSafeTraceString(s, r.opts)
				continue
			}
			out[i] = r.redactTraceValue("", item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for childKey, childVal := range x {
			out[childKey] = r.redactTraceValue(childKey, childVal)
		}
		return out
	default:
		return r.aliasString(fmt.Sprint(x))
	}
}

func (r *traceRedactor) aliasString(s string) string {
	scrubbed := harscrub.ScrubString(s, r.opts)
	if scrubbed == "" {
		return ""
	}
	if alias, ok := r.aliases[s]; ok {
		return alias
	}
	r.next++
	alias := fmt.Sprintf("[%s#%d len=%d]", redactedTraceKind, r.next, utf8.RuneCountInString(scrubbed))
	r.aliases[s] = alias
	return alias
}

func scrubSafeTraceString(s string, opts harscrub.ScrubOptions) string {
	return harscrub.ScrubString(s, opts)
}
