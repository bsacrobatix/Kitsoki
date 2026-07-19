package runtime

import (
	"context"
	"errors"
	"time"

	"kitsoki/internal/capsule/control"
)

const Schema = "capsule-runtime/v1"

type Definition struct {
	Schema       string                `yaml:"schema" json:"schema"`
	Commands     map[string]Command    `yaml:"commands" json:"commands"`
	Services     map[string]Service    `yaml:"services" json:"services"`
	Dependencies map[string]Dependency `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
	Profiles     map[string]Profile    `yaml:"profiles" json:"profiles"`
	Digest       string                `yaml:"-" json:"digest"`
}

type Command struct {
	Argv []string `yaml:"argv" json:"argv"`
}
type Service struct {
	Command    string          `yaml:"command" json:"command"`
	WorkingDir string          `yaml:"working_dir,omitempty" json:"working_dir,omitempty"`
	Ports      map[string]Port `yaml:"ports,omitempty" json:"ports,omitempty"`
	Health     *Health         `yaml:"health,omitempty" json:"health,omitempty"`
	DependsOn  []string        `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
}
type Port struct {
	Protocol string `yaml:"protocol" json:"protocol"`
	Env      string `yaml:"env" json:"env"`
	Exposure string `yaml:"exposure" json:"exposure"`
}
type Health struct {
	Port        string        `yaml:"port" json:"port"`
	Path        string        `yaml:"path,omitempty" json:"path,omitempty"`
	Timeout     time.Duration `yaml:"-" json:"timeout"`
	TimeoutText string        `yaml:"timeout" json:"-"`
}
type Dependency struct {
	Kind     string `yaml:"kind" json:"kind"`
	Snapshot string `yaml:"snapshot,omitempty" json:"snapshot,omitempty"`
}
type Profile struct {
	Provider  string `yaml:"provider" json:"provider"`
	Placement string `yaml:"placement,omitempty" json:"placement,omitempty"`
}

type Purpose string

const (
	PurposeChange  Purpose = "change"
	PurposeWave    Purpose = "wave"
	PurposeRelease Purpose = "release"
)

type Workspace struct {
	Handle                       control.Handle
	Owner, Path, SourceRef, Head string
}
type WorkspaceResolver interface {
	Resolve(context.Context, control.Handle, string) (Workspace, error)
}

// EndpointBroker is supplied by K5. The runtime only speaks logical endpoint
// roles and never selects numeric ports itself.
type EndpointBroker interface {
	Allocate(context.Context, EndpointRequest) (EndpointLease, error)
	Release(context.Context, EndpointLease) error
}
type EndpointRequest struct {
	RuntimeID, Owner, Service, Role, Protocol, Exposure, DefinitionDigest, SourceSHA string
	Generation                                                                       uint64
}
type EndpointLease struct {
	ID, Address, URL                          string
	Port                                      int
	RuntimeID, Owner, Service, Role, Exposure string
	Generation                                uint64
	ExpiresAt                                 time.Time
}

type ProcessLauncher interface {
	Start(context.Context, ProcessSpec) (Process, error)
	Process(string) (Process, bool)
}
type ProcessSpec struct {
	RuntimeID, Owner, Service, Directory string
	Generation                           uint64
	Argv                                 []string
	Env                                  map[string]string
}
type Process interface {
	ID() string
	Stop(context.Context) error
	Logs(context.Context) ([]byte, error)
}

// ListenerInspector must verify that the recorded process group owns exactly
// the brokered endpoint. A local host implementation is allowed to report its
// weaker serialized-release boundary in Attestation.Detail.
type ListenerInspector interface {
	Attest(context.Context, ListenerRequest) (Attestation, error)
}
type ListenerRequest struct {
	ProcessID string
	Endpoint  EndpointLease
}
type Attestation struct {
	ListenerPID string `json:"listener_pid"`
	Detail      string `json:"detail,omitempty"`
	Verified    bool   `json:"verified"`
}
type HealthChecker interface {
	Check(context.Context, HealthRequest) error
}
type HealthRequest struct {
	Service  string
	Endpoint EndpointLease
	Health   Health
}
type Clock interface{ Now() time.Time }

type RecordStore interface {
	Create(context.Context, Record) error
	Get(context.Context, string) (Record, error)
	List(context.Context) ([]Record, error)
	Update(context.Context, string, func(*Record) error) (Record, error)
}
type State string

const (
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateFailed   State = "failed"
	StateStopped  State = "stopped"
)

type Record struct {
	ID, Owner, SourceSHA, SourceRef, SourceManifestDigest, DefinitionDigest, Profile, Provider string
	Generation                                                                                 uint64
	Purpose                                                                                    Purpose
	State                                                                                      State
	Workspace                                                                                  control.Handle
	Services                                                                                   []ServiceReceipt
	StartedAt, StoppedAt, ExpiresAt                                                            time.Time
	Failure                                                                                    string
}

// SourceManifestDigest identifies the immutable source facts a runtime was
// materialized from. It is intentionally distinct from a mutable source ref.
func SourceManifestDigest(sourceRef, sourceSHA string) string {
	return digestSourceManifest(sourceRef, sourceSHA)
}

type ServiceReceipt struct {
	Name, ProcessID string
	Endpoints       []EndpointLease
	HealthPassed    bool
	Attestations    []Attestation
}
type Receipt struct {
	Schema string `json:"schema"`
	Record Record `json:"record"`
}

var (
	ErrNotFound            = errors.New("capsule runtime: not found")
	ErrSourceMismatch      = errors.New("capsule runtime: source SHA mismatch")
	ErrUnsupportedProvider = errors.New("capsule runtime: unsupported provider")
	ErrListenerPolicy      = errors.New("capsule runtime: listener policy violation")
)
