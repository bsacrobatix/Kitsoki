package main

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/reviewwave"
	"os"
	"strings"
)

func waveCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "wave", Short: "Manage durable, target-bound review waves"}
	cmd.AddCommand(waveCreateCmd(), waveShowCmd(), waveListCmd(), waveAddCmd(), waveRemoveCmd(), waveSplitCmd(), waveFreezeCmd(), waveUnfreezeCmd(), waveAbandonCmd(), wavePrepareCmd(), waveVerifyCmd(), waveApproveCmd(), waveLandCmd())
	return cmd
}
func waveStore(project string) *reviewwave.Store { return &reviewwave.Store{ProjectRoot: project} }
func emitWave(cmd *cobra.Command, w reviewwave.Wave, error error) error {
	if error != nil {
		return error
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(w)
}
func waveCreateCmd() *cobra.Command {
	var project, title, theme, train, risk, compatibility string
	var drives, testPlan []string
	cmd := &cobra.Command{Use: "create", Short: "Create a review wave", RunE: func(c *cobra.Command, _ []string) error {
		w, e := waveStore(project).Create(title, theme, train)
		if e != nil {
			return e
		}
		w, e = waveStore(project).Configure(w.ID, drives, testPlan, risk, compatibility)
		return emitWave(c, w, e)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&title, "title", "", "review title")
	cmd.Flags().StringVar(&theme, "theme", "", "wave theme")
	cmd.Flags().StringVar(&train, "train", "", "release train")
	cmd.Flags().StringSliceVar(&drives, "drive", nil, "graph anchor id")
	cmd.Flags().StringSliceVar(&testPlan, "test-plan", nil, "named deterministic gate or scenario")
	cmd.Flags().StringVar(&risk, "risk", "", "review risk summary")
	cmd.Flags().StringVar(&compatibility, "compatibility-impact", "", "none, patch, minor, or major")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("theme")
	_ = cmd.MarkFlagRequired("train")
	return cmd
}
func waveShowCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{Use: "show <wave-id>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		w, e := waveStore(project).Get(a[0])
		return emitWave(c, w, e)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	return cmd
}
func waveListCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{Use: "list", RunE: func(c *cobra.Command, _ []string) error {
		w, e := waveStore(project).List()
		if e != nil {
			return e
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(w)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	return cmd
}
func waveAddCmd() *cobra.Command {
	var project, candidate string
	cmd := &cobra.Command{Use: "add <wave-id>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		q, e := (queue.Store{ProjectRoot: project}).Get(candidate)
		if e != nil {
			return e
		}
		w, e := waveStore(project).Add(a[0], q)
		return emitWave(c, w, e)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&candidate, "candidate", "", "admitted queue candidate id")
	_ = cmd.MarkFlagRequired("candidate")
	return cmd
}
func waveRemoveCmd() *cobra.Command {
	var project, candidate string
	cmd := &cobra.Command{Use: "remove <wave-id>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		w, e := waveStore(project).Remove(a[0], candidate)
		return emitWave(c, w, e)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&candidate, "candidate", "", "member candidate id")
	_ = cmd.MarkFlagRequired("candidate")
	return cmd
}
func waveSplitCmd() *cobra.Command {
	var project, title, theme, train string
	var candidate []string
	cmd := &cobra.Command{Use: "split <wave-id>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		w, e := waveStore(project).Split(a[0], title, theme, train, candidate)
		return emitWave(c, w, e)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&title, "title", "", "new wave title")
	cmd.Flags().StringVar(&theme, "theme", "", "new wave theme")
	cmd.Flags().StringVar(&train, "train", "", "release train")
	cmd.Flags().StringSliceVar(&candidate, "candidate", nil, "member candidate id")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("theme")
	_ = cmd.MarkFlagRequired("train")
	_ = cmd.MarkFlagRequired("candidate")
	return cmd
}
func waveFreezeCmd() *cobra.Command {
	return waveSimple("freeze", func(s *reviewwave.Store, id string) (reviewwave.Wave, error) { return s.Freeze(id) })
}
func waveUnfreezeCmd() *cobra.Command {
	return waveSimple("unfreeze", func(s *reviewwave.Store, id string) (reviewwave.Wave, error) { return s.Unfreeze(id) })
}
func wavePrepareCmd() *cobra.Command {
	return waveSimple("prepare", func(s *reviewwave.Store, id string) (reviewwave.Wave, error) { return s.Prepare(id) })
}
func waveSimple(name string, run func(*reviewwave.Store, string) (reviewwave.Wave, error)) *cobra.Command {
	var project string
	cmd := &cobra.Command{Use: name + " <wave-id>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		w, e := run(waveStore(project), a[0])
		return emitWave(c, w, e)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	return cmd
}
func waveAbandonCmd() *cobra.Command {
	var project, reason string
	cmd := &cobra.Command{Use: "abandon <wave-id>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		w, e := waveStore(project).Abandon(a[0], reason)
		return emitWave(c, w, e)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&reason, "reason", "", "operator reason")
	return cmd
}
func waveVerifyCmd() *cobra.Command {
	var project string
	var receipt []string
	cmd := &cobra.Command{Use: "verify <wave-id>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		w, e := waveStore(project).Verify(a[0], receipt)
		return emitWave(c, w, e)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringSliceVar(&receipt, "receipt", nil, "verification receipt id")
	return cmd
}
func waveApproveCmd() *cobra.Command {
	var project, actor, reason, manifest, prepared string
	cmd := &cobra.Command{Use: "approve <wave-id>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if strings.TrimSpace(actor) == "" {
			actor = os.Getenv("USER")
		}
		w, e := waveStore(project).Approve(a[0], actor, reason, manifest, prepared)
		return emitWave(c, w, e)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&actor, "actor", "", "steward")
	cmd.Flags().StringVar(&reason, "reason", "", "approval reason")
	cmd.Flags().StringVar(&manifest, "manifest", "", "frozen manifest digest")
	cmd.Flags().StringVar(&prepared, "prepared", "", "prepared tree digest")
	_ = cmd.MarkFlagRequired("manifest")
	_ = cmd.MarkFlagRequired("prepared")
	return cmd
}
func waveLandCmd() *cobra.Command {
	var project string
	var result []string
	cmd := &cobra.Command{Use: "land <wave-id>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		out := make([]reviewwave.Landing, 0, len(result))
		for _, r := range result {
			p := strings.SplitN(r, ":", 3)
			if len(p) < 2 {
				return fmt.Errorf("invalid --result, want candidate:status[:error]")
			}
			x := reviewwave.Landing{CandidateID: p[0], Status: p[1]}
			if len(p) == 3 {
				x.Error = p[2]
			}
			out = append(out, x)
		}
		w, e := waveStore(project).Land(a[0], out)
		return emitWave(c, w, e)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringSliceVar(&result, "result", nil, "candidate:status[:error] landing outcome")
	return cmd
}
