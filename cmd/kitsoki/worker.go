package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/daemonfederation"
	"kitsoki/internal/webconfig"
	"kitsoki/internal/workerregistry"
)

// workerCmd is the top-level `kitsoki worker` registry CLI (standing-autonomy
// proposal §9 "Federation", ask 4): list/add/remove/enable/disable/drain
// against the canonical `workers:` block in .kitsoki.local.yaml. It is a
// distinct command from `kitsoki capsule worker` (cmd/kitsoki/capsule_worker.go),
// which runs a sealed Capsule execution envelope inside a prepared executor —
// this command instead manages the machine-local registry of remote worker
// identities that executors and daemon federation dispatch against.
func workerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "worker", Short: "Manage the machine-local worker registry (federation ask 4)"}
	cmd.AddCommand(
		workerListCmd(),
		workerAddCmd(),
		workerRemoveCmd(),
		workerEnableCmd(),
		workerDisableCmd(),
		workerDrainCmd(),
	)
	return cmd
}

// workerConfigFlags installs the shared --config flag; every subcommand
// reads it back via cmd.Flags().Lookup("config") so the flag default stays
// declared in exactly one place.
func workerConfigFlags(cmd *cobra.Command) {
	cmd.Flags().String("config", webconfig.DefaultConfigFile, "base config path (the sibling .local.yaml carries worker entries)")
}

func workerListCmd() *cobra.Command {
	var jsonOut bool
	var pollTimeout time.Duration
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List registered workers with cached/live health where cheaply available",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			config := cmd.Flags().Lookup("config").Value.String()
			reg, err := workerregistry.Load(config, webconfig.LocalConfigPath(config))
			if err != nil {
				return err
			}
			projections := projectWithHealth(cmd.Context(), reg.Entries, pollTimeout)
			if jsonOut {
				return writeWorkerJSON(cmd.OutOrStdout(), projections)
			}
			printWorkerTable(cmd.OutOrStdout(), projections, reg.Source)
			return nil
		},
	}
	workerConfigFlags(cmd)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the stable portal projection contract as JSON")
	cmd.Flags().DurationVar(&pollTimeout, "poll-timeout", 2*time.Second, "per-worker health poll timeout for entries with an endpoint")
	return cmd
}

// projectWithHealth polls entries that declare an endpoint directly (no
// running kitsoki daemon required — daemonfederation.Pool itself is an HTTP
// client) and marks entries without an endpoint (e.g. https-only,
// credential-env CI-style remotes) as health "unknown", since there is
// nothing to poll. This is a deliberate judgment call documented in
// docs/guide/development/worker-registry.md: `kitsoki worker list` performs
// its own short-lived poll rather than requiring a separately running daemon
// process to ask for cached status.
func projectWithHealth(ctx context.Context, entries []workerregistry.Entry, timeout time.Duration) []workerregistry.Projection {
	var pollable []daemonfederation.Worker
	for _, e := range entries {
		if e.Endpoint != "" {
			pollable = append(pollable, daemonfederation.Worker{ID: e.ID, Label: e.Label, Placement: e.Placement, Endpoint: e.Endpoint})
		}
	}
	statusByID := map[string]daemonfederation.WorkerStatus{}
	jobsByID := map[string]int{}
	if len(pollable) > 0 {
		pool := &daemonfederation.Pool{Workers: pollable, Timeout: timeout}
		pollCtx, cancel := context.WithTimeout(ctx, timeout+time.Second)
		defer cancel()
		snapshot := pool.Get(pollCtx)
		for _, status := range snapshot.Workers {
			statusByID[status.ID] = status
			jobsByID[status.ID] = status.JobCount
		}
	}
	out := make([]workerregistry.Projection, 0, len(entries))
	for _, e := range entries {
		health := "unknown"
		jobs := 0
		if status, ok := statusByID[e.ID]; ok {
			health = status.Health
			jobs = jobsByID[e.ID]
		}
		out = append(out, e.ToProjection(health, jobs))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func writeWorkerJSON(w io.Writer, projections []workerregistry.Projection) error {
	if projections == nil {
		projections = []workerregistry.Projection{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(projections)
}

func printWorkerTable(w io.Writer, projections []workerregistry.Projection, source string) {
	fmt.Fprintf(w, "source: %s\n", source)
	if len(projections) == 0 {
		fmt.Fprintln(w, "(no workers registered)")
		return
	}
	fmt.Fprintf(w, "%-16s %-20s %-12s %-10s %-8s %-9s %s\n", "ID", "LABEL", "PLACEMENT", "HEALTH", "ENABLED", "JOBS", "CAPABILITIES")
	for _, p := range projections {
		caps := "-"
		if len(p.Capabilities.Placements) > 0 || p.Capabilities.Isolation != "" || len(p.Capabilities.Networks) > 0 {
			caps = fmt.Sprintf("placements=%v isolation=%s networks=%v", p.Capabilities.Placements, p.Capabilities.Isolation, p.Capabilities.Networks)
		}
		fmt.Fprintf(w, "%-16s %-20s %-12s %-10s %-8t %-9d %s\n", p.ID, p.Label, p.Placement, p.Health, p.Enabled, p.Jobs, caps)
	}
}

func workerAddCmd() *cobra.Command {
	var id, label, placement, endpoint, credentialEnv, isolation string
	var placements, networks []string
	var enabled bool
	cmd := &cobra.Command{
		Use:          "add",
		Short:        "Add a worker to the local registry (.kitsoki.local.yaml)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			config := cmd.Flags().Lookup("config").Value.String()
			entry := workerregistry.Entry{
				ID:            strings.TrimSpace(id),
				Label:         strings.TrimSpace(label),
				Placement:     strings.TrimSpace(placement),
				Endpoint:      strings.TrimSpace(endpoint),
				CredentialEnv: strings.TrimSpace(credentialEnv),
				Enabled:       enabled,
				Capabilities: workerregistry.Capabilities{
					Placements: placements,
					Isolation:  isolation,
					Networks:   networks,
				},
			}
			entries, err := workerregistry.Add(webconfig.LocalConfigPath(config), entry)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added %q (%d worker(s) registered)\n", entry.ID, len(entries))
			return nil
		},
	}
	workerConfigFlags(cmd)
	cmd.Flags().StringVar(&id, "id", "", "unique lowercase slug id")
	cmd.Flags().StringVar(&label, "label", "", "human-readable label")
	cmd.Flags().StringVar(&placement, "placement", "", "thin, workstation, or local-model")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "http(s) worker endpoint (optional; omit for credential-only remotes)")
	cmd.Flags().StringVar(&credentialEnv, "credential-env", "", "env var name holding the worker's credential")
	cmd.Flags().StringVar(&isolation, "isolation", "", "advertised executor.Capabilities.Isolation value")
	cmd.Flags().StringSliceVar(&placements, "capability-placement", nil, "advertised executor.Capabilities.Placements entries (repeatable)")
	cmd.Flags().StringSliceVar(&networks, "capability-network", nil, "advertised executor.Capabilities.Networks entries (repeatable)")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "whether the worker is immediately enabled")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("label")
	_ = cmd.MarkFlagRequired("placement")
	return cmd
}

func workerRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "remove <id>",
		Short:        "Remove a worker from the local registry",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			config := cmd.Flags().Lookup("config").Value.String()
			entries, err := workerregistry.Remove(webconfig.LocalConfigPath(config), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %q (%d worker(s) remain)\n", args[0], len(entries))
			return nil
		},
	}
	workerConfigFlags(cmd)
	return cmd
}

func workerEnableCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "enable <id>",
		Short:        "Mark a registered worker enabled",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			config := cmd.Flags().Lookup("config").Value.String()
			if _, err := workerregistry.SetEnabled(webconfig.LocalConfigPath(config), args[0], true); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "enabled %q\n", args[0])
			return nil
		},
	}
	workerConfigFlags(cmd)
	return cmd
}

func workerDisableCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "disable <id>",
		Short:        "Mark a registered worker disabled",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			config := cmd.Flags().Lookup("config").Value.String()
			if _, err := workerregistry.SetEnabled(webconfig.LocalConfigPath(config), args[0], false); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "disabled %q\n", args[0])
			return nil
		},
	}
	workerConfigFlags(cmd)
	return cmd
}

// workerDrainCmd is advisory/soft-drain only: it disables the worker so no
// new dispatch targets it, and best-effort reports jobs the live poll still
// attributes to it. It never attempts to cancel or kill running jobs.
func workerDrainCmd() *cobra.Command {
	var pollTimeout time.Duration
	cmd := &cobra.Command{
		Use:          "drain <id>",
		Short:        "Disable a worker and report any jobs still attributed to it (advisory, no forceful kill)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			config := cmd.Flags().Lookup("config").Value.String()
			entries, err := workerregistry.SetEnabled(webconfig.LocalConfigPath(config), args[0], false)
			if err != nil {
				return err
			}
			var target workerregistry.Entry
			for _, e := range entries {
				if e.ID == args[0] {
					target = e
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "draining %q: enabled=false, no new dispatch will target it\n", args[0])
			if target.Endpoint == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "no endpoint configured for this worker: live jobs could not be checked")
				return nil
			}
			pool := &daemonfederation.Pool{
				Workers: []daemonfederation.Worker{{ID: target.ID, Label: target.Label, Placement: target.Placement, Endpoint: target.Endpoint}},
				Timeout: pollTimeout,
			}
			pollCtx, cancel := context.WithTimeout(cmd.Context(), pollTimeout+time.Second)
			defer cancel()
			snapshot := pool.Get(pollCtx)
			var status daemonfederation.WorkerStatus
			for _, s := range snapshot.Workers {
				if s.ID == target.ID {
					status = s
				}
			}
			if status.Health == "" || status.Health == daemonfederation.HealthOffline {
				fmt.Fprintln(cmd.OutOrStdout(), "worker unreachable: live jobs could not be checked")
				return nil
			}
			if status.JobCount == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no running jobs attributed to this worker")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d job(s) still attributed to this worker (not cancelled — drain is advisory):\n", status.JobCount)
			for _, job := range snapshot.Jobs {
				if job.WorkerID == target.ID {
					fmt.Fprintf(cmd.OutOrStdout(), "  - %s (%s) status=%s\n", job.JobID, job.Story, job.Status)
				}
			}
			return nil
		},
	}
	workerConfigFlags(cmd)
	cmd.Flags().DurationVar(&pollTimeout, "poll-timeout", 2*time.Second, "poll timeout for checking live jobs")
	return cmd
}
