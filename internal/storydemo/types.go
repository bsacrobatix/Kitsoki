// Package storydemo implements the app-scoped, typed host.demo provider.
package storydemo

import (
	"context"
	"encoding/json"
	"time"

	"kitsoki/internal/applicationcapture"
	"kitsoki/internal/clock"
	"kitsoki/internal/host"
)

const (
	maxCatalogPathBytes = 4096
	maxNodeIDBytes      = 512
	maxAudienceBytes    = 32
	maxClosureNodes     = 256
	maxTasks            = 64
	maxArtifacts        = 64
	maxPayloadBytes     = 256 * 1024
	maxArtifactBytes    = 64 * 1024 * 1024
)

// Scope is the immutable application authority checked before an operation.
type Scope struct {
	AppID string
	Root  string
	Actor string
	Op    string
}

// Authorizer validates app and actor authority before catalog or artifact I/O.
type Authorizer interface {
	Authorize(context.Context, Scope) error
}

// Resolver turns typed graph inputs into server-owned execution declarations.
type Resolver interface {
	Plan(context.Context, string, string, string) (Plan, error)
	Materialization(context.Context, string, string, string, string) (Materialization, error)
	ProjectMockup(context.Context, string, string, string, string) (MockupProjection, error)
}

// Materializer evaluates a server-resolved artifact materialization task.
type Materializer interface {
	Run(context.Context, string, Task) (TaskResult, error)
}

// MockupCreator creates a mockup from a server-owned projected scenario.
type MockupCreator interface {
	Create(context.Context, string, MockupManifest) (ToolResult, error)
}

// Capture records a server-resolved demo manifest.
type Capture interface {
	Record(context.Context, string, Manifest) (ToolResult, error)
}

// Doctor checks a server-resolved demo manifest.
type Doctor interface {
	Check(context.Context, string, Manifest) (DoctorResult, error)
}

// EvidenceStore persists and resolves immutable, app-scoped opaque references.
type EvidenceStore interface {
	Find(context.Context, string, string) (string, json.RawMessage, bool, error)
	Put(context.Context, string, string, json.RawMessage, time.Time) (string, error)
	Resolve(context.Context, string, string) (json.RawMessage, error)
}

// Dependencies are injected when a Story Application runtime is constructed.
type Dependencies struct {
	AppID        string
	Root         string
	Authorizer   Authorizer
	Resolver     Resolver
	Materializer Materializer
	Creator      MockupCreator
	Capture      Capture
	Doctor       Doctor
	Evidence     EvidenceStore
	Clock        clock.Clock
}

// BrokerCapture waits for an attached Story Application surface to execute a
// server-owned action plan and return its privacy-scrubbed rrweb recording.
type BrokerCapture struct {
	Broker applicationcapture.Broker
}

// Plan is the bounded graph projection for a demo node.
type Plan struct {
	CatalogDigest string
	CatalogPath   string
	NodeID        string
	ClosureOrder  []string
	Manifest      Manifest
	Artifacts     []Artifact
}

// Materialization contains only server-resolved tasks and resulting artifacts.
type Materialization struct {
	CatalogDigest string
	NodeID        string
	Phase         string
	Tasks         []Task
	Artifacts     []Artifact
}

// Task is private provider data containing only typed graph/artifact state.
type Task struct {
	ID        string
	Phase     string
	Artifacts []Artifact
}

// TaskResult is the bounded, sanitized result of one materialization task.
type TaskResult struct {
	ID         string `json:"id"`
	OK         bool   `json:"ok"`
	OutputHash string `json:"output_hash,omitempty"`
}

// Artifact names a server-owned file eligible for an opaque artifact handle.
type Artifact struct {
	Kind string
	Path string
}

// Manifest is a server-resolved existing demo manifest.
type Manifest struct {
	Path    string
	Capture *CapturePlan
}

// CapturePlan is private manifest data. Story inputs carry only manifest_ref.
type CapturePlan struct {
	ApplicationID string
	ScenarioRef   string
	ActionIDs     []string
}

// MockupProjection is a deterministic scenario and private creation plan.
type MockupProjection struct {
	CatalogDigest string
	NodeID        string
	Audience      string
	Scenario      json.RawMessage
	Manifest      MockupManifest
}

// MockupManifest contains private server-side inputs for the creator.
type MockupManifest struct {
	Scenario    json.RawMessage
	ScenarioRef string
	ActionIDs   []string
	WorkDir     string
	OutPath     string
}

// ToolResult contains server-owned files emitted by a tool adapter.
type ToolResult struct {
	Primary   Artifact
	Artifacts []Artifact
	Summary   string
}

// DoctorResult is the structured result of the injected doctor adapter.
type DoctorResult struct {
	Report map[string]any
	OK     bool
}

type manifestRecord struct {
	Path   string          `json:"path,omitempty"`
	Mockup *MockupManifest `json:"mockup,omitempty"`
}

type artifactRecord struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type operationReceipt struct {
	Schema      string       `json:"schema"`
	AppID       string       `json:"app_id"`
	Operation   string       `json:"operation"`
	InputDigest string       `json:"input_digest"`
	OK          bool         `json:"ok"`
	Evidence    any          `json:"evidence,omitempty"`
	Artifacts   []string     `json:"artifacts,omitempty"`
	Tasks       []TaskResult `json:"tasks,omitempty"`
	RecordedAt  string       `json:"recorded_at"`
}

func result(data map[string]any) host.Result {
	return host.Result{Data: data}
}
