// Package workerregistry unifies Kitsoki's two independently evolved
// remote-worker systems — internal/daemonfederation's observe-only
// daemon_federation.workers[] (health/job polling) and internal/capsule/ci's
// dispatch-only remotes: (credentialed CI executor targets) — behind one
// operator-facing registry.
//
// Design decision (documented per standing-autonomy proposal §9 "Federation"
// ask #4): daemonfederation.Config and capsule/ci.Config are NOT modified and
// keep parsing/validating/threading through their existing call sites exactly
// as before — daemon_federation.workers[] stays the source of truth for the
// live health-polling Pool, and capsule ci remotes: stays the source of truth
// for per-project, per-pipeline executor credentials. Forcing those two into a
// single shared struct would be the more invasive path: ci.Remote is
// project-scoped (lives in a project's checked-in .kitsoki/ci.yaml, keyed by
// executor name, https-only, no health concept) while daemonfederation.Worker
// is machine-scoped (lives in the operator's gitignored .kitsoki.local.yaml,
// loopback-tunnel-aware, health-polled). Unifying their Go types would blur
// that scope boundary for no behavioral gain.
//
// Instead this package defines the canonical, richer Entry shape — the
// superset the proposal describes (identity, endpoint/tunnel, credential env,
// advertised Capabilities, placement class, enabled bit) — and teaches it to
// read two things for its own listing/CLI purposes:
//
//  1. The new canonical `workers:` top-level block in .kitsoki.local.yaml
//     (preferred going forward; also mirrored onto webconfig.WebConfig.Workers
//     for in-process consumers).
//  2. As a back-compat fallback, when `workers:` is absent, the existing
//     `daemon_federation.workers[]` block, translated into Entry values. Those
//     translated entries have empty Capabilities (daemon federation config
//     never declared isolation/network capabilities) and Enabled defaults to
//     true (daemon federation has no enabled bit — every configured worker is
//     implicitly usable); this is documented, not silently guessed at by
//     placement policy, which treats empty Capabilities as satisfying no
//     worker_classes restriction (see internal/host/agent_launch_policy.go).
//
// capsule ci remotes: are project-scoped and are not folded into this
// machine-wide registry automatically; they keep working completely
// unmodified. A future iteration may add an explicit, opt-in translation.
package workerregistry

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/daemonfederation"
)

var validPlacements = map[string]bool{
	daemonfederation.PlacementThin:        true,
	daemonfederation.PlacementWorkstation: true,
	daemonfederation.PlacementLocalModel:  true,
}

// Capabilities is the trimmed, config-declared subset of
// internal/capsule/executor.Capabilities that makes sense to advertise ahead
// of a dispatch: placements this worker accepts work for, its isolation
// strength, and permitted network profiles. The runtime-only fields on
// executor.Capabilities (ID, EnvironmentRefs, Cancellable) are populated at
// prepare time by the executor package itself, not declared here.
type Capabilities struct {
	Placements []string `yaml:"placements,omitempty" json:"placements,omitempty"`
	Isolation  string   `yaml:"isolation,omitempty" json:"isolation,omitempty"`
	Networks   []string `yaml:"networks,omitempty" json:"networks,omitempty"`
}

// Entry is one canonical worker registry record. It unifies the identity and
// tunnel shape of daemonfederation.Worker with the credential convention of
// capsule/ci.Remote, plus the placement-policy-facing Capabilities contract
// and an operator enabled bit. Entries live only in .kitsoki.local.yaml,
// never the checked-in .kitsoki.yaml, because Endpoint/Tunnel/CredentialEnv
// are machine-local or secret-bearing.
type Entry struct {
	ID            string                   `yaml:"id" json:"id"`
	Label         string                   `yaml:"label" json:"label"`
	Placement     string                   `yaml:"placement" json:"placement"`
	Endpoint      string                   `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Tunnel        *daemonfederation.Tunnel `yaml:"tunnel,omitempty" json:"-"`
	CredentialEnv string                   `yaml:"credential_env,omitempty" json:"-"`
	Capabilities  Capabilities             `yaml:"capabilities,omitempty" json:"capabilities"`
	Enabled       bool                     `yaml:"enabled" json:"enabled"`

	// Legacy is set by back-compat translation from daemon_federation.workers[]
	// (see package doc). It is never present for entries read from the
	// canonical workers: block, and it is never serialized.
	Legacy bool `yaml:"-" json:"-"`
}

// Config is the top-level `workers:` block shape.
type Config struct {
	Workers []Entry `yaml:"workers,omitempty"`
}

// Registry is the resolved, validated set of worker entries plus provenance
// of where they were read from, for CLI/doc messaging.
type Registry struct {
	Entries []Entry
	// Source is "workers" when the canonical workers: block was present and
	// non-empty, or "daemon_federation" when entries were back-compat
	// translated from daemon_federation.workers[].
	Source string
}

// Find returns the entry with the given id, if any.
func (r Registry) Find(id string) (Entry, bool) {
	for _, e := range r.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Validate normalizes and validates a Config in place, mirroring
// daemonfederation.Config.Validate's rules (unique lowercase slug ids, valid
// placement, absolute http(s) endpoint when set) plus the new capabilities
// and enabled fields. configPath is used only for tunnel path resolution
// error messages consistent with daemonfederation.
func (c *Config) Validate(configPath string) error {
	seenIDs := map[string]bool{}
	for i := range c.Workers {
		w := &c.Workers[i]
		w.ID = strings.TrimSpace(w.ID)
		w.Label = strings.TrimSpace(w.Label)
		if !daemonfederationWorkerID(w.ID) || w.ID == "local" || seenIDs[w.ID] {
			return fmt.Errorf("workers[%d]: id must be a unique lowercase slug other than local", i)
		}
		seenIDs[w.ID] = true
		if w.Label == "" {
			return fmt.Errorf("workers[%d]: label is required", i)
		}
		if !validPlacements[w.Placement] {
			return fmt.Errorf("workers[%d]: placement must be thin, workstation, or local-model", i)
		}
		if w.Endpoint != "" {
			u, err := url.Parse(w.Endpoint)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("workers[%d]: endpoint must be absolute http(s) without credentials", i)
			}
		}
		if w.CredentialEnv != "" && !validEnvName(w.CredentialEnv) {
			return fmt.Errorf("workers[%d]: invalid credential_env", i)
		}
	}
	return nil
}

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func daemonfederationWorkerID(id string) bool {
	if id == "" {
		return false
	}
	if id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return len(id) <= 63
}

// fileShape is the minimal YAML surface Load reads from each config file: the
// canonical workers: block plus the legacy daemon_federation.workers[] block,
// read independently of webconfig.WebConfig to avoid an import cycle
// (webconfig imports this package so cfg.Workers is validated and merged the
// same way cfg.DaemonFederation already is).
type fileShape struct {
	Workers          []Entry                 `yaml:"workers"`
	DaemonFederation daemonfederation.Config `yaml:"daemon_federation"`
}

func readFile(path string) (fileShape, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileShape{}, false, nil
		}
		return fileShape{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var shape fileShape
	if err := yaml.Unmarshal(raw, &shape); err != nil {
		return fileShape{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return shape, true, nil
}

// Load reads the canonical registry from a base (checked-in) config path and
// a local (gitignored, machine-specific) config path, local-wins, mirroring
// webconfig.Load's merge convention. Either path may not exist. When the
// merged workers: block is empty, Load falls back to translating
// daemon_federation.workers[] (local overriding base whole, matching
// webconfig's existing DaemonFederation merge rule) into Entry values.
func Load(baseConfigPath, localConfigPath string) (Registry, error) {
	base, _, err := readFile(baseConfigPath)
	if err != nil {
		return Registry{}, err
	}
	local, _, err := readFile(localConfigPath)
	if err != nil {
		return Registry{}, err
	}

	workers := base.Workers
	if len(local.Workers) > 0 {
		workers = local.Workers
	}
	cfg := Config{Workers: workers}
	if err := cfg.Validate(localConfigPath); err != nil {
		return Registry{}, err
	}
	if len(cfg.Workers) > 0 {
		return Registry{Entries: cfg.Workers, Source: "workers"}, nil
	}

	fed := base.DaemonFederation
	if len(local.DaemonFederation.Workers) > 0 {
		fed = local.DaemonFederation
	}
	if len(fed.Workers) == 0 {
		return Registry{Source: "workers"}, nil
	}
	if err := fed.Validate(localConfigPath); err != nil {
		return Registry{}, err
	}
	return Registry{Entries: FromDaemonFederation(fed), Source: "daemon_federation"}, nil
}

// FromDaemonFederation translates legacy daemon_federation.workers[] entries
// into registry Entry values for back-compat listing. Translated entries
// carry no advertised Capabilities (daemon federation never declared them)
// and default Enabled to true, since daemon federation has no enabled bit —
// every configured worker is implicitly usable today.
func FromDaemonFederation(cfg daemonfederation.Config) []Entry {
	out := make([]Entry, 0, len(cfg.Workers))
	for _, w := range cfg.Workers {
		out = append(out, Entry{
			ID:        w.ID,
			Label:     w.Label,
			Placement: w.Placement,
			Endpoint:  w.Endpoint,
			Tunnel:    w.Tunnel,
			Enabled:   true,
			Legacy:    true,
		})
	}
	return out
}

// Projection is the stable, portal-facing JSON contract for `kitsoki worker
// list --json`: id, label, placement, health, capabilities, enabled. It
// never carries credentials, tunnel identity file paths, or any other
// machine-local secret material — Entry's Tunnel/CredentialEnv fields are
// deliberately excluded here, not merely tagged json:"-" on Entry (defense in
// depth: a future Entry field added without a json tag would still not reach
// Projection because Projection is a distinct type built field-by-field).
type Projection struct {
	ID           string       `json:"id"`
	Label        string       `json:"label"`
	Placement    string       `json:"placement"`
	Health       string       `json:"health"`
	Capabilities Capabilities `json:"capabilities"`
	Enabled      bool         `json:"enabled"`
	Jobs         int          `json:"jobs"`
}

// ToProjection builds the portal-safe projection for one entry. health and
// jobs are supplied by the caller (typically from a live
// daemonfederation.Pool snapshot, or "unknown"/0 when no daemon is reachable)
// since Entry alone carries no runtime state.
func (e Entry) ToProjection(health string, jobs int) Projection {
	if health == "" {
		health = "unknown"
	}
	return Projection{
		ID:           e.ID,
		Label:        e.Label,
		Placement:    e.Placement,
		Health:       health,
		Capabilities: e.Capabilities,
		Enabled:      e.Enabled,
		Jobs:         jobs,
	}
}
