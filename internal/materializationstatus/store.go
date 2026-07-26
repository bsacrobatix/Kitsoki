// Package materializationstatus persists the privacy-safe, app-scoped
// projection of graph.materialize jobs shared by the producer and Story
// Application host reader.
package materializationstatus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const Schema = "kitsoki/materialization-snapshot/v1"

type Stage struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type Artifact struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Handle string `json:"handle"`
}

type Record struct {
	ApplicationID string     `json:"application_id"`
	JobID         string     `json:"job_id"`
	SessionID     string     `json:"session_id,omitempty"`
	Status        string     `json:"status"`
	Stages        []Stage    `json:"stages"`
	Artifacts     []Artifact `json:"artifacts"`
	ReceiptIDs    []string   `json:"receipt_ids"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Store interface {
	Save(context.Context, Record) (Record, error)
	Get(context.Context, string, string) (Record, bool, error)
	List(context.Context, string, int) ([]Record, error)
	InterruptActive(context.Context, string) (int, error)
}

var (
	ErrInvalid = errors.New("materialization status: invalid record")
	active     = map[string]bool{"running": true, "awaiting_input": true}
	terminal   = map[string]bool{"done": true, "failed": true, "cancelled": true, "interrupted": true}
	stageState = map[string]bool{"waiting": true, "in-progress": true, "complete": true, "failed": true}
)

func Normalize(record Record) (Record, error) {
	record.ApplicationID = strings.TrimSpace(record.ApplicationID)
	record.JobID = strings.TrimSpace(record.JobID)
	record.SessionID = strings.TrimSpace(record.SessionID)
	record.Status = strings.TrimSpace(record.Status)
	if !opaque(record.ApplicationID) || !opaque(record.JobID) ||
		(record.SessionID != "" && !opaque(record.SessionID)) {
		return Record{}, fmt.Errorf("%w: application, job, and session identities must be opaque", ErrInvalid)
	}
	if !active[record.Status] && !terminal[record.Status] {
		return Record{}, fmt.Errorf("%w: unsupported status %q", ErrInvalid, record.Status)
	}
	if len(record.Stages) > 100 || len(record.Artifacts) > 100 || len(record.ReceiptIDs) > 100 {
		return Record{}, fmt.Errorf("%w: projection exceeds item bounds", ErrInvalid)
	}
	stageIDs := map[string]bool{}
	for i := range record.Stages {
		stage := &record.Stages[i]
		stage.ID, stage.Title, stage.Status = strings.TrimSpace(stage.ID), strings.TrimSpace(stage.Title), strings.TrimSpace(stage.Status)
		if !opaque(stage.ID) || stage.Title == "" || len(stage.Title) > 256 || !stageState[stage.Status] || stageIDs[stage.ID] {
			return Record{}, fmt.Errorf("%w: invalid stage %d", ErrInvalid, i)
		}
		stageIDs[stage.ID] = true
	}
	artifactHandles := map[string]bool{}
	for i := range record.Artifacts {
		artifact := &record.Artifacts[i]
		artifact.Kind, artifact.Title, artifact.Handle = strings.TrimSpace(artifact.Kind), strings.TrimSpace(artifact.Title), strings.TrimSpace(artifact.Handle)
		if !opaque(artifact.Kind) || artifact.Title == "" || len(artifact.Title) > 256 ||
			!opaque(artifact.Handle) || artifactHandles[artifact.Handle] {
			return Record{}, fmt.Errorf("%w: invalid artifact %d", ErrInvalid, i)
		}
		artifactHandles[artifact.Handle] = true
	}
	receiptCount := len(record.ReceiptIDs)
	record.ReceiptIDs = cleanOpaque(record.ReceiptIDs)
	if len(record.ReceiptIDs) != receiptCount {
		return Record{}, fmt.Errorf("%w: receipt identities must be unique and opaque", ErrInvalid)
	}
	if terminal[record.Status] && len(record.ReceiptIDs) == 0 {
		record.ReceiptIDs = []string{CanonicalReceiptID(record)}
	}
	record.UpdatedAt = record.UpdatedAt.UTC()
	if record.UpdatedAt.IsZero() {
		return Record{}, fmt.Errorf("%w: updated_at is required", ErrInvalid)
	}
	return record, nil
}

func CanonicalReceiptID(record Record) string {
	type receiptFacts struct {
		Schema        string     `json:"schema"`
		ApplicationID string     `json:"application_id"`
		JobID         string     `json:"job_id"`
		SessionID     string     `json:"session_id,omitempty"`
		Status        string     `json:"status"`
		Stages        []Stage    `json:"stages"`
		Artifacts     []Artifact `json:"artifacts"`
	}
	raw, _ := json.Marshal(receiptFacts{
		Schema: Schema, ApplicationID: record.ApplicationID, JobID: record.JobID,
		SessionID: record.SessionID, Status: record.Status,
		Stages: record.Stages, Artifacts: record.Artifacts,
	})
	sum := sha256.Sum256(raw)
	return "mr_" + hex.EncodeToString(sum[:16])
}

func IsActive(status string) bool   { return active[status] }
func IsTerminal(status string) bool { return terminal[status] }

func Opaque(value string) bool { return opaque(value) }

func opaque(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 || value == "." || value == ".." {
		return false
	}
	lower := strings.ToLower(value)
	return !strings.ContainsAny(value, `/\`) &&
		!strings.Contains(lower, "://") &&
		!strings.Contains(lower, "%2f") &&
		!strings.Contains(lower, "%5c")
}

func cleanOpaque(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !opaque(value) || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
