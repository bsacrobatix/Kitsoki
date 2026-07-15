package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"kitsoki/internal/capsule/playground"
	"os/exec"
	"strings"
	"time"
)

func capsulePlaygroundCmd() *cobra.Command {
	c := &cobra.Command{Use: "playground", Short: "Run a leased dev/test playground from a managed Capsule"}
	c.AddCommand(capsulePlaygroundStartCmd(), capsulePlaygroundStatusCmd(), capsulePlaygroundTouchCmd(), capsulePlaygroundStopCmd(), capsulePlaygroundSuperviseCmd())
	return c
}
func playgroundWrite(c *cobra.Command, v any) error { return capsuleWorkspaceWrite(c, v, true) }
func capsulePlaygroundStartCmd() *cobra.Command {
	var project, workspace, id, ref, instructions, scenarios string
	var idle time.Duration
	c := &cobra.Command{Use: "start --workspace <path> --id <id> -- <command> [args...]", Args: cobra.ArbitraryArgs, RunE: func(c *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("capsule playground: command is required after --")
		}
		sha, e := playground.SourceSHAForWorkspace(workspace)
		if e != nil {
			return e
		}
		if ref != "" {
			out, e := exec.Command("git", "-C", workspace, "rev-parse", ref).Output()
			if e != nil || strings.TrimSpace(string(out)) != sha {
				return fmt.Errorf("capsule playground: ref %q does not resolve to workspace source_sha %s", ref, sha)
			}
		}
		r, e := playground.Start(c.Context(), playground.StartRequest{Project: project, ID: id, Workspace: workspace, SourceSHA: sha, Ref: ref, Command: args, IdleTimeout: idle, Testing: playground.Testing{Instructions: splitCSV(instructions), ScenarioIDs: splitCSV(scenarios)}})
		if e != nil {
			return e
		}
		return playgroundWrite(c, r)
	}}
	c.Flags().StringVar(&project, "project", ".", "project root")
	c.Flags().StringVar(&workspace, "workspace", "", "managed Capsule workspace path")
	c.Flags().StringVar(&id, "id", "", "playground id")
	c.Flags().StringVar(&ref, "ref", "", "branch or ref which must resolve to workspace HEAD")
	c.Flags().DurationVar(&idle, "idle-timeout", playground.DefaultIdleTimeout, "idle lease (10m-15m)")
	c.Flags().StringVar(&instructions, "instructions", "", "comma-separated testing instructions")
	c.Flags().StringVar(&scenarios, "scenario-ids", "", "comma-separated scenario ids")
	_ = c.MarkFlagRequired("workspace")
	_ = c.MarkFlagRequired("id")
	return c
}
func capsulePlaygroundStatusCmd() *cobra.Command {
	return playgroundRecordCmd("status", playground.Status)
}
func capsulePlaygroundTouchCmd() *cobra.Command {
	return playgroundRecordCmd("touch", playground.Touch)
}
func capsulePlaygroundStopCmd() *cobra.Command { return playgroundRecordCmd("stop", playground.Stop) }
func playgroundRecordCmd(name string, fn func(string, string) (playground.Record, error)) *cobra.Command {
	var project, id string
	c := &cobra.Command{Use: name, RunE: func(c *cobra.Command, _ []string) error {
		r, e := fn(project, id)
		if e != nil {
			return e
		}
		return playgroundWrite(c, r)
	}}
	c.Flags().StringVar(&project, "project", ".", "project root")
	c.Flags().StringVar(&id, "id", "", "playground id")
	_ = c.MarkFlagRequired("id")
	return c
}
func capsulePlaygroundSuperviseCmd() *cobra.Command {
	var project, id string
	c := &cobra.Command{Use: "supervise", Hidden: true, RunE: func(c *cobra.Command, _ []string) error {
		for {
			r, e := playground.Reap(project, id, time.Now().UTC())
			if e != nil {
				return e
			}
			if r.State != "ready" && r.State != "starting" {
				return nil
			}
			select {
			case <-c.Context().Done():
				return c.Context().Err()
			case <-time.After(time.Minute):
			}
		}
	}}
	c.Flags().StringVar(&project, "project", ".", "project root")
	c.Flags().StringVar(&id, "id", "", "playground id")
	_ = c.MarkFlagRequired("id")
	return c
}
func splitCSV(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
