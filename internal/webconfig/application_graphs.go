package webconfig

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/host"
)

const maxApplicationGraphCallers = 64

// ApplicationGraphConfig binds one Story Application to a repository-owned
// graph. Runtime callers cannot select paths, bounds, write policy, or actor.
type ApplicationGraphConfig struct {
	ProjectRoot string `yaml:"project_root"`
	Catalog     string `yaml:"catalog"`
	Overlay     string `yaml:"overlay,omitempty"`
	MaxNodes    int    `yaml:"max_nodes"`
	MaxBytes    int    `yaml:"max_bytes"`
	WritePolicy string `yaml:"write_policy"`
}

func (c *ApplicationGraphConfig) UnmarshalYAML(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return fmt.Errorf("application graph binding must be a mapping")
	}
	allowed := map[string]bool{
		"project_root": true,
		"catalog":      true,
		"overlay":      true,
		"max_nodes":    true,
		"max_bytes":    true,
		"write_policy": true,
	}
	seen := make(map[string]bool, len(allowed))
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index].Value
		if !allowed[key] {
			return fmt.Errorf("application graph binding has unknown field %q", key)
		}
		if seen[key] {
			return fmt.Errorf("application graph binding repeats field %q", key)
		}
		seen[key] = true
	}
	type plain ApplicationGraphConfig
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*c = ApplicationGraphConfig(decoded)
	return nil
}

func (cfg *WebConfig) resolveApplicationGraphs() error {
	if len(cfg.ApplicationGraphs) > maxApplicationGraphCallers {
		return fmt.Errorf(
			"application_graphs has %d callers, exceeds %d",
			len(cfg.ApplicationGraphs), maxApplicationGraphCallers,
		)
	}
	for caller, binding := range cfg.ApplicationGraphs {
		if err := configIdentity("caller application id", caller); err != nil {
			return fmt.Errorf("application_graphs: %w", err)
		}
		for name, value := range map[string]string{
			"project_root": binding.ProjectRoot,
			"catalog":      binding.Catalog,
		} {
			if !repositoryRelativeGraphPath(value) {
				return fmt.Errorf(
					"application_graphs.%s.%s must be a repository-relative path",
					caller, name,
				)
			}
		}
		if binding.Overlay != "" && !repositoryRelativeGraphPath(binding.Overlay) {
			return fmt.Errorf(
				"application_graphs.%s.overlay must be a repository-relative path",
				caller,
			)
		}
		if binding.MaxNodes < 1 || binding.MaxNodes > host.MaxApplicationGraphNodes {
			return fmt.Errorf(
				"application_graphs.%s.max_nodes must be within 1..%d",
				caller, host.MaxApplicationGraphNodes,
			)
		}
		if binding.MaxBytes < 1 || binding.MaxBytes > host.MaxApplicationGraphBytes {
			return fmt.Errorf(
				"application_graphs.%s.max_bytes must be within 1..%d",
				caller, host.MaxApplicationGraphBytes,
			)
		}
		if !host.ValidApplicationGraphWritePolicy(binding.WritePolicy) {
			return fmt.Errorf(
				"application_graphs.%s.write_policy must be read, propose, or steward",
				caller,
			)
		}
	}
	return nil
}

func repositoryRelativeGraphPath(value string) bool {
	if value == "" || value != strings.TrimSpace(value) ||
		filepath.IsAbs(value) || strings.Contains(value, "://") ||
		strings.ContainsAny(value, "\r\n\x00") || escapesRoot(value) {
		return false
	}
	return true
}
