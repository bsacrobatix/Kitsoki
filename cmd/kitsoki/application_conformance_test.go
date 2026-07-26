package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kitsoki/internal/applicationconformance"
)

// conformance-consumer:cli
const cliConformanceFixture = "application-conformance-v1.json"

func TestApplicationCLIConformanceFixture(t *testing.T) {
	fixture, err := applicationconformance.Load()
	if err != nil {
		t.Fatal(err)
	}
	var requests []struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var rpc struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, rpc)
		w.Header().Set("Content-Type", "application/json")
		if rpc.Method == "runstatus.application.feedback" {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"report":{"schema":"kitsoki.feedback.report.v1","anchor":{"kind":"semantic_element","semantic_element":{"plugin":"kitsoki.application","ref":"demo.action.open"}}},"receipt":{"ref":"feedback-demo-action-open","deduped":false}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"schema":"application-outcome/v1","handler":"demo.open-handler","outcome":"ok","receipt":{"schema":"application-receipt/v1","semantic_ref":"demo.handler.open","transport":"cli","outcome":"ok"}}}`)
	}))
	defer server.Close()

	input, _ := json.Marshal(fixture.Action.Input)
	call := applicationCallCmd()
	var callOut bytes.Buffer
	call.SetOut(&callOut)
	call.SetArgs([]string{
		fixture.Action.Handler, "--url", server.URL, "--session-id", fixture.SessionID,
		"--input", string(input),
	})
	if err := call.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(callOut.String(), fixture.Expected.OutcomeSchema) ||
		requests[0].Method != "runstatus.application.cli_call" ||
		requests[0].Params["transport"] != nil {
		t.Fatalf("CLI call request=%#v output=%s", requests[0], callOut.String())
	}

	feedback := applicationFeedbackCmd()
	var feedbackOut bytes.Buffer
	feedback.SetOut(&feedbackOut)
	feedback.SetArgs([]string{
		fixture.Feedback.Ref, "--url", server.URL, "--session-id", fixture.SessionID,
		"--instruction", fixture.Feedback.Instruction, "--kind", fixture.Feedback.Kind,
		"--idempotency-key", fixture.Feedback.IdempotencyKey,
	})
	if err := feedback.Execute(); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(requests[1])
	for _, excluded := range fixture.Feedback.Excluded {
		if strings.Contains(string(encoded), excluded) || strings.Contains(feedbackOut.String(), excluded) {
			t.Fatalf("CLI feedback leaked %q", excluded)
		}
	}
	if requests[1].Method != "runstatus.application.feedback" ||
		requests[1].Params["ref"] != fixture.Feedback.Ref {
		t.Fatalf("CLI feedback request = %#v", requests[1])
	}
}
