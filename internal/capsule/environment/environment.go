package environment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const Schema = "capsule-environment/v1"
const LockSchema = "capsule-environment-lock/v1"

// Definition is checked in under .kitsoki/environments/<id>.yaml. SecretRefs
// are identifiers only; resolved secret values never enter this structure or a
// lock.
type Definition struct {
	Schema         string            `yaml:"schema" json:"schema"`
	ID             string            `yaml:"id" json:"id"`
	Source         Source            `yaml:"source" json:"source"`
	Toolchains     map[string]string `yaml:"toolchains,omitempty" json:"toolchains,omitempty"`
	Lockfiles      []string          `yaml:"lockfiles,omitempty" json:"lockfiles,omitempty"`
	Bootstrap      Bootstrap         `yaml:"bootstrap,omitempty" json:"bootstrap,omitempty"`
	Network        string            `yaml:"network,omitempty" json:"network,omitempty"`
	Caches         []CacheGrant      `yaml:"caches,omitempty" json:"caches,omitempty"`
	Sandbox        string            `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	SecretRefs     []string          `yaml:"secret_refs,omitempty" json:"secret_refs,omitempty"`
	DefinitionPath string            `yaml:"-" json:"-"`
}
type Source struct {
	Image        string `yaml:"image,omitempty" json:"image,omitempty"`
	Devcontainer string `yaml:"devcontainer,omitempty" json:"devcontainer,omitempty"`
	HostProbe    bool   `yaml:"host_probe,omitempty" json:"host_probe,omitempty"`
}
type Bootstrap struct {
	Command string `yaml:"command,omitempty" json:"command,omitempty"`
}
type CacheGrant struct {
	ID    string `yaml:"id" json:"id"`
	Scope string `yaml:"scope" json:"scope"`
	Mode  string `yaml:"mode" json:"mode"`
}
type InputDigest struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// Lock captures every material input needed to trust a prepared environment.
// Requested secret names are intentionally not serialized: the presence of a
// secret requirement is policy, while its value is executor-only authority.
type Lock struct {
	Schema           string            `json:"schema"`
	ID               string            `json:"id"`
	DefinitionDigest string            `json:"definition_digest"`
	ImageDigest      string            `json:"image_digest,omitempty"`
	Toolchains       map[string]string `json:"toolchains,omitempty"`
	Lockfiles        []InputDigest     `json:"lockfiles,omitempty"`
	BootstrapDigest  string            `json:"bootstrap_digest,omitempty"`
	Network          string            `json:"network"`
	Sandbox          string            `json:"sandbox"`
	CacheKeys        []string          `json:"cache_keys,omitempty"`
	SecretRequired   bool              `json:"secret_required,omitempty"`
	Digest           string            `json:"digest"`
}

type ToolProbe interface {
	Probe(context.Context, string) (string, error)
}
type ToolProbeFunc func(context.Context, string) (string, error)

func (f ToolProbeFunc) Probe(ctx context.Context, name string) (string, error) { return f(ctx, name) }

type ImageResolver interface {
	Resolve(context.Context, string) (string, error)
}
type ImageResolverFunc func(context.Context, string) (string, error)

func (f ImageResolverFunc) Resolve(ctx context.Context, ref string) (string, error) {
	return f(ctx, ref)
}

// Resolver is dependency-injected so all production/container probing and test
// fixtures share the exact same lock calculation.
type Resolver struct {
	ProjectRoot string
	Probe       ToolProbe
	Images      ImageResolver
	Now         func() time.Time
}

func Load(projectRoot, id string) (Definition, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return Definition{}, err
	}
	path := filepath.Join(root, ".kitsoki", "environments", id+".yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, fmt.Errorf("capsule environment: read %s: %w", path, err)
	}
	var d Definition
	if err := yaml.Unmarshal(raw, &d); err != nil {
		return Definition{}, fmt.Errorf("capsule environment: parse %s: %w", path, err)
	}
	d.DefinitionPath = path
	if err := Validate(d); err != nil {
		return Definition{}, err
	}
	return d, nil
}
func Validate(d Definition) error {
	if d.Schema != Schema {
		return fmt.Errorf("capsule environment: schema %q, want %q", d.Schema, Schema)
	}
	if strings.TrimSpace(d.ID) == "" {
		return fmt.Errorf("capsule environment: id is required")
	}
	n := 0
	if d.Source.Image != "" {
		n++
	}
	if d.Source.Devcontainer != "" {
		n++
	}
	if d.Source.HostProbe {
		n++
	}
	if n > 1 {
		return fmt.Errorf("capsule environment %q: source must select at most one materializer", d.ID)
	}
	if d.Network == "" {
		d.Network = "none"
	}
	if d.Network != "none" && d.Network != "replay" && d.Network != "live" {
		return fmt.Errorf("capsule environment %q: invalid network %q", d.ID, d.Network)
	}
	for _, path := range d.Lockfiles {
		if filepath.IsAbs(path) || strings.HasPrefix(filepath.Clean(path), "..") {
			return fmt.Errorf("capsule environment %q: lockfile %q escapes project", d.ID, path)
		}
	}
	for _, cache := range d.Caches {
		if cache.ID == "" || cache.Scope == "" || (cache.Mode != "read_only" && cache.Mode != "read_write") {
			return fmt.Errorf("capsule environment %q: invalid cache grant", d.ID)
		}
	}
	return nil
}

func (r Resolver) Resolve(ctx context.Context, id string) (Lock, error) {
	d, err := Load(r.ProjectRoot, id)
	if err != nil {
		return Lock{}, err
	}
	root, err := filepath.Abs(r.ProjectRoot)
	if err != nil {
		return Lock{}, err
	}
	defDigest, err := definitionDigest(d)
	if err != nil {
		return Lock{}, err
	}
	lock := Lock{Schema: LockSchema, ID: d.ID, DefinitionDigest: defDigest, Toolchains: map[string]string{}, Network: network(d.Network), Sandbox: sandbox(d.Sandbox), SecretRequired: len(d.SecretRefs) > 0}
	if d.Source.Image != "" {
		if r.Images == nil {
			return Lock{}, fmt.Errorf("capsule environment %q: image resolver is required", d.ID)
		}
		image, err := r.Images.Resolve(ctx, d.Source.Image)
		if err != nil {
			return Lock{}, err
		}
		if !strings.Contains(image, "@sha256:") {
			return Lock{}, fmt.Errorf("capsule environment %q: image resolver returned unpinned %q", d.ID, image)
		}
		lock.ImageDigest = image
	}
	if d.Source.Devcontainer != "" {
		path := filepath.Join(root, d.Source.Devcontainer)
		raw, err := os.ReadFile(path)
		if err != nil {
			return Lock{}, fmt.Errorf("capsule environment: read devcontainer: %w", err)
		}
		lock.ImageDigest = "devcontainer@sha256:" + hash(raw)
	}
	if d.Source.HostProbe || len(d.Toolchains) > 0 {
		if r.Probe == nil {
			return Lock{}, fmt.Errorf("capsule environment %q: host probe is required", d.ID)
		}
		keys := sortedMapKeys(d.Toolchains)
		for _, name := range keys {
			observed, err := r.Probe.Probe(ctx, name)
			if err != nil {
				return Lock{}, fmt.Errorf("capsule environment %q: probe %s: %w", d.ID, name, err)
			}
			want := d.Toolchains[name]
			if want != "" && !strings.Contains(strings.TrimPrefix(observed, "v"), strings.TrimPrefix(want, "v")) {
				return Lock{}, fmt.Errorf("capsule environment %q: toolchain %s mismatch: got %q, want %q", d.ID, name, observed, want)
			}
			lock.Toolchains[name] = observed
		}
	}
	for _, rel := range d.Lockfiles {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return Lock{}, fmt.Errorf("capsule environment: read lockfile %s: %w", rel, err)
		}
		lock.Lockfiles = append(lock.Lockfiles, InputDigest{Path: filepath.ToSlash(rel), Digest: "sha256:" + hash(raw)})
	}
	sort.Slice(lock.Lockfiles, func(i, j int) bool { return lock.Lockfiles[i].Path < lock.Lockfiles[j].Path })
	if d.Bootstrap.Command != "" {
		lock.BootstrapDigest = "sha256:" + hash([]byte(d.Bootstrap.Command))
	}
	for _, cache := range d.Caches {
		lock.CacheKeys = append(lock.CacheKeys, cache.Scope+":"+cache.ID)
	}
	sort.Strings(lock.CacheKeys)
	lock.Digest = lockDigest(lock)
	return lock, nil
}
func WriteLock(projectRoot string, lock Lock) (string, error) {
	if lock.Schema != LockSchema || lock.Digest == "" {
		return "", fmt.Errorf("capsule environment: invalid lock")
	}
	path := filepath.Join(projectRoot, ".kitsoki", "environments", lock.ID+".lock.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
func ReadLock(path string) (Lock, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Lock{}, err
	}
	var lock Lock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return Lock{}, err
	}
	if lock.Schema != LockSchema || lock.Digest != lockDigest(lock) {
		return Lock{}, fmt.Errorf("capsule environment: invalid or tampered lock")
	}
	return lock, nil
}

type hostProbe struct{}

func (hostProbe) Probe(ctx context.Context, name string) (string, error) {
	args := []string{"--version"}
	if name == "go" {
		args = []string{"version"}
	}
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
func HostProbe() ToolProbe { return hostProbe{} }
func definitionDigest(d Definition) (string, error) {
	d.DefinitionPath = ""
	raw, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	return "sha256:" + hash(raw), nil
}
func lockDigest(l Lock) string {
	l.Digest = ""
	raw, _ := json.Marshal(l)
	return "sha256:" + hash(raw)
}
func hash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func network(in string) string {
	if in == "" {
		return "none"
	}
	return in
}
func sandbox(in string) string {
	if in == "" {
		return "supervised"
	}
	return in
}
func sortedMapKeys(in map[string]string) []string {
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
