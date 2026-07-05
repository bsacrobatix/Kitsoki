// Command group `kitsoki kit` — S2 (.context/kits-implementation-plan.md):
// resolve, lock, list, and (minimally) verify kit dependencies, plus the
// `kit dev` local-checkout override for contributing to a kit.
//
// `kit verify` and `kit update` are deliberately minimal stubs here — S4
// (conformance) and S7 (lifecycle) build them out for real. They exist as
// real CLI verbs with honest, documented behaviour rather than TODO-only
// placeholders: verify does a real (if narrow) hash-consistency check;
// update prints that it isn't implemented yet.
package main

import (
	"context"
	"fmt"
	"os"
	stdpath "path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/app"
	"kitsoki/internal/kitdev"
	"kitsoki/internal/kitgit"
	"kitsoki/internal/kitlock"
	"kitsoki/internal/kitver"
)

// kitCmd groups the kit-dependency lifecycle commands.
func kitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kit",
		Short: "Manage kit dependencies (add/list/verify/update/dev)",
		Long: `Resolve and lock kit dependencies against a project's .kitsoki/kits.lock.

A kit source is one of:
  @kitsoki/<name>              a story in the kitsoki repo or embedded library
  git+https://host/org/repo@ref  a git remote, fetched into a content-addressed
                                cache (ref may be a tag, branch, or commit SHA)
  ./relative/path or /absolute/path   a local checkout

'kit add' resolves a source, checks any --version constraint against the
resolved kit's own app.version:, and records (source, version, commit,
tree-hash) in the lockfile. 'kit list' reads it back. 'kit verify' does a
narrow real check (lockfile present, resolved content still hashes to what's
locked) — full contract/conformance checking is S4. 'kit update' is a stub —
semver-gated re-resolution is S7. 'kit dev' overrides one kit's resolution
with a local checkout for development, generalizing the --kitsoki-repo /
$KITSOKI_REPO override to a single named kit (internal/kitdev).`,
	}
	cmd.AddCommand(kitAddCmd())
	cmd.AddCommand(kitListCmd())
	cmd.AddCommand(kitVerifyCmd())
	cmd.AddCommand(kitUpdateCmd())
	cmd.AddCommand(kitDevCmd())
	return cmd
}

func kitAddCmd() *cobra.Command {
	var (
		target     string
		name       string
		constraint string
	)
	cmd := &cobra.Command{
		Use:   "add <source>",
		Short: "Resolve a kit source and add/update its lockfile entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := args[0]
			targetAbs, err := filepath.Abs(target)
			if err != nil {
				return fmt.Errorf("resolve --target: %w", err)
			}
			kitName := name
			if kitName == "" {
				kitName = deriveKitName(source)
				if kitName == "" {
					return fmt.Errorf("cannot derive a kit name from %q; pass --name", source)
				}
			}

			entry, err := resolveKitEntry(cmd.Context(), source, targetAbs, constraint)
			if err != nil {
				return err
			}

			lockPath := kitlock.Path(targetAbs)
			lf, err := kitlock.Load(lockPath)
			if err != nil {
				return err
			}
			lf.Kits[kitName] = entry
			if err := kitlock.Save(lockPath, lf); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "locked %s@%s\n", kitName, displayVersion(entry.Version))
			fmt.Fprintf(out, "  source:    %s\n", entry.Source)
			if entry.Commit != "" {
				fmt.Fprintf(out, "  commit:    %s\n", entry.Commit)
			}
			fmt.Fprintf(out, "  tree_hash: %s\n", entry.TreeHash)
			fmt.Fprintf(out, "  lockfile:  %s\n", lockPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", ".", "project root the lockfile is written under (.kitsoki/kits.lock)")
	cmd.Flags().StringVar(&name, "name", "", "kit name to lock under (default: derived from the source)")
	cmd.Flags().StringVar(&constraint, "version", "", "version constraint the resolved kit's app.version: must satisfy (e.g. ^1.2.0)")
	return cmd
}

func kitListCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List locked kit dependencies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			targetAbs, err := filepath.Abs(target)
			if err != nil {
				return fmt.Errorf("resolve --target: %w", err)
			}
			lockPath := kitlock.Path(targetAbs)
			out := cmd.OutOrStdout()
			if !kitlock.Exists(lockPath) {
				fmt.Fprintf(out, "no lockfile at %s — run `kitsoki kit add` first\n", lockPath)
				return nil
			}
			lf, err := kitlock.Load(lockPath)
			if err != nil {
				return err
			}
			if len(lf.Kits) == 0 {
				fmt.Fprintln(out, "no kits locked yet")
				return nil
			}
			for _, kn := range lf.SortedNames() {
				e := lf.Kits[kn]
				fmt.Fprintf(out, "%s@%s\n", kn, displayVersion(e.Version))
				fmt.Fprintf(out, "  source:    %s\n", e.Source)
				if e.Commit != "" {
					fmt.Fprintf(out, "  commit:    %s\n", e.Commit)
				}
				fmt.Fprintf(out, "  tree_hash: %s\n", e.TreeHash)
				if dev := kitdev.Resolve(kn); dev != "" {
					fmt.Fprintf(out, "  dev override: %s\n", dev)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", ".", "project root the lockfile is read from")
	return cmd
}

func kitVerifyCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check the lockfile exists and resolved kits still hash-match (minimal stub — full contract checks are S4)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			targetAbs, err := filepath.Abs(target)
			if err != nil {
				return fmt.Errorf("resolve --target: %w", err)
			}
			lockPath := kitlock.Path(targetAbs)
			out := cmd.OutOrStdout()
			if !kitlock.Exists(lockPath) {
				return fmt.Errorf("no lockfile at %s — run `kitsoki kit add` first", lockPath)
			}
			lf, err := kitlock.Load(lockPath)
			if err != nil {
				return err
			}
			if len(lf.Kits) == 0 {
				fmt.Fprintln(out, "lockfile present, no kits locked — nothing to verify")
				return nil
			}

			var mismatches []string
			for _, kn := range lf.SortedNames() {
				e := lf.Kits[kn]
				status, verifyErr := verifyEntry(kn, e, targetAbs)
				if verifyErr != nil {
					mismatches = append(mismatches, fmt.Sprintf("%s: %v", kn, verifyErr))
					fmt.Fprintf(out, "%s: ERROR (%v)\n", kn, verifyErr)
					continue
				}
				if status {
					fmt.Fprintf(out, "%s: OK\n", kn)
				} else {
					mismatches = append(mismatches, kn)
					fmt.Fprintf(out, "%s: MISMATCH (resolved content no longer matches the lockfile)\n", kn)
				}
			}
			if len(mismatches) > 0 {
				return fmt.Errorf("kit verify: %d kit(s) failed: %s", len(mismatches), strings.Join(mismatches, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", ".", "project root the lockfile is read from")
	return cmd
}

// verifyEntry re-derives the tree hash for a locked kit and compares it to
// what's recorded. For a git-sourced kit it checks the (offline) content
// cache only — it does not re-fetch, matching the "narrow real check, not
// full conformance" scope this stub commits to.
func verifyEntry(name string, e *kitlock.Entry, targetDir string) (ok bool, err error) {
	if e.Commit != "" {
		res, cached, cacheErr := kitgit.CachedResult(e.Commit)
		if cacheErr != nil {
			return false, cacheErr
		}
		if !cached {
			return false, fmt.Errorf("commit %s not present in the local git cache (re-run `kitsoki kit add %s`)", e.Commit, e.Source)
		}
		return res.TreeHash == e.TreeHash, nil
	}
	appPath, resolveErr := app.ResolveSource(e.Source, targetDir, buildImportResolver())
	if resolveErr != nil {
		return false, resolveErr
	}
	treeHash, hashErr := kitgit.DirTreeHash(filepath.Dir(appPath))
	if hashErr != nil {
		return false, hashErr
	}
	return treeHash == e.TreeHash, nil
}

func kitUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update [name]",
		Short: "Re-resolve a locked kit against its version constraint (not yet implemented)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "kit update: not yet implemented — semver-gated re-resolution + migration worklist land in S7 (lifecycle). For now, re-run `kitsoki kit add` with the new source/version to update a lock entry.")
			return nil
		},
	}
}

func kitDevCmd() *cobra.Command {
	var (
		path  string
		clear bool
	)
	cmd := &cobra.Command{
		Use:   "dev <name>",
		Short: "Override a kit's resolution with a local checkout for development",
		Long: `Generalizes the --kitsoki-repo / $KITSOKI_REPO override (which points EVERY
@kitsoki/<name> import at one repo-wide checkout) into a per-kit local-path
override: every resolution of <name> — whether its declared source is
@kitsoki/<name> or a git+ source recorded under that name in the lockfile —
resolves against <path>/app.yaml instead, until 'kitsoki kit dev <name>
--clear'. Persisted under ~/.kitsoki/kit-dev/ (internal/kitdev), mirroring
how ~/.kitsoki/repo persists the repo-wide override (internal/kitrepo).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			out := cmd.OutOrStdout()
			if clear {
				if err := kitdev.Clear(name); err != nil {
					return err
				}
				fmt.Fprintf(out, "cleared dev override for %s\n", name)
				return nil
			}
			if path == "" {
				return fmt.Errorf("--path is required (or pass --clear to remove an existing override)")
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolve --path: %w", err)
			}
			if _, statErr := os.Stat(filepath.Join(abs, "app.yaml")); statErr != nil {
				return fmt.Errorf("--path %s: no app.yaml found there: %w", abs, statErr)
			}
			if err := kitdev.Set(name, abs); err != nil {
				return err
			}
			fmt.Fprintf(out, "dev override: %s -> %s\n", name, abs)
			fmt.Fprintf(out, "every @kitsoki/%s import (or a git+ source locked under %q) now resolves here until `kitsoki kit dev %s --clear`.\n", name, name, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "local checkout path (must contain app.yaml)")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the dev override for <name>")
	return cmd
}

// resolveKitEntry resolves source (git+ or any other tier) and builds the
// lockfile Entry it should produce, validating an optional version
// constraint along the way.
func resolveKitEntry(ctx context.Context, source, importerDir, constraint string) (*kitlock.Entry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if url, ref, ok := kitgit.ParseSource(source); ok {
		res, err := kitgit.Materialize(ctx, kitgit.DefaultRunner, url, ref)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", source, err)
		}
		appPath := filepath.Join(res.Root, "app.yaml")
		def, err := app.LoadWithResolver(appPath, nil, buildImportResolver())
		if err != nil {
			return nil, fmt.Errorf("load resolved kit manifest %s: %w", appPath, err)
		}
		if err := checkConstraint(def.App.Version, constraint); err != nil {
			return nil, err
		}
		return &kitlock.Entry{Source: source, Version: def.App.Version, Commit: res.Commit, TreeHash: res.TreeHash}, nil
	}

	appPath, err := app.ResolveSource(source, importerDir, buildImportResolver())
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", source, err)
	}
	def, err := app.LoadWithResolver(appPath, nil, buildImportResolver())
	if err != nil {
		return nil, fmt.Errorf("load resolved kit manifest %s: %w", appPath, err)
	}
	if err := checkConstraint(def.App.Version, constraint); err != nil {
		return nil, err
	}
	treeHash, err := kitgit.DirTreeHash(filepath.Dir(appPath))
	if err != nil {
		return nil, fmt.Errorf("hash resolved kit directory: %w", err)
	}
	return &kitlock.Entry{Source: source, Version: def.App.Version, TreeHash: treeHash}, nil
}

func checkConstraint(version, constraint string) error {
	if constraint == "" {
		return nil
	}
	ok, err := kitver.Satisfies(version, constraint)
	if err != nil {
		return fmt.Errorf("version constraint %q: %w", constraint, err)
	}
	if !ok {
		return fmt.Errorf("resolved version %q does not satisfy constraint %q", version, constraint)
	}
	return nil
}

// deriveKitName picks a reasonable default lock-entry name from a source
// string when --name isn't given.
func deriveKitName(source string) string {
	if url, _, ok := kitgit.ParseSource(source); ok {
		base := stdpath.Base(url)
		return strings.TrimSuffix(base, ".git")
	}
	if strings.HasPrefix(source, "@kitsoki/") {
		return strings.TrimPrefix(source, "@kitsoki/")
	}
	trimmed := strings.TrimRight(source, "/")
	base := filepath.Base(trimmed)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func displayVersion(v string) string {
	if v == "" {
		return "(no app.version declared)"
	}
	return v
}
