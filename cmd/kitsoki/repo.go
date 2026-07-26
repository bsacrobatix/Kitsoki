package main

// kitsoki repo: serve bare git repositories over smart HTTP (internal/gitserve)
// and back them up as incremental bundle chains in object storage
// (internal/gitbackup). Everything here is additive glue: the command group
// wires the two seams together without changing any existing workspace,
// queue, or promotion behavior.

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"kitsoki/internal/gitbackup"
	"kitsoki/internal/gitserve"
	"kitsoki/internal/objectstore"
)

func repoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Serve, back up, and restore bare git repositories",
	}
	cmd.AddCommand(repoServeCmd())
	cmd.AddCommand(repoInitCmd())
	cmd.AddCommand(repoBackupCmd())
	cmd.AddCommand(repoRestoreCmd())
	return cmd
}

// repoNewObjectStore builds the object store used by every repo subcommand.
// It is a package-level var so tests can substitute objectstore.NewFake()
// without real bucket credentials (same seam pattern as newPoolObjectStore in
// internal/capsule/ci).
var repoNewObjectStore = func(bucketURL, keyEnv, secretEnv string) (objectstore.Store, error) {
	cfg, err := objectstore.ParseBucketURL(bucketURL)
	if err != nil {
		return nil, err
	}
	cfg.KeyEnv, cfg.SecretEnv = keyEnv, secretEnv
	return objectstore.NewSpaces(cfg, nil)
}

// repoBucketFlags is the shared object-store flag surface, matching the
// existing --bucket-url / --bucket-key-env / --bucket-secret-env convention
// (see vmpool_roundtrip.go).
type repoBucketFlags struct {
	bucketURL string
	keyEnv    string
	secretEnv string
}

// register adds the bucket flags. required marks --bucket-url mandatory
// (one-shot backup/restore); serve leaves it optional and validates it only
// when --backup is set.
func (f *repoBucketFlags) register(cmd *cobra.Command, required bool) {
	cmd.Flags().StringVar(&f.bucketURL, "bucket-url", "", "virtual-hosted bucket URL")
	cmd.Flags().StringVar(&f.keyEnv, "bucket-key-env", "DO_SPACES_KEY_ID", "env var holding the bucket access key id")
	cmd.Flags().StringVar(&f.secretEnv, "bucket-secret-env", "DO_KITSOKI_TEST_API_KEY", "env var holding the bucket secret")
	if required {
		_ = cmd.MarkFlagRequired("bucket-url")
	}
}

func (f *repoBucketFlags) store() (objectstore.Store, error) {
	return repoNewObjectStore(f.bucketURL, f.keyEnv, f.secretEnv)
}

func repoInitCmd() *cobra.Command {
	var root, name string
	cmd := &cobra.Command{
		Use:          "init",
		Short:        "Create a new bare repository under a serving root",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := gitserve.InitBare(root, name)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "serving root directory (required)")
	cmd.Flags().StringVar(&name, "name", "", "repository name, e.g. team/project (required; .git appended when missing)")
	_ = cmd.MarkFlagRequired("root")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func repoBackupCmd() *cobra.Command {
	var bucket repoBucketFlags
	var repoPath, prefix string
	var full bool
	cmd := &cobra.Command{
		Use:          "backup",
		Short:        "Back up one repository as a bundle chain in object storage",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := bucket.store()
			if err != nil {
				return err
			}
			backer, err := gitbackup.Open(store, prefix, repoPath)
			if err != nil {
				return err
			}
			var result gitbackup.Result
			if full {
				result, err = backer.BackupFull(cmd.Context())
			} else {
				result, err = backer.BackupIncremental(cmd.Context())
			}
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(repoBackupOutput(result))
		},
	}
	bucket.register(cmd, true)
	cmd.Flags().StringVar(&repoPath, "repo", "", "path to the git repository to back up (required)")
	cmd.Flags().StringVar(&prefix, "prefix", "", "object-store prefix for the bundle chain (required)")
	cmd.Flags().BoolVar(&full, "full", false, "force a full backup (new compaction point) instead of an increment")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("prefix")
	return cmd
}

// repoBackupOutput flattens a gitbackup.Result into the stable JSON shape
// printed by `kitsoki repo backup`.
func repoBackupOutput(result gitbackup.Result) map[string]any {
	out := map[string]any{
		"ok":         true,
		"skipped":    result.Skipped,
		"generation": result.Manifest.Generation,
		"entries":    len(result.Manifest.Entries),
	}
	if !result.Skipped {
		out["seq"] = result.Entry.Seq
		out["kind"] = result.Entry.Kind
		if result.Entry.Key != "" {
			out["key"] = result.Entry.Key
			out["size"] = result.Entry.Size
		}
	}
	return out
}

func repoRestoreCmd() *cobra.Command {
	var bucket repoBucketFlags
	var prefix, target string
	cmd := &cobra.Command{
		Use:          "restore",
		Short:        "Restore a backed-up repository chain into a fresh directory",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := bucket.store()
			if err != nil {
				return err
			}
			manifest, err := gitbackup.Restore(cmd.Context(), store, prefix, target)
			if err != nil {
				return err
			}
			tip, _ := manifest.Tip()
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
				"ok":         true,
				"target":     target,
				"generation": manifest.Generation,
				"entries":    len(manifest.Entries),
				"tip_seq":    tip.Seq,
				"refs":       len(tip.Refs),
			})
		},
	}
	bucket.register(cmd, true)
	cmd.Flags().StringVar(&prefix, "prefix", "", "object-store prefix of the bundle chain (required)")
	cmd.Flags().StringVar(&target, "target", "", "directory to restore into (must not be an existing non-empty repo) (required)")
	_ = cmd.MarkFlagRequired("prefix")
	_ = cmd.MarkFlagRequired("target")
	return cmd
}
