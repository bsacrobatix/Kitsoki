package queue

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type trackedGateProfile struct {
	Commands struct {
		Test    string `yaml:"test"`
		Change  string `yaml:"change"`
		Full    string `yaml:"full"`
		Release string `yaml:"release"`
	} `yaml:"commands"`
}

// RequiredGateCommand resolves the checked-in command for a protected target.
// Repositories without a project profile retain compatibility; once a profile
// exists, its tier command is mandatory and an empty declaration fails closed.
func RequiredGateCommand(projectRoot, target string) (string, bool, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", false, err
	}
	ref := strings.TrimPrefix(strings.TrimSpace(target), "refs/heads/")
	path := "refs/heads/" + ref + ":.kitsoki/project-profile.yaml"
	cmd := exec.Command("git", "-C", root, "show", path)
	raw, err := cmd.Output()
	if err != nil {
		tier := RequiredGateTierForTarget(target)
		return "", false, fmt.Errorf("queue: target %q requires tracked %s gate policy at %s: %w", target, tier, path, err)
	}
	var profile trackedGateProfile
	if err := yaml.Unmarshal(raw, &profile); err != nil {
		return "", true, fmt.Errorf("queue: parse tracked gate profile: %w", err)
	}
	tier := RequiredGateTierForTarget(target)
	var command string
	switch tier {
	case "release":
		command = profile.Commands.Release
	case "full":
		command = first(profile.Commands.Full, profile.Commands.Test)
	default:
		command = first(profile.Commands.Change, profile.Commands.Test)
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return "", true, fmt.Errorf("queue: tracked project profile does not declare commands.%s for target %q", tier, target)
	}
	return command, true, nil
}

// ValidateGateCommand prevents callers from relabeling an arbitrary shell
// command as a full/release gate. The command is resolved from trusted project
// configuration, never from the candidate tree.
func ValidateGateCommand(projectRoot, target, command string) error {
	required, configured, err := RequiredGateCommand(projectRoot, target)
	if err != nil {
		return err
	}
	if configured && strings.TrimSpace(command) != required {
		return fmt.Errorf("queue: target %q requires tracked %s gate %q; refusing command %q",
			target, RequiredGateTierForTarget(target), required, strings.TrimSpace(command))
	}
	return nil
}

func validateProcessGatePolicy(projectRoot string, deps ProcessDeps) error {
	target := strings.TrimSpace(deps.TargetRef)
	// Empty-target ProcessDeps are a trusted programmatic embedding contract
	// retained for compatibility. Every built-in CLI/native promotion surface
	// supplies an explicit target and therefore remains fail-closed.
	if target == "" {
		return nil
	}
	tier := strings.TrimSpace(deps.GateTier)
	requiredTier := RequiredGateTierForTarget(target)
	if tier != "" && tier != requiredTier {
		return fmt.Errorf("queue: target %q requires gate tier %q, got %q", target, requiredTier, tier)
	}
	switch gate := deps.Gate.(type) {
	case ShellGate:
		return ValidateGateCommand(projectRoot, target, gate.Command)
	case *ShellGate:
		return ValidateGateCommand(projectRoot, target, gate.Command)
	case ExecutorGate:
		_, _, err := RequiredGateCommand(projectRoot, target)
		if err != nil {
			return err
		}
		if gate.pipelineName() != requiredTier {
			return fmt.Errorf("queue: target %q requires executor pipeline %q, got %q", target, requiredTier, gate.pipelineName())
		}
		return nil
	case *ExecutorGate:
		_, _, err := RequiredGateCommand(projectRoot, target)
		if err != nil {
			return err
		}
		if gate.pipelineName() != requiredTier {
			return fmt.Errorf("queue: target %q requires executor pipeline %q, got %q", target, requiredTier, gate.pipelineName())
		}
		return nil
	default:
		// Programmatic test/embedding gates are injected capabilities rather
		// than caller-controlled shell strings. All built-in user surfaces
		// construct ShellGate or ExecutorGate and are policy-checked above.
		return nil
	}
}
