package webconfig

import (
	"fmt"
	"path/filepath"
	"strings"

	"kitsoki/internal/applicationartifact"
)

const (
	maxArtifactCallers = 64
	maxCatalogRefBytes = 128
)

// StoryApplicationArtifactConfig binds one calling Story Application to
// deployment-selected artifact producers. Story inputs never select these
// applications, phases, catalog paths, or execution authority.
type StoryApplicationArtifactConfig struct {
	Catalog      string                                 `yaml:"catalog"`
	CatalogRef   string                                 `yaml:"catalog_ref"`
	CreateMockup *applicationartifact.Binding           `yaml:"create_mockup"`
	Materialize  map[string]applicationartifact.Binding `yaml:"materialize"`
}

func (cfg *WebConfig) resolveStoryApplicationArtifacts() error {
	if len(cfg.StoryApplicationArtifacts) > maxArtifactCallers {
		return fmt.Errorf(
			"story_application_artifacts has %d callers, exceeds %d",
			len(cfg.StoryApplicationArtifacts),
			maxArtifactCallers,
		)
	}
	for caller, binding := range cfg.StoryApplicationArtifacts {
		if err := configIdentity("caller application id", caller); err != nil {
			return fmt.Errorf("story_application_artifacts: %w", err)
		}
		if strings.TrimSpace(binding.Catalog) == "" {
			return fmt.Errorf("story_application_artifacts.%s.catalog is required", caller)
		}
		if filepath.IsAbs(binding.Catalog) ||
			strings.Contains(binding.Catalog, "://") ||
			escapesRoot(binding.Catalog) {
			return fmt.Errorf(
				"story_application_artifacts.%s.catalog must be repository-relative",
				caller,
			)
		}
		if err := configIdentity("catalog_ref", binding.CatalogRef); err != nil ||
			len(binding.CatalogRef) > maxCatalogRefBytes {
			return fmt.Errorf(
				"story_application_artifacts.%s.catalog_ref must be a bounded opaque identity",
				caller,
			)
		}
		if binding.CreateMockup == nil {
			return fmt.Errorf("story_application_artifacts.%s.create_mockup is required", caller)
		}
		if !binding.CreateMockup.Bundle || strings.TrimSpace(binding.CreateMockup.PrimaryOutput) == "" {
			return fmt.Errorf(
				"story_application_artifacts.%s.create_mockup requires bundle: true and primary_output",
				caller,
			)
		}
		if err := validateConfiguredBinding(caller, "create_mockup", *binding.CreateMockup); err != nil {
			return err
		}
		if len(binding.Materialize) != 3 {
			return fmt.Errorf(
				"story_application_artifacts.%s.materialize must configure dependencies, subject, and verify",
				caller,
			)
		}
		for _, phase := range []string{"dependencies", "subject", "verify"} {
			operation, ok := binding.Materialize[phase]
			if !ok {
				return fmt.Errorf(
					"story_application_artifacts.%s.materialize.%s is required",
					caller,
					phase,
				)
			}
			if err := validateConfiguredBinding(caller, "materialize."+phase, operation); err != nil {
				return err
			}
		}
		for phase := range binding.Materialize {
			if phase != "dependencies" && phase != "subject" && phase != "verify" {
				return fmt.Errorf(
					"story_application_artifacts.%s.materialize has unknown phase %q",
					caller,
					phase,
				)
			}
		}
	}
	return nil
}

func validateConfiguredBinding(caller, operation string, binding applicationartifact.Binding) error {
	if err := configIdentity("producer application id", binding.ApplicationID); err != nil {
		return fmt.Errorf("story_application_artifacts.%s.%s: %w", caller, operation, err)
	}
	if len(binding.Phases) == 0 || len(binding.Phases) > applicationartifact.MaxPhases {
		return fmt.Errorf(
			"story_application_artifacts.%s.%s phases must be within 1..%d",
			caller,
			operation,
			applicationartifact.MaxPhases,
		)
	}
	for index, phase := range binding.Phases {
		if err := configIdentity("phase id", phase.ID); err != nil {
			return fmt.Errorf(
				"story_application_artifacts.%s.%s.phases[%d]: %w",
				caller,
				operation,
				index,
				err,
			)
		}
		if (strings.TrimSpace(phase.Handler) == "") == (strings.TrimSpace(phase.Action) == "") {
			return fmt.Errorf(
				"story_application_artifacts.%s.%s.phases[%d] must select exactly one handler or action",
				caller,
				operation,
				index,
			)
		}
		if phase.Handler != "" {
			if err := configIdentity("handler", phase.Handler); err != nil {
				return fmt.Errorf("story_application_artifacts.%s.%s: %w", caller, operation, err)
			}
		}
		if phase.Action != "" {
			if err := configIdentity("action", phase.Action); err != nil {
				return fmt.Errorf("story_application_artifacts.%s.%s: %w", caller, operation, err)
			}
		}
		if len(phase.ArtifactOutputs) == 0 || len(phase.ArtifactOutputs) > applicationartifact.MaxOutputs {
			return fmt.Errorf(
				"story_application_artifacts.%s.%s phase %q artifact_outputs must be within 1..%d",
				caller,
				operation,
				phase.ID,
				applicationartifact.MaxOutputs,
			)
		}
		for _, output := range phase.ArtifactOutputs {
			if err := configIdentity("artifact output", output); err != nil {
				return fmt.Errorf("story_application_artifacts.%s.%s: %w", caller, operation, err)
			}
		}
	}
	return nil
}

func configIdentity(label, value string) error {
	value = strings.TrimSpace(value)
	if value == "" ||
		len(value) > applicationartifact.MaxIdentityByte ||
		strings.ContainsAny(value, `/\`) ||
		strings.Contains(value, "..") ||
		strings.Contains(value, "://") {
		return fmt.Errorf("%s is empty, malformed, or unbounded", label)
	}
	return nil
}

func escapesRoot(path string) bool {
	clean := filepath.Clean(path)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
