package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/storylauncher"
)

func capsuleWorkerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run sealed Capsule worker jobs inside a prepared executor",
	}
	cmd.AddCommand(capsuleWorkerRunCmd())
	return cmd
}

func capsuleWorkerRunCmd() *cobra.Command {
	var envelopePath, resultPath string
	cmd := &cobra.Command{
		Use:          "run",
		Short:        "Run a sealed Capsule execution envelope and write executor result JSON",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if envelopePath == "" || resultPath == "" {
				return fmt.Errorf("--envelope and --result are required")
			}
			raw, err := os.ReadFile(envelopePath)
			if err != nil {
				return fmt.Errorf("capsule worker: read envelope: %w", err)
			}
			var envelope executor.Envelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				return fmt.Errorf("capsule worker: parse envelope: %w", err)
			}
			sealed, err := executor.Seal(envelope)
			if err != nil {
				return err
			}
			if envelope.Digest != "" && envelope.Digest != sealed.Digest {
				return fmt.Errorf("capsule worker: envelope digest mismatch")
			}
			prepared := executor.Prepared{ID: "worker-" + sealed.Digest[len(sealed.Digest)-12:], Envelope: sealed, Placement: "container", Applied: sealed.Policy}
			verdict, err := storylauncher.Launcher{StoryPath: sealed.StoryPath}.Launch(cmd.Context(), prepared)
			state := executor.CompletionState{Schema: executor.CompletionStateSchema, Outcome: "passed"}
			if err == nil {
				err = ci.ValidateVerdict(verdict, sealed, ci.ResultContract{})
			}
			if err != nil {
				state.Outcome = "failed"
				state.Reason = err.Error()
			}
			verdictRaw, _ := json.Marshal(verdict)
			result := executor.Result{ExitCode: 0, VerdictArtifact: "verdict:worker", VerdictJSON: verdictRaw}
			if state.Outcome != "passed" {
				result.ExitCode = 1
			}
			out := struct {
				Result          executor.Result          `json:"result"`
				CompletionState executor.CompletionState `json:"completion_state"`
			}{Result: result, CompletionState: state}
			encoded, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(resultPath, encoded, 0o600); err != nil {
				return fmt.Errorf("capsule worker: write result: %w", err)
			}
			if state.Outcome != "passed" {
				return fmt.Errorf("capsule worker: %s", state.Reason)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&envelopePath, "envelope", "", "sealed capsule execution envelope JSON")
	cmd.Flags().StringVar(&resultPath, "result", "", "executor result JSON output path")
	return cmd
}
