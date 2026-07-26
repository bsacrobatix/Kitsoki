// Package applicationconformance owns the deterministic, no-LLM scenario
// shared by application adapter tests.
package applicationconformance

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	appplatform "kitsoki/internal/application"
)

//go:embed application-conformance-v1.json
var fixtureFS embed.FS

const FixtureFile = "application-conformance-v1.json"

type Fixture struct {
	Schema        string          `json:"schema"`
	Scenario      string          `json:"scenario"`
	ApplicationID string          `json:"application_id"`
	SessionID     string          `json:"session_id"`
	Page          string          `json:"page"`
	FrameRevision uint64          `json:"frame_revision"`
	Action        ActionFixture   `json:"action"`
	Event         EventFixture    `json:"event"`
	Feedback      FeedbackFixture `json:"feedback"`
	Expected      ExpectedFixture `json:"expected"`
	StoryYAML     string          `json:"story_yaml"`
	Schemas       map[string]any  `json:"schemas"`
	Coverage      []Coverage      `json:"coverage"`
}

type ActionFixture struct {
	ID                 string         `json:"id"`
	Handler            string         `json:"handler"`
	SemanticRef        string         `json:"semantic_ref"`
	HandlerSemanticRef string         `json:"handler_semantic_ref"`
	Input              map[string]any `json:"input"`
}

type EventFixture struct {
	ID    string         `json:"id"`
	Mode  string         `json:"mode"`
	Input map[string]any `json:"input"`
}

type FeedbackFixture struct {
	Ref            string   `json:"ref"`
	Instruction    string   `json:"instruction"`
	Kind           string   `json:"kind"`
	IdempotencyKey string   `json:"idempotency_key"`
	Excluded       []string `json:"excluded"`
}

type ExpectedFixture struct {
	FrameSchema            string `json:"frame_schema"`
	OutcomeSchema          string `json:"outcome_schema"`
	ReceiptSchema          string `json:"receipt_schema"`
	Outcome                string `json:"outcome"`
	ApplicationSemanticRef string `json:"application_semantic_ref"`
	PageSemanticRef        string `json:"page_semantic_ref"`
	FeedbackSchema         string `json:"feedback_schema"`
	FeedbackPlugin         string `json:"feedback_plugin"`
}

type Coverage struct {
	Surface  string `json:"surface"`
	Consumer string `json:"consumer"`
	Marker   string `json:"marker"`
}

type NormalizedOutcome struct {
	Schema        string
	Outcome       string
	ReceiptSchema string
	SemanticRef   string
	Transport     appplatform.Transport
	EventID       string
	EventMode     appplatform.EventMode
	ApplicationID string
	Page          string
	FrameSchema   string
}

func Load() (Fixture, error) {
	raw, err := fixtureFS.ReadFile(FixtureFile)
	if err != nil {
		return Fixture{}, err
	}
	var fixture Fixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		return Fixture{}, fmt.Errorf("application conformance fixture: %w", err)
	}
	return fixture, nil
}

func Normalize(outcome appplatform.OutcomeEnvelope) NormalizedOutcome {
	normalized := NormalizedOutcome{
		Schema: outcome.Schema, Outcome: outcome.Outcome,
		ReceiptSchema: outcome.Receipt.Schema, SemanticRef: outcome.Receipt.SemanticRef,
		Transport: outcome.Receipt.Transport, EventID: outcome.Receipt.EventID,
		EventMode: outcome.Receipt.EventMode,
	}
	if outcome.Frame != nil {
		normalized.ApplicationID = outcome.Frame.ApplicationID
		normalized.Page = outcome.Frame.Page
		normalized.FrameSchema = outcome.Frame.Schema
	}
	return normalized
}

func MaterializeStory(root string) (string, error) {
	fixture, err := Load()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(root, "schemas"), 0o700); err != nil {
		return "", err
	}
	for name, schema := range fixture.Schemas {
		raw, marshalErr := json.Marshal(schema)
		if marshalErr != nil {
			return "", marshalErr
		}
		if writeErr := os.WriteFile(filepath.Join(root, "schemas", name), raw, 0o600); writeErr != nil {
			return "", writeErr
		}
	}
	path := filepath.Join(root, "app.yaml")
	if err := os.WriteFile(path, []byte(fixture.StoryYAML), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
