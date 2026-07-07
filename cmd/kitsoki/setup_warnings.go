package main

import (
	"strings"

	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/webconfig"
)

const runAsUserSetupStoryRef = "@kitsoki/run-as-user-setup"

func setupWarningsFromConfig(cfg webconfig.WebConfig, goos string) []server.SetupWarning {
	if w := runAsUserSetupWarning(cfg, goos); w != nil {
		return []server.SetupWarning{*w}
	}
	return nil
}

func runAsUserSetupWarning(cfg webconfig.WebConfig, goos string) *server.SetupWarning {
	if goos != "darwin" {
		return nil
	}
	if cfg.AgentUserDelegation != nil &&
		cfg.AgentUserDelegation.Enabled &&
		strings.TrimSpace(cfg.AgentUserDelegation.RunAsUser) != "" {
		return nil
	}
	return &server.SetupWarning{
		ID:            "run-as-user",
		Title:         "Agent run_as_user delegation is not configured",
		Body:          "Launch policy is not a sandbox. Before letting agents write on macOS, run the setup story and add the generated agent_user_delegation block to .kitsoki.local.yaml.",
		ActionLabel:   "Open setup story",
		ActionCommand: "kitsoki run " + runAsUserSetupStoryRef,
		StoryID:       "run-as-user-setup",
		StoryRef:      runAsUserSetupStoryRef,
	}
}
