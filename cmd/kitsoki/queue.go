package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/receipt"
)

func queueCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "queue", Short: "Submit verified candidates to the Capsule merge queue"}
	cmd.AddCommand(queueSubmitCmd(), queueListCmd())
	return cmd
}
func queueSubmitCmd() *cobra.Command {
	var project, branch, sha, receiptPath, backend string
	var paths []string
	cmd := &cobra.Command{Use: "submit", Short: "Admit a receipt-bound candidate", RunE: func(cmd *cobra.Command, _ []string) error {
		raw, err := os.ReadFile(receiptPath)
		if err != nil {
			return fmt.Errorf("queue: read receipt: %w", err)
		}
		var r receipt.Receipt
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("queue: parse receipt: %w", err)
		}
		c, err := (queue.Store{ProjectRoot: project}).Submit(queue.Submit{Branch: branch, SHA: sha, Receipt: r, Backend: backend, Paths: paths})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(c)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&branch, "branch", "", "candidate branch")
	cmd.Flags().StringVar(&sha, "sha", "", "full candidate git SHA")
	cmd.Flags().StringVar(&receiptPath, "receipt", "", "capsule CI receipt JSON")
	cmd.Flags().StringVar(&backend, "backend", "local", "dispatch backend")
	cmd.Flags().StringSliceVar(&paths, "path", nil, "changed path (repeatable)")
	_ = cmd.MarkFlagRequired("branch")
	_ = cmd.MarkFlagRequired("sha")
	_ = cmd.MarkFlagRequired("receipt")
	return cmd
}
func queueListCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{Use: "list", Short: "List durable queue candidates", RunE: func(cmd *cobra.Command, _ []string) error {
		state, err := (queue.Store{ProjectRoot: project}).List()
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(state)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	return cmd
}
