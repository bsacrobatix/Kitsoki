// local_llm.go implements the local-model transport: an Oracle that talks to a
// llama.cpp server over its OpenAI-compatible /v1/chat/completions HTTP API.
//
// Why this transport exists: routing and schema-bounded `decide` do not need
// Claude's reasoning — they need cheap, offline, schema-shaped JSON. A small
// local model behind llama.cpp delivers that, and (for schemas inside the
// translatable grammar subset, see grammar_subset.go) can be grammar-constrained
// so its first decode is strongly biased toward valid JSON, collapsing the retry
// loop. This is additive and opt-in; oracle.claude stays the default.
//
// Request shape (one HTTP POST to base + "/v1/chat/completions"):
//
//	{
//	  "model": "<model>",
//	  "messages": [{"role": "user", "content": "<rendered prompt>"}],
//	  "response_format": {                       // omitted unless grammar applies
//	    "type": "json_schema",
//	    "json_schema": {"name": "submission", "strict": true, "schema": <SchemaJSON>}
//	  }
//	}
//
// Success response (OpenAI chat completion):
//
//	{
//	  "choices": [{"message": {"content": "<json-encoded submission>"}}],
//	  "usage":   {"prompt_tokens": N, "completion_tokens": M}
//	}
//
// Grammar is best-effort: response_format is attached only when grammar is
// enabled AND the schema is inside the translatable subset. llama.cpp fails open
// (decodes unconstrained on a translation error, still returns 200), so this
// transport does NOT validate — ValidateSubmission (validate.go) remains the
// sole authority. Meta["grammar"] records whether grammar was actually requested
// so the trace tells the truth about what enforced the shape.
//
// Base URL resolution: when endpoint != "" the transport talks to it directly
// and never spawns or fetches anything (bring-your-own-server / test mode);
// otherwise it lazily ensures a managed sidecar (server package, step 2).
//
// Deadline: AskRequest.Deadline cancels the request context; the caller's ctx
// cancelling first propagates instead. Close releases idle connections and, in
// managed mode, terminates the sidecar.
//
// Non-goals: no system-prompt handling (AskRequest carries only the rendered
// PromptText; any system framing is folded in upstream), no streaming, no
// multi-turn — one prompt, one schema-shaped reply.

package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// chatMessage is one entry in the OpenAI chat `messages` array.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// jsonSchemaSpec is the `json_schema` object inside an OpenAI response_format.
type jsonSchemaSpec struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

// responseFormat is the OpenAI `response_format` directive. Only the
// json_schema variant is used here; it is omitted entirely (pointer nil) when
// grammar does not apply.
type responseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *jsonSchemaSpec `json:"json_schema,omitempty"`
}

// chatRequest is the POST body for /v1/chat/completions.
type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

// chatChoice is one element of the response `choices` array.
type chatChoice struct {
	Message chatMessage `json:"message"`
}

// chatUsage mirrors the OpenAI `usage` block. Surfaced verbatim into Meta so the
// trace records token counts without the state machine interpreting them.
type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// chatResponse is the OpenAI chat-completion response envelope.
type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

// localSidecar is the subset of the server.Sidecar lifecycle local_llm depends
// on. Declaring it as an interface here lets tests inject a fake without pulling
// in the real fetch/spawn machinery, and lets the constructor stay decoupled
// from the server package's concrete type until step 2 wires it.
type localSidecar interface {
	// EnsureRunning lazily resolves (and in managed mode starts) the backend,
	// returning its base URL. It honours ctx for the start/health wait.
	EnsureRunning(ctx context.Context) (baseURL string, err error)
	// Close terminates a managed backend; it is a no-op in endpoint mode.
	Close() error
}

// LocalLLMOracle implements Oracle against a llama.cpp OpenAI HTTP server. The
// zero value is not usable — construct it with NewLocalLLM. It is safe for
// concurrent use: the http.Client pools connections and Ask holds no per-call
// state on the receiver.
type LocalLLMOracle struct {
	model    string
	grammar  bool
	endpoint string
	sidecar  localSidecar
	client   *http.Client
}

// NewLocalLLM creates a LocalLLMOracle.
//
// model is the GGUF model id passed to llama.cpp (and used to provision weights
// in managed mode); port/serverBin/env configure a managed sidecar; grammar
// requests best-effort grammar-constrained decoding for in-subset schemas;
// endpoint, when non-empty, points at an already-running server and disables all
// fetching/spawning. The sidecar is created here but only contacted lazily on
// the first managed Ask, mirroring NewMCPHTTP's eager-client / lazy-work split.
func NewLocalLLM(model string, port int, serverBin string, grammar bool, endpoint string, env map[string]string) *LocalLLMOracle {
	return &LocalLLMOracle{
		model:    model,
		grammar:  grammar,
		endpoint: endpoint,
		sidecar:  newSidecar(model, port, serverBin, endpoint, env),
		client: &http.Client{
			// No global timeout; the per-request deadline is enforced via the
			// http.Request context derived from AskRequest.Deadline.
			Transport: &http.Transport{},
		},
	}
}

// Ask sends the rendered prompt to the local model and returns its
// schema-shaped reply. It does not validate the Submission — ValidateSubmission
// is the sole authority — but it does record in Meta whether grammar was
// requested so downstream knows what (if anything) constrained the decode.
func (o *LocalLLMOracle) Ask(ctx context.Context, req AskRequest) (AskResponse, error) {
	// Honour an already-cancelled caller context up front so we never spawn a
	// sidecar or open a connection for a turn that is already over.
	if ctx.Err() != nil {
		return AskResponse{}, &AskError{
			Kind:       "deadline_exceeded",
			Underlying: ctx.Err(),
			Detail:     fmt.Sprintf("local_llm oracle: context already done: %v", ctx.Err()),
		}
	}

	// Build deadline context: whichever fires first — caller ctx or req.Deadline.
	callCtx := ctx
	if !req.Deadline.IsZero() {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithDeadline(ctx, req.Deadline)
		defer cancel()
	}

	// Resolve the base URL. Endpoint mode talks directly to a running server;
	// managed mode lazily ensures the sidecar (download/spawn/health) here.
	base := o.endpoint
	if base == "" {
		var err error
		base, err = o.sidecar.EnsureRunning(callCtx)
		if err != nil {
			return AskResponse{}, o.translateContextErr(callCtx, ctx, err,
				fmt.Sprintf("local_llm oracle: ensure backend: %v", err))
		}
	}

	// Decide whether to grammar-constrain. Only when grammar is enabled, a
	// schema is present, and that schema is inside the translatable subset.
	grammarApplied := false
	var rf *responseFormat
	if o.grammar && req.SchemaJSON != nil && GrammarSubsetOK(req.SchemaJSON) == nil {
		grammarApplied = true
		rf = &responseFormat{
			Type: "json_schema",
			JSONSchema: &jsonSchemaSpec{
				Name:   "submission",
				Strict: true,
				Schema: req.SchemaJSON,
			},
		}
	}

	bodyBytes, err := json.Marshal(chatRequest{
		Model:          o.model,
		Messages:       []chatMessage{{Role: "user", Content: req.PromptText}},
		ResponseFormat: rf,
	})
	if err != nil {
		return AskResponse{}, &AskError{
			Kind:       "transport_error",
			Underlying: err,
			Detail:     fmt.Sprintf("local_llm oracle: marshal chat request: %v", err),
		}
	}

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return AskResponse{}, &AskError{
			Kind:       "transport_error",
			Underlying: err,
			Detail:     fmt.Sprintf("local_llm oracle: build http request: %v", err),
		}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := o.client.Do(httpReq)
	if err != nil {
		return AskResponse{}, o.translateContextErr(callCtx, ctx, err,
			fmt.Sprintf("local_llm oracle: http do: %v", err))
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, MaxHTTPResponseSize))
	if err != nil {
		return AskResponse{}, &AskError{
			Kind:       "transport_error",
			Underlying: err,
			Detail:     fmt.Sprintf("local_llm oracle: read response body: %v", err),
		}
	}

	if httpResp.StatusCode >= 400 {
		return AskResponse{}, &AskError{
			Kind:   "transport_error",
			Detail: fmt.Sprintf("local_llm oracle: http %d: %s", httpResp.StatusCode, truncateBytes(respBody, ErrorDetailTruncateBytes)),
		}
	}

	var cr chatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return AskResponse{}, &AskError{
			Kind:       "transport_error",
			Underlying: err,
			Detail:     fmt.Sprintf("local_llm oracle: unmarshal chat response: %v (raw: %q)", err, truncateBytes(respBody, ErrorDetailTruncateBytes)),
		}
	}

	if len(cr.Choices) == 0 {
		return AskResponse{}, &AskError{
			Kind:   "transport_error",
			Detail: fmt.Sprintf("local_llm oracle: response has no choices (raw: %q)", truncateBytes(respBody, ErrorDetailTruncateBytes)),
		}
	}
	content := cr.Choices[0].Message.Content
	if content == "" {
		return AskResponse{}, &AskError{
			Kind:   "transport_error",
			Detail: fmt.Sprintf("local_llm oracle: first choice has empty content (raw: %q)", truncateBytes(respBody, ErrorDetailTruncateBytes)),
		}
	}

	return AskResponse{
		Submission: json.RawMessage(content),
		Meta: map[string]any{
			"model":             o.model,
			"prompt_tokens":     cr.Usage.PromptTokens,
			"completion_tokens": cr.Usage.CompletionTokens,
			"grammar":           grammarApplied,
		},
	}, nil
}

// translateContextErr maps a transport-level failure to a typed AskError,
// classifying it as deadline_exceeded when either context was cancelled or timed
// out (so a slow model or a cancelled turn surfaces correctly) and
// transport_error otherwise.
func (o *LocalLLMOracle) translateContextErr(callCtx, ctx context.Context, err error, detail string) *AskError {
	if callCtx.Err() == context.DeadlineExceeded || ctx.Err() == context.DeadlineExceeded ||
		callCtx.Err() == context.Canceled || ctx.Err() == context.Canceled {
		return &AskError{
			Kind:       "deadline_exceeded",
			Underlying: err,
			Detail:     detail,
		}
	}
	return &AskError{
		Kind:       "transport_error",
		Underlying: err,
		Detail:     detail,
	}
}

// Close releases idle HTTP connections and, in managed mode, terminates the
// sidecar. In endpoint mode the sidecar Close is a no-op (we did not start it).
func (o *LocalLLMOracle) Close() error {
	if t, ok := o.client.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	if o.endpoint == "" && o.sidecar != nil {
		return o.sidecar.Close()
	}
	return nil
}
