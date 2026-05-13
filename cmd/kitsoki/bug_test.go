package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestBugSlug covers the freeform-title → filesystem-slug mapping.
// The function is small but load-bearing — a sloppy slug produces
// unreadable filenames and confuses grep, so the corner cases get
// their own row.
func TestBugSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Hello World", "hello-world"},
		{"  Lots of   spaces  ", "lots-of-spaces"},
		{"Special!! chars$$$ matter#?", "special-chars-matter"},
		{"UPPERCASE", "uppercase"},
		{"123 numbers ok", "123-numbers-ok"},
		{"hyphens-survive", "hyphens-survive"},
		{"trailing-hyphens---", "trailing-hyphens"},
		{"", "bug"},
		{"!!!", "bug"}, // nothing slug-worthy → fallback
		{strings.Repeat("a", 100), strings.Repeat("a", 60)},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			require.Equal(t, tc.want, bugSlug(tc.in))
		})
	}
}

// TestBugFilename composes the slug with a UTC timestamp.
func TestBugFilename(t *testing.T) {
	ts := time.Date(2026, 5, 13, 10, 32, 5, 0, time.UTC)
	got := bugFilename(ts, "TUI hangs on Esc")
	require.Equal(t, "2026-05-13T103205Z-tui-hangs-on-esc.md", got)
}

// TestRenderBugMarkdown_FullPayload asserts the rendered file body
// contains every field, with the YAML front-matter properly quoted
// and the body separated from front-matter by a blank line.
func TestRenderBugMarkdown_FullPayload(t *testing.T) {
	ts := time.Date(2026, 5, 13, 10, 32, 5, 0, time.UTC)
	got := renderBugMarkdown(bugRecord{
		Title:      "TUI hangs on Esc",
		Body:       "Expected the Esc menu to open.\nGot a frozen prompt instead.",
		ReproSteps: []string{"Run cloak", "Press Esc at the foyer"},
		StatePath:  "main.foyer",
		AppID:      "cloak",
		FiledAt:    ts,
	})

	require.Contains(t, got, `title: "TUI hangs on Esc"`)
	require.Contains(t, got, "filed_at: 2026-05-13T10:32:05Z")
	require.Contains(t, got, `app_id: "cloak"`)
	require.Contains(t, got, `state_path: "main.foyer"`)
	require.Contains(t, got, "# TUI hangs on Esc")
	require.Contains(t, got, "Expected the Esc menu to open.")
	require.Contains(t, got, "## Steps to reproduce")
	require.Contains(t, got, "1. Run cloak")
	require.Contains(t, got, "2. Press Esc at the foyer")
}

// TestRenderBugMarkdown_MinimalPayload asserts optional fields are
// omitted from front-matter and "Steps to reproduce" is omitted when
// no repro steps were given.
func TestRenderBugMarkdown_MinimalPayload(t *testing.T) {
	ts := time.Date(2026, 5, 13, 10, 32, 5, 0, time.UTC)
	got := renderBugMarkdown(bugRecord{
		Title:   "minimal",
		Body:    "just the title and body",
		FiledAt: ts,
	})
	require.Contains(t, got, `title: "minimal"`)
	require.NotContains(t, got, "app_id:")
	require.NotContains(t, got, "state_path:")
	require.NotContains(t, got, "## Steps to reproduce")
}

// TestYAMLQuoteLine escapes embedded quotes and newlines so the
// front-matter line stays parseable.
func TestYAMLQuoteLine(t *testing.T) {
	require.Equal(t, `"hello"`, yamlQuoteLine("hello"))
	require.Equal(t, `"says \"hi\""`, yamlQuoteLine(`says "hi"`))
	require.Equal(t, `"line1\nline2"`, yamlQuoteLine("line1\nline2"))
	require.Equal(t, `"path\\to\\file"`, yamlQuoteLine(`path\to\file`))
}

// TestBugCreateCmd_WritesMarkdownFile drives the cobra command
// end-to-end against a temp dir and asserts the produced file's path
// + contents.
func TestBugCreateCmd_WritesMarkdownFile(t *testing.T) {
	tmp := t.TempDir()
	root := newRootCmd()
	root.SetArgs([]string{
		"bug", "create",
		"--title", "Esc menu hang",
		"--body", "Pressing Esc froze the TUI.",
		"--repro", "Run cloak",
		"--repro", "Press Esc",
		"--state-path", "main.foyer",
		"--app-id", "cloak",
		"--app-dir", tmp,
		"--clock-now", "1747130000", // deterministic timestamp
	})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	require.NoError(t, root.Execute())

	relPath := strings.TrimSpace(out.String())
	// 1747130000 Unix seconds = 2025-05-13 09:53:20 UTC.
	require.Equal(t, "bugs/2025-05-13T095320Z-esc-menu-hang.md", relPath)

	abs := filepath.Join(tmp, relPath)
	contents, err := os.ReadFile(abs)
	require.NoError(t, err)

	body := string(contents)
	require.Contains(t, body, `title: "Esc menu hang"`)
	require.Contains(t, body, `app_id: "cloak"`)
	require.Contains(t, body, `state_path: "main.foyer"`)
	require.Contains(t, body, "Pressing Esc froze the TUI.")
	require.Contains(t, body, "1. Run cloak")
	require.Contains(t, body, "2. Press Esc")
}

// TestBugCreateCmd_RequiresTitle asserts the command fails (no file
// written) when --title is missing or whitespace-only.
func TestBugCreateCmd_RequiresTitle(t *testing.T) {
	tmp := t.TempDir()
	root := newRootCmd()
	root.SetArgs([]string{
		"bug", "create",
		"--body", "no title",
		"--app-dir", tmp,
	})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "title")

	// No bugs/ directory should exist if the command bailed before
	// the write.
	_, statErr := os.Stat(filepath.Join(tmp, "bugs"))
	require.True(t, os.IsNotExist(statErr), "no bugs/ should be created on validation failure")
}

// TestBugCreateCmd_RequiresBody asserts the command fails when --body
// is missing.
func TestBugCreateCmd_RequiresBody(t *testing.T) {
	tmp := t.TempDir()
	root := newRootCmd()
	root.SetArgs([]string{
		"bug", "create",
		"--title", "only title",
		"--app-dir", tmp,
	})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "body")
}
