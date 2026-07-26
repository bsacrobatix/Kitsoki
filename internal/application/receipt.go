package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	OutcomeSchema = "application-outcome/v1"
	ReceiptSchema = "application-receipt/v1"
)

type ChildRun struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Outcome string `json:"outcome,omitempty"`
}

type JoinState struct {
	Status    string   `json:"status"`
	Pending   []string `json:"pending,omitempty"`
	Completed []string `json:"completed,omitempty"`
}

type OutcomeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RoutingReceipt struct {
	Requested RoutingMode `json:"requested"`
	Resolved  RoutingMode `json:"resolved"`
}

type BudgetDecision struct {
	Allowed bool   `json:"allowed"`
	Code    string `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type Receipt struct {
	Schema              string         `json:"schema"`
	ID                  string         `json:"id"`
	HandlerID           string         `json:"handler_id"`
	SemanticRef         string         `json:"semantic_ref"`
	SessionID           string         `json:"session_id,omitempty"`
	Actor               string         `json:"actor,omitempty"`
	Effect              EffectClass    `json:"effect"`
	Routing             RoutingReceipt `json:"routing"`
	Budget              BudgetDecision `json:"budget"`
	IdempotencyKey      string         `json:"idempotency_key,omitempty"`
	InputDigest         string         `json:"input_digest"`
	OutputDigest        string         `json:"output_digest"`
	Transport           Transport      `json:"transport"`
	EventID             string         `json:"event_id,omitempty"`
	EventMode           EventMode      `json:"event_mode,omitempty"`
	FrameRevision       uint64         `json:"frame_revision,omitempty"`
	Outcome             string         `json:"outcome"`
	Replayed            bool           `json:"replayed,omitempty"`
	ReplayOf            string         `json:"replay_of,omitempty"`
	SelectedImplementor string         `json:"selected_implementor,omitempty"`
}

type OutcomeEnvelope struct {
	Schema              string          `json:"schema"`
	Handler             string          `json:"handler"`
	Outcome             string          `json:"outcome"`
	Output              json.RawMessage `json:"output,omitempty"`
	Frame               *Frame          `json:"frame,omitempty"`
	Children            []ChildRun      `json:"children,omitempty"`
	Join                *JoinState      `json:"join,omitempty"`
	Error               *OutcomeError   `json:"error,omitempty"`
	Receipt             Receipt         `json:"receipt"`
	SelectedImplementor string          `json:"selected_implementor,omitempty"`
}

func NormalizeJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("application: invalid JSON: %w", err)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("application: normalize JSON: %w", err)
	}
	return normalized, nil
}

func DigestJSON(raw json.RawMessage) (string, error) {
	normalized, err := NormalizeJSON(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(normalized)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func FinalizeReceipt(receipt Receipt) (Receipt, error) {
	receipt.Schema = ReceiptSchema
	receipt.ID = ""
	raw, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, fmt.Errorf("application: encode receipt: %w", err)
	}
	sum := sha256.Sum256(raw)
	receipt.ID = "ar_" + hex.EncodeToString(sum[:16])
	return receipt, nil
}
