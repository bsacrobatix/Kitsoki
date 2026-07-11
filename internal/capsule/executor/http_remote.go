package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Credential supplies an ephemeral transport credential. Its value is sent
// only in the request header; it is never copied into prepared state, envelopes
// or executor events.
type Credential func(context.Context) (string, error)

// HTTPRemoteWorker is the production transport adapter for a Capsule worker
// service. The service owns source materialization and story execution; this
// controller sends an already sealed envelope and accepts only its normalized
// typed result. It deliberately has no fallback to local task execution.
type HTTPRemoteWorker struct {
	Endpoint   string
	Client     *http.Client
	Credential Credential
}

func (w HTTPRemoteWorker) Describe(ctx context.Context) (Capabilities, error) {
	var response struct {
		Capabilities Capabilities `json:"capabilities"`
	}
	if err := w.call(ctx, http.MethodGet, "/v1/capsules/capabilities", nil, &response); err != nil {
		return Capabilities{}, err
	}
	return response.Capabilities, nil
}

func (w HTTPRemoteWorker) Run(ctx context.Context, prepared Prepared, _ Task, sink EventSink) (Result, error) {
	if sink != nil {
		_ = sink.Emit(ctx, Event{Kind: "capsule.executor.started", EnvelopeDigest: prepared.Envelope.Digest, ExecutionID: prepared.ID, Fields: map[string]any{"transport": "https", "remote_host": endpointHost(w.Endpoint)}})
	}
	var response struct {
		Result Result `json:"result"`
	}
	err := w.call(ctx, http.MethodPost, "/v1/capsules/run", map[string]Prepared{"prepared": prepared}, &response)
	if err == nil {
		response.Result.ExecutionID = prepared.ID
		sort.Strings(response.Result.Artifacts)
	}
	if sink != nil {
		kind := "capsule.executor.finished"
		fields := map[string]any{"transport": "https", "remote_host": endpointHost(w.Endpoint)}
		if err != nil {
			kind = "capsule.executor.failed"
			fields["error_kind"] = remoteErrorKind(err)
		}
		_ = sink.Emit(ctx, Event{Kind: kind, EnvelopeDigest: prepared.Envelope.Digest, ExecutionID: prepared.ID, Fields: fields})
	}
	return response.Result, err
}

func (w HTTPRemoteWorker) Cancel(ctx context.Context, id string) error {
	return w.call(ctx, http.MethodDelete, "/v1/capsules/executions/"+url.PathEscape(id), nil, nil)
}

func (w HTTPRemoteWorker) call(ctx context.Context, method, path string, body any, out any) error {
	started := time.Now()
	endpoint := strings.TrimRight(w.Endpoint, "/")
	if endpoint == "" || !strings.HasPrefix(endpoint, "https://") {
		return fmt.Errorf("capsule executor: remote endpoint must use https")
	}
	endpointURL, _ := url.Parse(endpoint)
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	requestID := newRequestID()
	req.Header.Set("X-Kitsoki-Request-ID", requestID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if w.Credential != nil {
		credential, err := w.Credential(ctx)
		if err != nil {
			return fmt.Errorf("capsule executor: remote credential: %w", err)
		}
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
	}
	client := w.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return remoteCallError{Method: method, Path: path, Host: endpointURL.Host, RequestID: requestID, Duration: time.Since(started), Kind: "transport", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return remoteCallError{Method: method, Path: path, Host: endpointURL.Host, RequestID: firstNonEmpty(response.Header.Get("X-Kitsoki-Request-ID"), requestID), Status: response.Status, Duration: time.Since(started), Kind: "status", Body: sanitizeRemoteBody(body)}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return remoteCallError{Method: method, Path: path, Host: endpointURL.Host, RequestID: firstNonEmpty(response.Header.Get("X-Kitsoki-Request-ID"), requestID), Status: response.Status, Duration: time.Since(started), Kind: "decode", Cause: err}
	}
	return nil
}

type remoteCallError struct {
	Method    string
	Path      string
	Host      string
	RequestID string
	Status    string
	Duration  time.Duration
	Kind      string
	Body      string
	Cause     error
}

func (e remoteCallError) Error() string {
	parts := []string{fmt.Sprintf("capsule executor: remote %s %s failed", e.Method, e.Path)}
	if e.Kind != "" {
		parts = append(parts, "kind="+e.Kind)
	}
	if e.Host != "" {
		parts = append(parts, "host="+e.Host)
	}
	if e.Status != "" {
		parts = append(parts, "status="+e.Status)
	}
	if e.RequestID != "" {
		parts = append(parts, "request_id="+e.RequestID)
	}
	if e.Duration > 0 {
		parts = append(parts, "duration="+e.Duration.Round(time.Millisecond).String())
	}
	if e.Body != "" {
		parts = append(parts, "body="+e.Body)
	}
	if e.Cause != nil {
		parts = append(parts, "cause="+e.Cause.Error())
	}
	return strings.Join(parts, " ")
}

func (e remoteCallError) Unwrap() error { return e.Cause }

func remoteErrorKind(err error) string {
	if e, ok := err.(remoteCallError); ok && e.Kind != "" {
		return e.Kind
	}
	return "unknown"
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return "req-" + hex.EncodeToString(b[:])
}

func endpointHost(endpoint string) string {
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil {
		return ""
	}
	return u.Host
}

func sanitizeRemoteBody(raw []byte) string {
	text := strings.TrimSpace(strings.ReplaceAll(string(raw), "\n", " "))
	text = strings.ReplaceAll(text, "\r", " ")
	fields := strings.Fields(text)
	for i, field := range fields {
		lower := strings.ToLower(field)
		for _, marker := range []string{"token", "secret", "password", "credential", "api_key", "private_key"} {
			if strings.Contains(lower, marker) {
				fields[i] = marker + "=<redacted>"
				break
			}
		}
	}
	text = strings.Join(fields, " ")
	if len(text) > 512 {
		return text[:512] + "…"
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

var _ RemoteWorker = HTTPRemoteWorker{}
