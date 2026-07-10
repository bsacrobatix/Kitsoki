package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
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
		_ = sink.Emit(ctx, Event{Kind: "capsule.executor.started", EnvelopeDigest: prepared.Envelope.Digest, ExecutionID: prepared.ID})
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
		if err != nil {
			kind = "capsule.executor.failed"
		}
		_ = sink.Emit(ctx, Event{Kind: kind, EnvelopeDigest: prepared.Envelope.Digest, ExecutionID: prepared.ID})
	}
	return response.Result, err
}

func (w HTTPRemoteWorker) Cancel(ctx context.Context, id string) error {
	return w.call(ctx, http.MethodDelete, "/v1/capsules/executions/"+url.PathEscape(id), nil, nil)
}

func (w HTTPRemoteWorker) call(ctx context.Context, method, path string, body any, out any) error {
	endpoint := strings.TrimRight(w.Endpoint, "/")
	if endpoint == "" || !strings.HasPrefix(endpoint, "https://") {
		return fmt.Errorf("capsule executor: remote endpoint must use https")
	}
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
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("capsule executor: remote %s %s returned %s", method, path, response.Status)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("capsule executor: decode remote response: %w", err)
	}
	return nil
}

var _ RemoteWorker = HTTPRemoteWorker{}
