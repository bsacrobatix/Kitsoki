package applicationjob

import (
	"encoding/json"
	"time"
)

const (
	ReceiptSchema       = "kitsoki/application-job-receipt/v1"
	MaxInputBytes       = 256 * 1024
	MaxRuntimeSeconds   = 24 * 60 * 60
	MaxArtifactOutputs  = 16
	MaxIdentityBytes    = 256
	MaxArtifactRefBytes = 512
)

// Bounds are deployment-owned limits for one application-job template.
type Bounds struct {
	MaxInputBytes     int `yaml:"max_input_bytes" json:"max_input_bytes"`
	MaxRuntimeSeconds int `yaml:"max_runtime_seconds" json:"max_runtime_seconds"`
}

// Template is the complete deployment authority for one public template name.
type Template struct {
	ApplicationID   string   `yaml:"application_id" json:"application_id"`
	Event           string   `yaml:"event" json:"event"`
	ArtifactOutputs []string `yaml:"artifact_outputs" json:"artifact_outputs"`
	PrimaryOutput   string   `yaml:"primary_output" json:"primary_output"`
	Bounds          Bounds   `yaml:"bounds" json:"bounds"`
}

// Record is the private mapping from a stable public artifact-job reference to
// the existing Application Event child that performs the work.
type Record struct {
	JobRef              string
	CallerApplicationID string
	TemplateID          string
	TargetApplicationID string
	TargetEvent         string
	ArtifactOutputs     []string
	PrimaryOutput       string
	MaxInputBytes       int
	MaxRuntimeSeconds   int
	TargetRouteID       string
	TargetSessionID     string
	ChildJobID          string
	InputDigest         string
	Artifacts           []string
	PrimaryHandle       string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Receipt is the bounded privacy-safe receipt returned by the public host.
type Receipt struct {
	Schema    string `json:"schema"`
	ID        string `json:"id"`
	JobRef    string `json:"job_ref"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
	Replayed  bool   `json:"replayed,omitempty"`
}

// Result is the complete public application-job projection.
type Result struct {
	JobRef    string   `json:"job_ref"`
	Status    string   `json:"status"`
	Artifacts []string `json:"artifact_handles,omitempty"`
	Primary   string   `json:"primary,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Receipt   Receipt  `json:"receipt"`
}

// DispatchRequest is private input to the injected Application Event adapter.
type DispatchRequest struct {
	JobRef              string
	CallerApplicationID string
	TemplateID          string
	Template            Template
	Input               json.RawMessage
}

// DispatchResult captures only the private routing identities minted by the
// existing Application Event runtime.
type DispatchResult struct {
	RouteID   string
	SessionID string
	ChildID   string
}

// ChildSnapshot is the scheduler projection needed to update the public job.
type ChildSnapshot struct {
	Status string
	Output map[string]any
}
