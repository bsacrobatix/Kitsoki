package agents

import _ "embed"

//go:embed kitsoki_improver.md
var kitsokiImproverPrompt string

// kitsokiImprover returns the builtin kitsoki-improver Agent: a read-only
// continuous-improvement reviewer for kitsoki engine runs. It is surfaced
// through the builtin `kitsoki.improve` meta mode.
func kitsokiImprover() Agent {
	return Agent{
		Name:         NameKitsokiImprover,
		SystemPrompt: kitsokiImproverPrompt,
		Model:        "",
		Tools: []string{
			"Read",
			"Glob",
			"Grep",
		},
		DefaultCwd: "${KITSOKI_REPO}",
	}
}
