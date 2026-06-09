// Command starcheck statically validates Starlark source against the canonical
// go.starlark.net runtime. It parses and resolves each file WITHOUT executing
// it, so it is safe to run on untrusted input and has no side effects.
//
// What it catches that a plain "does it run" check cannot:
//
//   - syntax errors (the parser)
//   - undefined names, scope violations, illegal global rebinding, use of a
//     dialect feature the file's options forbid (the resolver)
//   - references to a builtin a given capability *level* does not grant — by
//     restricting the predeclared name set with -predeclared. This is the same
//     "determinism enforced by injection" check an embedder runs at load time:
//     a function that may only see {json, math} will fail to resolve if it
//     mentions `http`.
//
// It never evaluates the module, so resolving cleanly does not prove the code
// is correct — only that it is well-formed and references nothing outside the
// declared environment.
//
// Usage:
//
//	starcheck [flags] <file-or-dir>...
//
// Exit status is 0 if every file parses and resolves, 1 otherwise.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func main() {
	var (
		predeclared = flag.String("predeclared", "", "comma-separated predeclared names available to the module (simulates a capability level); empty = none")
		universe    = flag.Bool("universe", true, "treat the standard Starlark universe (len, range, dict, ...) as available")
		allowWhile  = flag.Bool("while", false, "allow 'while' statements")
		allowTLC    = flag.Bool("toplevel-control", false, "allow if/for/while at top level")
		allowSet    = flag.Bool("set", false, "allow the 'set' builtin")
		allowReassign = flag.Bool("global-reassign", false, "allow reassignment of top-level (global) names")
		allowRecur  = flag.Bool("recursion", false, "allow recursive functions")
		recurse     = flag.Bool("r", false, "recurse into directories, validating every *.star / *.bzl / *.sky file")
		quiet       = flag.Bool("q", false, "only print errors, not the per-file OK lines")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: starcheck [flags] <file-or-dir>...\n\n")
		fmt.Fprintf(os.Stderr, "Parses and resolves Starlark without executing it. Exit 0 = all clean.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	opts := &syntax.FileOptions{
		While:           *allowWhile,
		TopLevelControl: *allowTLC,
		Set:             *allowSet,
		GlobalReassign:  *allowReassign,
		Recursion:       *allowRecur,
	}

	// The set of names the module may reference without binding them itself.
	pre := map[string]bool{}
	for _, n := range strings.Split(*predeclared, ",") {
		if n = strings.TrimSpace(n); n != "" {
			pre[n] = true
		}
	}
	isPredeclared := func(name string) bool { return pre[name] }
	isUniversal := func(name string) bool {
		if !*universe {
			return false
		}
		_, ok := starlark.Universe[name]
		return ok
	}

	files, err := collect(flag.Args(), *recurse)
	if err != nil {
		fmt.Fprintln(os.Stderr, "starcheck:", err)
		os.Exit(2)
	}

	failed := false
	for _, path := range files {
		if errs := check(opts, path, isPredeclared, isUniversal); len(errs) > 0 {
			failed = true
			for _, e := range errs {
				fmt.Fprintln(os.Stderr, e)
			}
		} else if !*quiet {
			fmt.Printf("OK   %s\n", path)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// check parses then resolves one file, returning a slice of formatted
// diagnostics (empty if the file is clean). It never executes the code.
func check(opts *syntax.FileOptions, path string, isPredeclared, isUniversal func(string) bool) []string {
	src, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", path, err)}
	}
	f, err := opts.Parse(path, src, 0)
	if err != nil {
		return formatErr(err)
	}
	if err := resolve.File(f, isPredeclared, isUniversal); err != nil {
		return formatErr(err)
	}
	return nil
}

// formatErr renders the parser's and resolver's error shapes — both a single
// syntax.Error and the resolver's multi-error ErrorList — as one line each.
func formatErr(err error) []string {
	switch e := err.(type) {
	case resolve.ErrorList:
		out := make([]string, 0, len(e))
		for _, re := range e {
			out = append(out, fmt.Sprintf("%s: %s", re.Pos, re.Msg))
		}
		return out
	case syntax.Error:
		return []string{fmt.Sprintf("%s: %s", e.Pos, e.Msg)}
	default:
		return []string{err.Error()}
	}
}

// collect expands the given paths into a sorted file list. A directory is
// walked for Starlark files only when -r is set; otherwise it is an error.
func collect(paths []string, recurse bool) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		if !recurse {
			return nil, fmt.Errorf("%s is a directory (pass -r to recurse)", p)
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && isStarlarkFile(path) {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func isStarlarkFile(path string) bool {
	switch filepath.Ext(path) {
	case ".star", ".bzl", ".sky":
		return true
	}
	return false
}
