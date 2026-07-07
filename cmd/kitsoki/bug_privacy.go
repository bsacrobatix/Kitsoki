package main

import (
	"strings"

	"kitsoki/internal/bugprivacy"
	"kitsoki/internal/host"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/webconfig"
)

func bugPrivacyCheckerFromConfig(cfg webconfig.WebConfig, workingDir string) bugprivacy.Checker {
	profiles, _ := harnessProfilesFromConfig(cfg)
	ladder := cfg.HarnessLadder.ToHostLadderConfig()
	if !ladder.Enabled() {
		return nil
	}
	return host.NewBugPrivacyAgentChecker(ladder, bugPrivacyProviders(profiles), workingDir)
}

func bugPrivacyProviders(profiles map[string]orchestrator.HarnessProfile) map[string]host.Provider {
	if len(profiles) == 0 {
		return nil
	}
	out := make(map[string]host.Provider, len(profiles))
	for name, p := range profiles {
		provider := host.Provider{
			Backend: strings.TrimSpace(p.Backend),
			Model:   strings.TrimSpace(p.Model),
			Effort:  strings.TrimSpace(p.Effort),
			Env:     p.Env,
		}
		out[name] = provider
	}
	return out
}
