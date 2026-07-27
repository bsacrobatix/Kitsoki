package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

// queueCapsulePromotionExecutorCmd is deliberately a host-side command.  Its
// caller identifies a registered, immutable Capsule; the credentialed host
// then owns CI, source-bundle sealing, object publication, and authenticated
// queue admission.  A workstation never manufactures a receipt, handoff, or
// bundle and never needs the admission or object-store credentials.
func queueCapsulePromotionExecutorCmd() *cobra.Command {
	var project, workspace, pipeline, target, gate, message string
	var admissionURL, tokenEnv, bucketURL, keyEnv, secretEnv, targetBase, train, statusCommand string
	cmd := &cobra.Command{
		Use:          "execute-capsule-promotion",
		Short:        "Hosted executor: run a registered Capsule through CI and authenticated integration admission",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := capsulePromoteOptions{
				ProjectRoot: project, WorkspaceID: workspace, Pipeline: pipeline,
				TargetRef: target, GateCommand: gate, Message: message,
				RemoteAdmission: remoteAdmissionOptions{
					URL: admissionURL, TokenEnv: tokenEnv, BucketURL: bucketURL,
					KeyEnv: keyEnv, SecretEnv: secretEnv, TargetBaseSHA: targetBase,
					TrainID: train, StatusCommand: statusCommand,
				},
			}
			if err := validateHostedCapsulePromotion(opts); err != nil {
				return err
			}
			result, err := runCapsulePromote(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if result.Status != "remote_admitted" || result.RemoteAdmission == nil {
				return fmt.Errorf("queue execute-capsule-promotion: expected durable remote admission, got status %q", result.Status)
			}
			return capsuleWorkspaceWrite(cmd, result, true)
		},
	}
	cmd.Flags().StringVar(&project, "project", ".", "host project root containing the registered Capsule")
	cmd.Flags().StringVar(&workspace, "workspace", "", "registered managed Capsule id; source is resolved only on this host")
	cmd.Flags().StringVar(&pipeline, "pipeline", "change", "Capsule CI pipeline to run on this host")
	cmd.Flags().StringVar(&target, "target", "", "explicit integration/* destination ref")
	cmd.Flags().StringVar(&gate, "gate", "git diff --check", "deterministic queue gate command")
	cmd.Flags().StringVar(&message, "message", "hosted capsule promotion candidate", "snapshot commit message when the registered Capsule is dirty")
	cmd.Flags().StringVar(&admissionURL, "admission-url", "http://127.0.0.1:7444", "loopback-only hosted queue-admission URL")
	cmd.Flags().StringVar(&tokenEnv, "admission-token-env", "KITSOKI_QUEUE_ADMISSION_TOKEN", "host-only environment variable holding the admission bearer")
	cmd.Flags().StringVar(&bucketURL, "bucket-url", "", "Spaces/S3 bucket URL for sealed host-produced promotion objects")
	cmd.Flags().StringVar(&keyEnv, "bucket-key-env", "KITSOKI_WORKER_OUTPUTS_ACCESS_KEY", "host-only environment variable holding the bucket access key")
	cmd.Flags().StringVar(&secretEnv, "bucket-secret-env", "KITSOKI_WORKER_OUTPUTS_SECRET_KEY", "host-only environment variable holding the bucket secret")
	cmd.Flags().StringVar(&targetBase, "target-base-sha", "", "exact integration target base SHA observed by the caller")
	cmd.Flags().StringVar(&train, "train", "", "immutable integration train identity")
	cmd.Flags().StringVar(&statusCommand, "status-command", "", "exact hosted `kitsoki queue status ... --json` command")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("target")
	_ = cmd.MarkFlagRequired("bucket-url")
	_ = cmd.MarkFlagRequired("target-base-sha")
	_ = cmd.MarkFlagRequired("train")
	_ = cmd.MarkFlagRequired("status-command")
	return cmd
}

func validateHostedCapsulePromotion(opts capsulePromoteOptions) error {
	if strings.TrimSpace(opts.WorkspaceID) == "" {
		return fmt.Errorf("queue execute-capsule-promotion: --workspace is required")
	}
	if !strings.HasPrefix(opts.TargetRef, "integration/") || strings.TrimPrefix(opts.TargetRef, "integration/") == "" {
		return fmt.Errorf("queue execute-capsule-promotion: target must be an explicit integration/* ref")
	}
	if !fullGitSHA(opts.RemoteAdmission.TargetBaseSHA) {
		return fmt.Errorf("queue execute-capsule-promotion: --target-base-sha must be a lowercase full Git SHA")
	}
	if !loopbackHTTPURL(opts.RemoteAdmission.URL) {
		return fmt.Errorf("queue execute-capsule-promotion: --admission-url must be a loopback http(s) URL")
	}
	return nil
}

func loopbackHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
