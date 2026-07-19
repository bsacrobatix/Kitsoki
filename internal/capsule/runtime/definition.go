package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var envPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func Load(raw []byte) (Definition, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var d Definition
	if err := dec.Decode(&d); err != nil {
		return Definition{}, fmt.Errorf("capsule runtime: decode declaration: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Definition{}, fmt.Errorf("capsule runtime: declaration contains multiple documents")
	}
	return Seal(d)
}

func Seal(d Definition) (Definition, error) {
	if d.Schema != Schema {
		return Definition{}, fmt.Errorf("capsule runtime: schema must be %q", Schema)
	}
	if len(d.Commands) == 0 || len(d.Services) == 0 || len(d.Profiles) == 0 {
		return Definition{}, fmt.Errorf("capsule runtime: commands, services, and profiles are required")
	}
	for id, cmd := range d.Commands {
		if !namePattern.MatchString(id) || len(cmd.Argv) == 0 || strings.TrimSpace(cmd.Argv[0]) == "" {
			return Definition{}, fmt.Errorf("capsule runtime: invalid command %q", id)
		}
	}
	for name, svc := range d.Services {
		if !namePattern.MatchString(name) {
			return Definition{}, fmt.Errorf("capsule runtime: invalid service %q", name)
		}
		if _, ok := d.Commands[svc.Command]; !ok {
			return Definition{}, fmt.Errorf("capsule runtime: service %q references unknown command %q", name, svc.Command)
		}
		if err := relativeDirectory(svc.WorkingDir); err != nil {
			return Definition{}, fmt.Errorf("capsule runtime: service %q: %w", name, err)
		}
		for role, port := range svc.Ports {
			if !namePattern.MatchString(role) || (port.Protocol != "http" && port.Protocol != "tcp") || !envPattern.MatchString(port.Env) || (port.Exposure != "review" && port.Exposure != "internal") {
				return Definition{}, fmt.Errorf("capsule runtime: service %q has invalid port %q", name, role)
			}
		}
		if svc.Health != nil {
			if _, ok := svc.Ports[svc.Health.Port]; !ok {
				return Definition{}, fmt.Errorf("capsule runtime: service %q health references unknown port", name)
			}
			timeout, err := time.ParseDuration(svc.Health.TimeoutText)
			if err != nil || timeout <= 0 {
				return Definition{}, fmt.Errorf("capsule runtime: service %q health timeout is required", name)
			}
			svc.Health.Timeout = timeout
			d.Services[name] = svc
		}
		for _, dep := range svc.DependsOn {
			if _, ok := d.Services[dep]; !ok {
				return Definition{}, fmt.Errorf("capsule runtime: service %q depends on unknown service %q", name, dep)
			}
		}
	}
	for name, profile := range d.Profiles {
		if !namePattern.MatchString(name) || strings.TrimSpace(profile.Provider) == "" {
			return Definition{}, fmt.Errorf("capsule runtime: invalid profile %q", name)
		}
	}
	d.Digest = ""
	raw, err := json.Marshal(canonical(d))
	if err != nil {
		return Definition{}, err
	}
	sum := sha256.Sum256(raw)
	d.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return d, nil
}
func relativeDirectory(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	clean := filepath.Clean(dir)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("working_dir must be project-relative")
	}
	return nil
}
func canonical(d Definition) Definition {
	d.Commands = sortedCommands(d.Commands)
	d.Services = sortedServices(d.Services)
	d.Dependencies = sortedDependencies(d.Dependencies)
	d.Profiles = sortedProfiles(d.Profiles)
	return d
}

func digestSourceManifest(sourceRef, sourceSHA string) string {
	raw, _ := json.Marshal(struct {
		Schema    string `json:"schema"`
		SourceRef string `json:"source_ref"`
		SourceSHA string `json:"source_sha"`
	}{Schema: "capsule-runtime-source/v1", SourceRef: sourceRef, SourceSHA: sourceSHA})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// JSON maps already serialize in key order; these copies prevent caller maps
// being mutated while sealing and make the normalization intent explicit.
func sortedCommands(in map[string]Command) map[string]Command           { return copyMap(in) }
func sortedServices(in map[string]Service) map[string]Service           { return copyMap(in) }
func sortedDependencies(in map[string]Dependency) map[string]Dependency { return copyMap(in) }
func sortedProfiles(in map[string]Profile) map[string]Profile           { return copyMap(in) }
func copyMap[T any](in map[string]T) map[string]T {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]T, len(in))
	for _, k := range keys {
		out[k] = in[k]
	}
	return out
}
