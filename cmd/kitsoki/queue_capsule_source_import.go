package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/sourceartifact"
	"kitsoki/internal/objectstore"
)

// queueImportCapsuleSourceCmd is the streaming bridge for a read-only
// controller.  The controller can pipe a Git bundle over its authenticated
// transport; only this credentialed host writes the durable object-store
// artifact.  The artifact is keyed by registered Capsule identity, never by
// an inferred local-main checkout.
func queueImportCapsuleSourceCmd() *cobra.Command {
	var projectID, workspace, definition, branch, head, bucketURL, keyEnv, secretEnv, prefix string
	var generation uint64
	cmd := &cobra.Command{Use: "import-capsule-source", Short: "Store a streamed registered Capsule source as an immutable host-fetchable artifact", SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		identity := sourceartifact.Identity{ProjectID: projectID, WorkspaceID: workspace, WorkspaceGen: generation, DefinitionDigest: definition, Branch: branch, Head: head}
		if !fullGitSHA(head) {
			return fmt.Errorf("queue import-capsule-source: --head must be a lowercase full Git SHA")
		}
		raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), executor.DefaultMaxBundleSize+1))
		if err != nil {
			return err
		}
		if int64(len(raw)) == 0 || int64(len(raw)) > executor.DefaultMaxBundleSize {
			return fmt.Errorf("queue import-capsule-source: bundle exceeds allowed size")
		}
		if err := verifyImportedBundle(cmd.Context(), raw, head); err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		bundle := executor.SourceBundle{Schema: executor.SourceBundleSchema, Format: executor.SourceBundleFormat, Head: head, Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(raw)), Data: raw}
		manifest, err := sourceartifact.New(identity, bundle, prefix)
		if err != nil {
			return err
		}
		config, err := objectstore.ParseBucketURL(bucketURL)
		if err != nil {
			return err
		}
		config.KeyEnv, config.SecretEnv = keyEnv, secretEnv
		store, err := objectstore.NewSpaces(config, nil)
		if err != nil {
			return err
		}
		if err := sourceartifact.Publish(cmd.Context(), store, manifest, bundle); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(manifest)
	}}
	cmd.Flags().StringVar(&projectID, "project-id", "", "registered Capsule project identity")
	cmd.Flags().StringVar(&workspace, "workspace", "", "registered Capsule id")
	cmd.Flags().Uint64Var(&generation, "generation", 0, "registered Capsule generation")
	cmd.Flags().StringVar(&definition, "definition-digest", "", "registered Capsule definition digest")
	cmd.Flags().StringVar(&branch, "branch", "", "registered Capsule branch")
	cmd.Flags().StringVar(&head, "head", "", "registered Capsule immutable HEAD")
	cmd.Flags().StringVar(&bucketURL, "bucket-url", "", "host object-store bucket URL")
	cmd.Flags().StringVar(&keyEnv, "bucket-key-env", "KITSOKI_WORKER_OUTPUTS_ACCESS_KEY", "host-only object-store access-key environment name")
	cmd.Flags().StringVar(&secretEnv, "bucket-secret-env", "KITSOKI_WORKER_OUTPUTS_SECRET_KEY", "host-only object-store secret environment name")
	cmd.Flags().StringVar(&prefix, "prefix", "capsule-registered-sources", "immutable source artifact object prefix")
	for _, name := range []string{"project-id", "workspace", "generation", "definition-digest", "branch", "head", "bucket-url"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func verifyImportedBundle(ctx context.Context, raw []byte, head string) error {
	dir, err := os.MkdirTemp("", "kitsoki-imported-capsule-source-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "source.bundle")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "git", "bundle", "list-heads", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("queue import-capsule-source: verify Git bundle: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == head {
			return nil
		}
	}
	return fmt.Errorf("queue import-capsule-source: bundle does not contain registered HEAD %s", head)
}
