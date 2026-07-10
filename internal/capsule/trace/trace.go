// Package trace defines the shared Capsule trace document and event schema.
package trace

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const DocumentSchema = "capsule-ci-trace/v1"

const (
	KindWorkspaceMaterializing = "capsule.workspace.materializing"
	KindWorkspaceReady         = "capsule.workspace.ready"
	KindWorkspaceFailed        = "capsule.workspace.failed"
	KindWorkspaceChanged       = "capsule.workspace.changed"
	KindWorkspaceCommitted     = "capsule.workspace.committed"
	KindWorkspaceIntegrated    = "capsule.workspace.integrated"
	KindWorkspaceClosed        = "capsule.workspace.closed"

	KindEnvironmentResolved = "capsule.environment.resolved"

	KindCIStarted = "capsule.ci.started"
	KindCIVerdict = "capsule.ci.verdict"

	KindSyncPlanned    = "capsule.sync.planned"
	KindSyncApplied    = "capsule.sync.applied"
	KindSyncStale      = "capsule.sync.stale"
	KindSyncConflicted = "capsule.sync.conflicted"
	KindSyncAborted    = "capsule.sync.aborted"
)

type Document struct {
	Schema string  `json:"schema"`
	Events []Event `json:"events"`
}

type Event struct {
	Kind              string         `json:"kind"`
	At                time.Time      `json:"at"`
	JobID             string         `json:"job_id,omitempty"`
	EnvelopeDigest    string         `json:"envelope_digest,omitempty"`
	Outcome           string         `json:"outcome,omitempty"`
	InstanceID        string         `json:"instance_id,omitempty"`
	Generation        uint64         `json:"generation,omitempty"`
	PlanDigest        string         `json:"plan_digest,omitempty"`
	Operation         string         `json:"operation,omitempty"`
	Class             string         `json:"class,omitempty"`
	TargetRef         string         `json:"target_ref,omitempty"`
	Candidate         string         `json:"candidate,omitempty"`
	OldTarget         string         `json:"old_target,omitempty"`
	NewTarget         string         `json:"new_target,omitempty"`
	ContinuationToken string         `json:"continuation_token,omitempty"`
	Error             string         `json:"error,omitempty"`
	Fields            map[string]any `json:"fields,omitempty"`
}

func NewDocument(events ...Event) Document {
	out := Document{Schema: DocumentSchema, Events: make([]Event, 0, len(events))}
	for _, event := range events {
		if event.At.IsZero() {
			event.At = time.Now().UTC()
		} else {
			event.At = event.At.UTC()
		}
		out.Events = append(out.Events, event)
	}
	return out
}

func MarshalDocument(doc Document) ([]byte, error) {
	if err := ValidateDocument(doc); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

func ValidateDocument(doc Document) error {
	if doc.Schema != DocumentSchema {
		return fmt.Errorf("capsule trace: unsupported schema %q", doc.Schema)
	}
	for i, event := range doc.Events {
		if err := ValidateEvent(event); err != nil {
			return fmt.Errorf("capsule trace: event %d: %w", i, err)
		}
	}
	return nil
}

func ValidateEvent(event Event) error {
	if strings.TrimSpace(event.Kind) == "" {
		return fmt.Errorf("kind is required")
	}
	switch event.Kind {
	case KindCIStarted, KindCIVerdict:
		if event.JobID == "" || event.EnvelopeDigest == "" {
			return fmt.Errorf("%s requires job_id and envelope_digest", event.Kind)
		}
	case KindSyncPlanned, KindSyncApplied, KindSyncStale, KindSyncConflicted, KindSyncAborted:
		if event.PlanDigest == "" || event.Operation == "" || event.TargetRef == "" {
			return fmt.Errorf("%s requires plan_digest, operation, and target_ref", event.Kind)
		}
	case KindWorkspaceMaterializing, KindWorkspaceReady, KindWorkspaceFailed, KindWorkspaceChanged, KindWorkspaceCommitted, KindWorkspaceIntegrated, KindWorkspaceClosed:
		if event.InstanceID == "" {
			return fmt.Errorf("%s requires instance_id", event.Kind)
		}
	case KindEnvironmentResolved:
		// Environment resolution may be recorded before a job exists; Fields carry
		// definition and lock digests.
	default:
		return fmt.Errorf("unknown kind %q", event.Kind)
	}
	return nil
}
