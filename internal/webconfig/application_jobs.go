package webconfig

import (
	"fmt"

	"kitsoki/internal/applicationjob"
)

const (
	maxApplicationJobCallers   = 64
	maxApplicationJobTemplates = 64
)

func (cfg *WebConfig) resolveStoryApplicationJobs() error {
	if len(cfg.StoryApplicationJobs) > maxApplicationJobCallers {
		return fmt.Errorf(
			"story_application_jobs has %d callers, exceeds %d",
			len(cfg.StoryApplicationJobs),
			maxApplicationJobCallers,
		)
	}
	for caller, templates := range cfg.StoryApplicationJobs {
		if err := configIdentity("caller application id", caller); err != nil {
			return fmt.Errorf("story_application_jobs: %w", err)
		}
		if len(templates) == 0 || len(templates) > maxApplicationJobTemplates {
			return fmt.Errorf(
				"story_application_jobs.%s template count must be within 1..%d",
				caller,
				maxApplicationJobTemplates,
			)
		}
		for name, template := range templates {
			if err := applicationjob.ValidateTemplate(name, template); err != nil {
				return fmt.Errorf("story_application_jobs.%s.%s: %w", caller, name, err)
			}
		}
	}
	return nil
}
