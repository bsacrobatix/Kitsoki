package webconfig

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

const (
	maxAssuranceChecks         = 64
	maxAssuranceResolvedBytes  = 128 * 1024
	maxComplianceEvidenceBytes = 256 * 1024
	maxFlowEvidenceBytes       = 1024 * 1024
	maxAssuranceSuites         = 16
	maxAssuranceRuns           = 200
	maxAssuranceSuiteBytes     = 8 * 1024 * 1024
)

// StoryApplicationAssuranceConfig binds one application to server-owned
// compliance and deterministic flow-evidence authority.
type StoryApplicationAssuranceConfig struct {
	Catalog      string                   `yaml:"catalog"`
	Compliance   *ApplicationCompliance   `yaml:"compliance,omitempty"`
	FlowEvidence *ApplicationFlowEvidence `yaml:"flow_evidence,omitempty"`
	Extra        map[string]any           `yaml:",inline"`
}

type ApplicationCompliance struct {
	MaxChecks        int            `yaml:"max_checks"`
	MaxResolvedBytes int            `yaml:"max_resolved_bytes"`
	MaxEvidenceBytes int            `yaml:"max_evidence_bytes"`
	Extra            map[string]any `yaml:",inline"`
}

type ApplicationFlowEvidence struct {
	Suites           []ApplicationFlowSuite `yaml:"suites"`
	MaxSuites        int                    `yaml:"max_suites"`
	MaxRuns          int                    `yaml:"max_runs"`
	MaxSuiteBytes    int                    `yaml:"max_suite_bytes"`
	MaxEvidenceBytes int                    `yaml:"max_evidence_bytes"`
	Extra            map[string]any         `yaml:",inline"`
}

type ApplicationFlowSuite struct {
	ID      string         `yaml:"id"`
	App     string         `yaml:"app"`
	Flows   string         `yaml:"flows"`
	Version string         `yaml:"version"`
	Extra   map[string]any `yaml:",inline"`
}

func (c *StoryApplicationAssuranceConfig) UnmarshalYAML(data []byte) error {
	type plain StoryApplicationAssuranceConfig
	if err := rejectYAMLKeys(data, map[string]bool{
		"catalog": true, "compliance": true, "flow_evidence": true,
	}); err != nil {
		return fmt.Errorf("story application assurance: %w", err)
	}
	var decoded plain
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = StoryApplicationAssuranceConfig(decoded)
	return nil
}

func (c *ApplicationCompliance) UnmarshalYAML(data []byte) error {
	type plain ApplicationCompliance
	if err := rejectYAMLKeys(data, map[string]bool{
		"max_checks": true, "max_resolved_bytes": true, "max_evidence_bytes": true,
	}); err != nil {
		return fmt.Errorf("compliance: %w", err)
	}
	var decoded plain
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = ApplicationCompliance(decoded)
	return nil
}

func (c *ApplicationFlowEvidence) UnmarshalYAML(data []byte) error {
	type plain ApplicationFlowEvidence
	if err := rejectYAMLKeys(data, map[string]bool{
		"suites": true, "max_suites": true, "max_runs": true,
		"max_suite_bytes": true, "max_evidence_bytes": true,
	}); err != nil {
		return fmt.Errorf("flow_evidence: %w", err)
	}
	var decoded plain
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = ApplicationFlowEvidence(decoded)
	return nil
}

func (c *ApplicationFlowSuite) UnmarshalYAML(data []byte) error {
	type plain ApplicationFlowSuite
	if err := rejectYAMLKeys(data, map[string]bool{
		"id": true, "app": true, "flows": true, "version": true,
	}); err != nil {
		return fmt.Errorf("flow suite: %w", err)
	}
	var decoded plain
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = ApplicationFlowSuite(decoded)
	return nil
}

func rejectYAMLKeys(data []byte, allowed map[string]bool) error {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key := range raw {
		if !allowed[key] {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	return nil
}

func (cfg *WebConfig) resolveStoryApplicationAssurance() error {
	for appID, binding := range cfg.StoryApplicationAssurance {
		prefix := "story_application_assurance." + appID
		if err := rejectApplicationAssuranceExtra(prefix, binding.Extra); err != nil {
			return err
		}
		if !readModelIdentity(appID) {
			return fmt.Errorf("story_application_assurance key %q must be an opaque application id", appID)
		}
		if err := repositoryRelativeAssurancePath(binding.Catalog); err != nil {
			return fmt.Errorf("%s.catalog: %w", prefix, err)
		}
		if binding.Compliance == nil && binding.FlowEvidence == nil {
			return fmt.Errorf("%s must enable compliance or flow_evidence", prefix)
		}
		if binding.Compliance != nil {
			c := binding.Compliance
			if err := rejectApplicationAssuranceExtra(prefix+".compliance", c.Extra); err != nil {
				return err
			}
			switch {
			case c.MaxChecks < 1 || c.MaxChecks > maxAssuranceChecks:
				return fmt.Errorf("%s.compliance.max_checks must be between 1 and %d", prefix, maxAssuranceChecks)
			case c.MaxResolvedBytes < 1 || c.MaxResolvedBytes > maxAssuranceResolvedBytes:
				return fmt.Errorf("%s.compliance.max_resolved_bytes must be between 1 and %d", prefix, maxAssuranceResolvedBytes)
			case c.MaxEvidenceBytes < 1 || c.MaxEvidenceBytes > maxComplianceEvidenceBytes:
				return fmt.Errorf("%s.compliance.max_evidence_bytes must be between 1 and %d", prefix, maxComplianceEvidenceBytes)
			}
		}
		if binding.FlowEvidence != nil {
			f := binding.FlowEvidence
			if err := rejectApplicationAssuranceExtra(prefix+".flow_evidence", f.Extra); err != nil {
				return err
			}
			switch {
			case f.MaxSuites < 1 || f.MaxSuites > maxAssuranceSuites:
				return fmt.Errorf("%s.flow_evidence.max_suites must be between 1 and %d", prefix, maxAssuranceSuites)
			case f.MaxRuns < 1 || f.MaxRuns > maxAssuranceRuns:
				return fmt.Errorf("%s.flow_evidence.max_runs must be between 1 and %d", prefix, maxAssuranceRuns)
			case f.MaxSuiteBytes < 1 || f.MaxSuiteBytes > maxAssuranceSuiteBytes:
				return fmt.Errorf("%s.flow_evidence.max_suite_bytes must be between 1 and %d", prefix, maxAssuranceSuiteBytes)
			case f.MaxEvidenceBytes < 1 || f.MaxEvidenceBytes > maxFlowEvidenceBytes:
				return fmt.Errorf("%s.flow_evidence.max_evidence_bytes must be between 1 and %d", prefix, maxFlowEvidenceBytes)
			case len(f.Suites) < 1:
				return fmt.Errorf("%s.flow_evidence.suites must not be empty", prefix)
			case len(f.Suites) > f.MaxSuites:
				return fmt.Errorf("%s.flow_evidence.suites exceeds max_suites", prefix)
			}
			seen := make(map[string]bool, len(f.Suites))
			for i, suite := range f.Suites {
				suitePrefix := fmt.Sprintf("%s.flow_evidence.suites[%d]", prefix, i)
				if err := rejectApplicationAssuranceExtra(suitePrefix, suite.Extra); err != nil {
					return err
				}
				if !readModelIdentity(suite.ID) {
					return fmt.Errorf("%s.id must be an opaque identity", suitePrefix)
				}
				if seen[suite.ID] {
					return fmt.Errorf("%s.id %q is duplicated", suitePrefix, suite.ID)
				}
				seen[suite.ID] = true
				if !readModelIdentity(suite.Version) {
					return fmt.Errorf("%s.version must be an opaque identity", suitePrefix)
				}
				if err := repositoryRelativeAssurancePath(suite.App); err != nil {
					return fmt.Errorf("%s.app: %w", suitePrefix, err)
				}
				if filepath.Base(suite.App) != "app.yaml" {
					return fmt.Errorf("%s.app must name app.yaml", suitePrefix)
				}
				if err := repositoryRelativeAssuranceGlob(suite.Flows); err != nil {
					return fmt.Errorf("%s.flows: %w", suitePrefix, err)
				}
			}
		}
	}
	return nil
}

func rejectApplicationAssuranceExtra(prefix string, extra map[string]any) error {
	for key := range extra {
		return fmt.Errorf("%s: unknown field %q", prefix, key)
	}
	return nil
}

func repositoryRelativeAssurancePath(value string) error {
	if value == "" || strings.TrimSpace(value) != value || filepath.IsAbs(value) {
		return fmt.Errorf("must be a non-empty repository-relative path")
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) ||
		strings.ContainsAny(value, "\r\n\x00") || strings.Contains(strings.ToLower(value), "://") {
		return fmt.Errorf("must remain within the repository")
	}
	return nil
}

func repositoryRelativeAssuranceGlob(value string) error {
	if err := repositoryRelativeAssurancePath(value); err != nil {
		return err
	}
	if _, err := filepath.Match(value, value); err != nil {
		return fmt.Errorf("invalid glob: %w", err)
	}
	return nil
}
