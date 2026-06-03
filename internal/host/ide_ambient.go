// Package host — ambient editor-context seam for oracle prompts.
//
// When the TUI holds a live IDE link it reads the active selection at
// turn-submit and threads it onto the turn ctx with WithIDEAmbient. The oracle
// handlers (oracle_ask.go, oracle_ask_with_mcp.go, oracle_task.go) merge it into
// the prompt template scope under the reserved `args.ide` key, so a story prompt
// can reference `{{ args.ide.file }}` / `{{ args.ide.selection }}` without the
// orchestrator having to plumb editor state through world slots. When no link is
// connected (CLI one-shots, flow tests, headless runs, or `/ide` not connected)
// no ambient value is set and the template scope is byte-identical to today.
//
// The capture is read-at-submit and gated TUI-side on a deny list; the echo line
// the operator sees (`⧉ Selected N lines from <file>`) is the source of truth for
// the exact range that rode the turn. See docs/tui/README.md ("Editor awareness:
// /ide") and docs/hosts.md ("host.ide.*").
package host

import "context"

// IDEAmbient is the editor context captured at turn-submit and exposed to
// prompt templates as `args.ide`. Fields are the selection's source file and
// text plus the human-readable range echoed in the transcript; all are empty
// when nothing was selected. It is intentionally small — only what a prompt
// would reference, never the raw diagnostics blob (that rides host.ide.* verbs).
type IDEAmbient struct {
	// File is the absolute path of the file the selection was read from.
	File string `json:"file"`
	// Selection is the selected text exactly as the editor returned it.
	Selection string `json:"selection"`
	// Lines is the line count attributed to the selection (the N in the
	// `⧉ Selected N lines from <file>` echo).
	Lines int `json:"lines"`
	// Range is a compact, human-readable description of the read range
	// (e.g. "12:0-24:8") mirroring the echo's source-of-truth promise.
	Range string `json:"range"`
}

// asMap renders the ambient context as the map a pongo2 template sees under
// `args.ide`. Kept in one place so the template-facing key names stay stable.
func (a IDEAmbient) asMap() map[string]any {
	return map[string]any{
		"file":      a.File,
		"selection": a.Selection,
		"lines":     a.Lines,
		"range":     a.Range,
	}
}

// ideAmbientKey is the unexported context key for the injected ambient context.
type ideAmbientKey struct{}

// WithIDEAmbient injects the turn's ambient editor context into ctx so the
// oracle handlers expose it as `args.ide`. An empty IDEAmbient (no File) is a
// no-op — the scope stays byte-identical to a turn with no editor context, so
// the deny-list and disconnected paths never need a separate ctx branch.
func WithIDEAmbient(ctx context.Context, a IDEAmbient) context.Context {
	if a.File == "" {
		return ctx
	}
	return context.WithValue(ctx, ideAmbientKey{}, a)
}

// IDEAmbientFromCtx returns the ambient editor context previously injected with
// WithIDEAmbient, and ok=false when none was injected.
func IDEAmbientFromCtx(ctx context.Context) (IDEAmbient, bool) {
	a, ok := ctx.Value(ideAmbientKey{}).(IDEAmbient)
	return a, ok
}

// mergeIDEAmbient returns templateArgs with the ambient editor context added
// under the reserved `ide` key when one is present in ctx and the author has
// not already bound `ide` themselves (an explicit author binding wins). It never
// mutates the caller's map: when there is nothing to merge it returns the input
// unchanged, and otherwise it shallow-copies before adding the key. This is the
// single seam every oracle prompt path calls so `{{ args.ide.* }}` resolves
// consistently.
func mergeIDEAmbient(ctx context.Context, templateArgs map[string]any) map[string]any {
	amb, ok := IDEAmbientFromCtx(ctx)
	if !ok {
		return templateArgs
	}
	if _, taken := templateArgs["ide"]; taken {
		return templateArgs
	}
	merged := make(map[string]any, len(templateArgs)+1)
	for k, v := range templateArgs {
		merged[k] = v
	}
	merged["ide"] = amb.asMap()
	return merged
}
