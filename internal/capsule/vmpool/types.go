// Package vmpool manages a bounded pool of ephemeral cloud VM workers
// (DigitalOcean droplets; per-second billing) for remote Capsule execution.
// One VM serves one job: acquired from a base-image snapshot, health-checked,
// dispatched, and destroyed. The lifecycle guards — provisioning/activity
// timeouts, startup recovery, and tag-based orphan reconciliation — are
// ported from rumbledunk's vmworkerpool; the durable pool state and slot
// gating follow kitsoki's queue-store pattern instead of rumbledunk's
// in-process mutex queue.
package vmpool

import (
	"context"
	"time"
)

// Status is the worker lifecycle. Transitions:
//
//	creating → provisioning → ready → running → destroying → destroyed
//
// with terminal "failed" reachable from any non-terminal state.
type Status string

const (
	StatusCreating     Status = "creating"     // instance requested from the cloud
	StatusProvisioning Status = "provisioning" // instance active, worker boot in progress
	StatusReady        Status = "ready"        // worker service health-checked, awaiting dispatch
	StatusRunning      Status = "running"      // job dispatched to the worker
	StatusDestroying   Status = "destroying"   // destroy requested
	StatusDestroyed    Status = "destroyed"    // terminal
	StatusFailed       Status = "failed"       // terminal
)

func (s Status) Terminal() bool { return s == StatusDestroyed || s == StatusFailed }

// Worker is the durable pool record for one ephemeral VM.
type Worker struct {
	ID             string    `json:"id"`
	JobID          string    `json:"job_id"`
	InstanceID     string    `json:"instance_id,omitempty"`
	InstanceName   string    `json:"instance_name"`
	Status         Status    `json:"status"`
	PublicIP       string    `json:"public_ip,omitempty"`
	PrivateIP      string    `json:"private_ip,omitempty"`
	Image          string    `json:"image"`
	CreatedAt      time.Time `json:"created_at"`
	ReadyAt        time.Time `json:"ready_at,omitzero"`
	LastActivityAt time.Time `json:"last_activity_at,omitzero"`
	TerminalAt     time.Time `json:"terminal_at,omitzero"`
	Error          string    `json:"error,omitempty"`
	// Preserved marks a failed worker whose instance was deliberately kept
	// running for post-mortem; Reconcile treats it as known, not orphaned,
	// until an explicit Release.
	Preserved bool `json:"preserved,omitempty"`
}

// Instance is the provider-neutral view of a cloud VM.
type Instance struct {
	ID        string
	Name      string
	Status    string // provider status: "new", "active", "off", ...
	PublicIP  string
	PrivateIP string
	CreatedAt time.Time
	Tags      []string
}

// CreateParams describes one VM to provision.
type CreateParams struct {
	Name      string
	Region    string
	Size      string
	Image     string // numeric image/snapshot ID, snapshot name, or distribution slug
	VPCUUID   string
	UserData  string
	SSHKeyIDs []string
	Tags      []string
}

// Provisioner is the cloud-provider seam. The production implementation is
// DigitalOcean via godo; tests use the Fake. Implementations must be safe for
// concurrent use.
type Provisioner interface {
	Create(ctx context.Context, params CreateParams) (Instance, error)
	// Destroy is idempotent: destroying an unknown instance is not an error.
	Destroy(ctx context.Context, instanceID string) error
	// Get reports found=false (with nil error) for unknown instances.
	Get(ctx context.Context, instanceID string) (Instance, bool, error)
	ListByTag(ctx context.Context, tag string) ([]Instance, error)
	// Snapshot powers the base-image flow: snapshot the instance and block
	// until the image is available, returning the new image ID.
	Snapshot(ctx context.Context, instanceID, name string) (string, error)
	// ResolveImage turns a snapshot name or ID into a concrete image ID;
	// distribution slugs pass through unchanged.
	ResolveImage(ctx context.Context, ref string) (string, error)
}

// Config bounds the pool. Defaults reflect the current deployment decisions:
// region sgp1 (co-located with the kitsoki-test Spaces bucket) and 16 GB
// workers to start.
type Config struct {
	Tag              string        `yaml:"tag" json:"tag"`
	NamePrefix       string        `yaml:"name_prefix" json:"name_prefix"`
	MaxConcurrent    int           `yaml:"max_concurrent" json:"max_concurrent"`
	Region           string        `yaml:"region" json:"region"`
	Size             string        `yaml:"size" json:"size"`
	Image            string        `yaml:"image" json:"image"`
	VPCUUID          string        `yaml:"vpc_uuid,omitempty" json:"vpc_uuid,omitempty"`
	SSHKeyIDs        []string      `yaml:"ssh_key_ids,omitempty" json:"ssh_key_ids,omitempty"`
	ProvisionTimeout time.Duration `yaml:"provision_timeout" json:"provision_timeout"`
	ActivityTimeout  time.Duration `yaml:"activity_timeout" json:"activity_timeout"`
	MaxLifetime      time.Duration `yaml:"max_lifetime" json:"max_lifetime"`
	// PreserveFailedTTL bounds how long a PreserveFailed worker's instance
	// survives before Reconcile treats it as reapable instead of protected.
	// Without this bound a preserved instance is terminal (Poll never
	// revisits it) and Reconcile's orphan sweep otherwise treats "known,
	// preserved" as permanent, so it would bill until a human ran `vmpool
	// release` by hand. Only meaningful when the pool's PreserveFailed is
	// set; ignored otherwise.
	PreserveFailedTTL time.Duration `yaml:"preserve_failed_ttl" json:"preserve_failed_ttl"`
}

const (
	DefaultTag               = "kitsoki-worker"
	DefaultNamePrefix        = "kitsoki-worker-"
	DefaultMaxConcurrent     = 5
	DefaultRegion            = "sgp1"
	DefaultSize              = "s-4vcpu-16gb"
	DefaultProvisionTimeout  = 15 * time.Minute
	DefaultActivityTimeout   = 10 * time.Minute
	DefaultMaxLifetime       = 2 * time.Hour
	DefaultPreserveFailedTTL = 4 * time.Hour
)

// WithDefaults fills zero fields with the deployment defaults above.
func (c Config) WithDefaults() Config {
	if c.Tag == "" {
		c.Tag = DefaultTag
	}
	if c.NamePrefix == "" {
		c.NamePrefix = DefaultNamePrefix
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = DefaultMaxConcurrent
	}
	if c.Region == "" {
		c.Region = DefaultRegion
	}
	if c.Size == "" {
		c.Size = DefaultSize
	}
	if c.ProvisionTimeout <= 0 {
		c.ProvisionTimeout = DefaultProvisionTimeout
	}
	if c.ActivityTimeout <= 0 {
		c.ActivityTimeout = DefaultActivityTimeout
	}
	if c.MaxLifetime <= 0 {
		c.MaxLifetime = DefaultMaxLifetime
	}
	if c.PreserveFailedTTL <= 0 {
		c.PreserveFailedTTL = DefaultPreserveFailedTTL
	}
	return c
}
