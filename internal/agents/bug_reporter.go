package agents

import _ "embed"

//go:embed bug_reporter.md
var bugReporterPrompt string

// bugReporter returns the builtin bug-reporter Agent: gathers
// reproduction context and files a bug report by invoking the
// `kitsoki bug create` CLI subcommand. Surfaced through the builtin
// `bug` meta mode.
//
// Tool surface (informational — every claude subprocess currently
// runs with --permission-mode bypassPermissions, so this list
// documents intent for prompt authors and code reviewers rather
// than acting as a runtime gate):
//
//   - Read + Grep — the agent's prompt directs it to read the
//     [context]-supplied `trace_file` to reconstruct what happened
//     instead of interrogating the user about it.
//   - Bash(kitsoki bug create*) — the actual filing step. Pattern
//     form documents the only Bash invocation the agent should
//     produce; the prompt teaches it the command explicitly.
//
// No Edit/Write — bug-reporter is read-only on the filesystem (by
// agreement, not enforcement); the only side effect is the markdown
// file the CLI subcommand writes.
//
// The "host.bugs.create" abstraction from the meta-mode proposal §1
// is realised as this CLI subcommand rather than a kitsoki-internal
// MCP tool — same observable behaviour, far smaller surface to
// wire up.
func bugReporter() Agent {
	return Agent{
		Name:         "bug-reporter",
		SystemPrompt: bugReporterPrompt,
		Model:        "",
		Tools: []string{
			"Read",
			"Grep",
			"Bash(kitsoki bug create*)",
		},
		DefaultCwd: "",
	}
}
