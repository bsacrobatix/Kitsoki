package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule"
)

func capsuleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capsule",
		Short: "Open, verify, and close hermetic development capsules",
	}
	cmd.AddCommand(capsuleOpenCmd())
	cmd.AddCommand(capsuleVerifyCmd())
	cmd.AddCommand(capsuleCloseCmd())
	return cmd
}

func capsuleOpenCmd() *cobra.Command {
	var dest string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "open <name|path>",
		Short: "Materialize a capsule into a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := capsule.Open(cmd.Context(), args[0], capsule.OpenOptions{Dest: dest})
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(res.Manifest)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "capsule %s\n", res.Manifest.CapsuleName)
			fmt.Fprintf(cmd.OutOrStdout(), "workspace: %s\n", res.Manifest.Workspace)
			fmt.Fprintf(cmd.OutOrStdout(), "manifest: %s/%s\n", res.Manifest.Workspace, capsule.ManifestFile)
			fmt.Fprintf(cmd.OutOrStdout(), "tree_digest: sha256:%s\n", res.Manifest.TreeDigest)
			return nil
		},
	}
	cmd.Flags().StringVar(&dest, "dest", "", "destination directory (default: temp directory)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print manifest JSON")
	return cmd
}

func capsuleVerifyCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "verify <name|workspace>",
		Short: "Verify a capsule spec or materialized workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := capsule.Verify(cmd.Context(), args[0], nil)
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if encErr := enc.Encode(res); encErr != nil {
					return encErr
				}
			} else {
				status := "ok"
				if !res.OK {
					status = "failed"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "capsule %s verification: %s\n", res.CapsuleName, status)
				fmt.Fprintf(cmd.OutOrStdout(), "workspace: %s\n", res.Workspace)
				fmt.Fprintf(cmd.OutOrStdout(), "tree_digest: sha256:%s\n", res.ActualTreeDigest)
				for _, probe := range res.Probes {
					pstatus := "ok"
					if !probe.OK {
						pstatus = "failed"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "probe %s: %s (exit %d)\n", probe.Name, pstatus, probe.ExitCode)
				}
			}
			if !res.OK {
				return fmt.Errorf("capsule verification failed: %v", res.Errors)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print verification JSON")
	return cmd
}

func capsuleCloseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close <workspace>",
		Short: "Remove a materialized capsule workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := capsule.Close(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "closed %s\n", args[0])
			return nil
		},
	}
	return cmd
}
