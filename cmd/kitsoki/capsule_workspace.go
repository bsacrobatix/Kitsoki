package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/hygiene"
)

var (
	capsuleWorkspaceActivityProbe = hygiene.ProbeWorkspaceActivity
	capsuleWorkspaceProcessProbe  = hygiene.ProbeWorkspaceProcessCommands
	capsuleWorkspaceCloseNow      = func() time.Time { return time.Now().UTC() }
)

// capsuleWorkspaceCmd is the operator/automation CLI counterpart to the
// handle-scoped MCP lifecycle. It retains current script compatibility while
// letting any onboarded project use the native manager directly.
func capsuleWorkspaceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "workspace", Short: "Create and manage native Capsule workspaces"}
	cmd.AddCommand(capsuleWorkspaceCreateCmd(), capsuleWorkspaceCreateScriptCmd(), capsuleWorkspaceListCmd(), capsuleWorkspaceStatusCmd(), capsuleWorkspaceReconcileCmd(), capsuleWorkspaceExecCmd(), capsuleWorkspaceCommitCmd(), capsuleWorkspaceIntegrateCmd(), capsuleWorkspaceCloseCmd(), capsuleWorkspacePurgeCmd())
	return cmd
}

func capsuleWorkspacePurgeCmd() *cobra.Command {
	var project, receiptPath string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:          "purge",
		Short:        "Purge one old closed quarantine bound to a durable retention receipt",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, err := os.Lstat(receiptPath)
			if err != nil {
				return fmt.Errorf("capsule workspace purge: inspect receipt: %w", err)
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64<<10 {
				return fmt.Errorf("capsule workspace purge: receipt must be a regular non-symlink file no larger than 64 KiB")
			}
			file, err := os.Open(receiptPath)
			if err != nil {
				return fmt.Errorf("capsule workspace purge: open receipt: %w", err)
			}
			defer file.Close()
			var receipt hygiene.RetentionReceipt
			decoder := json.NewDecoder(io.LimitReader(file, (64<<10)+1))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&receipt); err != nil {
				return fmt.Errorf("capsule workspace purge: parse receipt: %w", err)
			}
			result, err := hygiene.PurgeClosedWorkspace(cmd.Context(), hygiene.PurgeOptions{
				ProjectRoot: project,
				Receipt:     receipt,
			})
			if err != nil {
				return err
			}
			return capsuleWorkspaceWrite(cmd, result, jsonOut)
		},
	}
	cmd.Flags().StringVar(&project, "project", ".", "trusted project root")
	cmd.Flags().StringVar(&receiptPath, "receipt", "", "capsule-workspace-retention/v1 JSON receipt")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	_ = cmd.MarkFlagRequired("receipt")
	return cmd
}

func capsuleWorkspaceExecCmd() *cobra.Command {
	var project, id, owner, commandID string
	var generation uint64
	var timeout time.Duration
	var jsonOut bool
	cmd := &cobra.Command{Use: "exec", Short: "Run one definition-declared command in a managed Capsule workspace", RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		result, err := capsuleWorkspaceExecute(cmd.Context(), manager, control.Handle{ID: id, Generation: generation}, owner, commandID, timeout)
		if err != nil {
			return err
		}
		view := capsuleWorkspaceExecutionResult{
			OK:              result.ExitCode == 0,
			ID:              id,
			Generation:      result.Handle.Generation,
			Owner:           owner,
			CommandID:       commandID,
			ExitCode:        result.ExitCode,
			Output:          result.Output,
			OutputTruncated: result.OutputTruncated,
			TimedOut:        result.TimedOut,
		}
		if err := capsuleWorkspaceWrite(cmd, view, jsonOut); err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("capsule workspace exec: command %q exited %d", commandID, result.ExitCode)
		}
		return nil
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().Uint64Var(&generation, "generation", 0, "exact workspace handle generation")
	cmd.Flags().StringVar(&owner, "owner", "", "lease owner")
	cmd.Flags().StringVar(&commandID, "command", "", "definition-declared command id")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "optional command timeout override")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("generation")
	_ = cmd.MarkFlagRequired("owner")
	_ = cmd.MarkFlagRequired("command")
	return cmd
}
func capsuleWorkspaceManager(project string) (*control.Manager, error) {
	m, _, err := newCapsuleManager(project, "local", []string{"*"})
	return m, err
}
func capsuleWorkspaceCreateCmd() *cobra.Command {
	var project, id, definition, owner string
	var jsonOut bool
	cmd := &cobra.Command{Use: "create", Short: "Create or reacquire a managed Capsule workspace", RunE: func(cmd *cobra.Command, args []string) error {
		m, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		def, err := m.Definition(cmd.Context(), definition)
		if err != nil {
			return err
		}
		if def.Source.Kind == control.SourceDevWorkspaceScript {
			return fmt.Errorf("capsule workspace create: definition %q uses scripts/dev-workspace.sh; use capsule workspace create-script", definition)
		}
		h, err := m.Create(cmd.Context(), control.CreateRequest{ID: id, DefinitionID: definition, Owner: owner})
		if err != nil {
			return err
		}
		view, err := capsuleWorkspaceView(cmd.Context(), m, h)
		if err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, view, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().StringVar(&definition, "definition", "", "capsule definition id")
	cmd.Flags().StringVar(&owner, "owner", "cli", "lease owner")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("definition")
	return cmd
}

// capsuleWorkspaceCreateScriptCmd is the only supported bridge from the
// protected clone lifecycle into Capsule CI. Manager creation persists the
// immutable instance before it asks scripts/dev-workspace.sh to materialize a
// checkout, so callers never receive a usable path without a CI identity.
func capsuleWorkspaceCreateScriptCmd() *cobra.Command {
	var project, id, owner string
	var jsonOut bool
	cmd := &cobra.Command{Use: "create-script", Short: "Create a registered scripts/dev-workspace.sh Capsule", RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		h, err := manager.CreateDevWorkspaceScript(cmd.Context(), control.CreateRequest{ID: id, DefinitionID: "development", Owner: owner})
		if err != nil {
			return err
		}
		view, err := capsuleWorkspaceView(cmd.Context(), manager, h)
		if err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, view, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().StringVar(&owner, "owner", "cli", "lease owner")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
func capsuleWorkspaceListCmd() *cobra.Command {
	var project string
	var jsonOut bool
	cmd := &cobra.Command{Use: "list", Short: "List managed Capsule workspaces", RunE: func(cmd *cobra.Command, args []string) error {
		m, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		all, err := m.List(cmd.Context())
		if err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, map[string]any{"workspaces": all}, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}
func capsuleWorkspaceStatusCmd() *cobra.Command {
	var project, id string
	var jsonOut bool
	cmd := &cobra.Command{Use: "status", Short: "Show a managed Capsule workspace", RunE: func(cmd *cobra.Command, args []string) error {
		m, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		in, err := m.Instances.Get(cmd.Context(), id)
		if err != nil {
			return err
		}
		view, err := capsuleWorkspaceView(cmd.Context(), m, control.Handle{ID: in.ID, Generation: in.Generation})
		if err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, view, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
func capsuleWorkspaceCommitCmd() *cobra.Command {
	var project, id, message string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "commit",
		Short: "Commit local workspace changes and reconcile the registered head with git",
		Long: "Commit outstanding working-tree changes in a managed Capsule workspace.\n\n" +
			"Committing with plain `git commit` inside the workspace is fully supported.\n" +
			"When there is nothing left to stage but git HEAD has advanced past the\n" +
			"registered head with a clean tree, this command adopts that head and reports\n" +
			"what it adopted. It refuses only when there is genuinely nothing to do, or\n" +
			"when adopting would drop commits Capsule had already registered.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := capsuleWorkspaceManager(project)
			if err != nil {
				return err
			}
			in, err := m.Instances.Get(cmd.Context(), id)
			if err != nil {
				return err
			}
			result, err := m.CommitVCSResult(cmd.Context(), control.Handle{ID: in.ID, Generation: in.Generation}, message)
			if err != nil {
				return err
			}
			view, err := capsuleWorkspaceView(cmd.Context(), m, result.Handle)
			if err != nil {
				return err
			}
			view.Committed = result.Committed
			view.Adopted = result.Adopted
			view.PreviousHead = result.Previous
			view.CommitsAdopted = result.Commits
			view.Summary = result.Summary
			if !jsonOut {
				fmt.Fprintln(cmd.OutOrStdout(), result.Summary)
				return nil
			}
			return capsuleWorkspaceWrite(cmd, view, jsonOut)
		}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().StringVar(&message, "message", "", "commit message")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("message")
	return cmd
}

// capsuleWorkspaceReconcileCmd is the deliberate operator path for absorbing
// raw `git commit` work into the Capsule registered head. It never creates a
// commit and never touches the working tree, so it is safe to run at any time
// to answer "why won't this promote?".
func capsuleWorkspaceReconcileCmd() *cobra.Command {
	var project, id string
	var jsonOut, dryRun bool
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Adopt a workspace head that advanced via raw git into the Capsule registered head",
		Long: "Reconcile the Capsule registered head with the workspace's live git HEAD.\n\n" +
			"Adoption requires a clean working tree and a git HEAD that is a strict\n" +
			"descendant of the registered head, so no registered commit can be dropped.\n" +
			"A rewrite (reset, rebase, amend) is refused loudly and named. Reconciling\n" +
			"records provenance only: it never runs a gate and never marks work validated,\n" +
			"so promotion still requires its own Capsule CI receipt.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := capsuleWorkspaceManager(project)
			if err != nil {
				return err
			}
			in, err := m.Instances.Get(cmd.Context(), id)
			if err != nil {
				return err
			}
			handle := control.Handle{ID: in.ID, Generation: in.Generation}
			if dryRun {
				drift, err := m.InspectHead(cmd.Context(), handle)
				if err != nil {
					return err
				}
				planned := capsuleWorkspaceReconcileResult{
					Schema: "capsule-workspace-reconcile/v1", ID: in.ID, Generation: in.Generation,
					Relation: string(drift.Relation), RegisteredHead: drift.RegisteredHead, GitHead: drift.GitHead,
					Branch: drift.Branch, Dirty: drift.Dirty, Ahead: drift.Ahead, Behind: drift.Behind,
					Adoptable: drift.Relation.Adoptable() && !drift.Dirty,
					Summary:   drift.Explain(), Next: drift.Next(),
				}
				if !jsonOut {
					fmt.Fprintln(cmd.OutOrStdout(), planned.Summary)
					fmt.Fprintln(cmd.OutOrStdout(), "next: "+planned.Next)
					return nil
				}
				return capsuleWorkspaceWrite(cmd, planned, jsonOut)
			}
			adoption, err := m.AdoptHead(cmd.Context(), handle)
			if err != nil {
				return err
			}
			drift := adoption.Drift
			result := capsuleWorkspaceReconcileResult{
				Schema: "capsule-workspace-reconcile/v1", ID: in.ID, Generation: adoption.Handle.Generation,
				Relation: string(drift.Relation), RegisteredHead: adoption.Previous, GitHead: adoption.Head,
				Branch: drift.Branch, Dirty: drift.Dirty, Ahead: drift.Ahead, Behind: drift.Behind,
				Adopted: adoption.Adopted, Adoptable: true, CommitsAdopted: adoption.Commits,
				Summary: adoption.Summary, Next: drift.Next(),
			}
			if !adoption.Adopted {
				result.Next = drift.Next()
			}
			if !jsonOut {
				fmt.Fprintln(cmd.OutOrStdout(), result.Summary)
				fmt.Fprintln(cmd.OutOrStdout(), "next: "+result.Next)
				return nil
			}
			return capsuleWorkspaceWrite(cmd, result, jsonOut)
		}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "diagnose drift without changing the registered head")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

type capsuleWorkspaceReconcileResult struct {
	Schema         string `json:"schema"`
	ID             string `json:"id"`
	Generation     uint64 `json:"generation"`
	Relation       string `json:"relation"`
	RegisteredHead string `json:"registered_head,omitempty"`
	GitHead        string `json:"git_head,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Dirty          bool   `json:"dirty"`
	Ahead          int    `json:"ahead,omitempty"`
	Behind         int    `json:"behind,omitempty"`
	Adoptable      bool   `json:"adoptable"`
	Adopted        bool   `json:"adopted"`
	CommitsAdopted int    `json:"commits_adopted,omitempty"`
	Summary        string `json:"summary"`
	Next           string `json:"next"`
}

func capsuleWorkspaceIntegrateCmd() *cobra.Command {
	var project, id, gate, owner string
	var teardown, jsonOut bool
	cmd := &cobra.Command{Use: "integrate", Short: "Integrate a provider-managed workspace through its declared protected lifecycle", RunE: func(cmd *cobra.Command, args []string) error {
		m, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		in, err := m.Instances.Get(cmd.Context(), id)
		if err != nil {
			return err
		}
		result, err := capsuleWorkspaceIntegrate(cmd.Context(), m, in, gate, teardown, owner)
		if err != nil {
			return err
		}
		if result.CleanupError != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: workspace %s integrated, but teardown needs attention: %s\n", id, result.CleanupError)
		}
		return capsuleWorkspaceWrite(cmd, result, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().StringVar(&gate, "gate", "", "focused validation command declared by the development adapter")
	cmd.Flags().StringVar(&owner, "owner", "", "lease owner required when tearing down; defaults to the workspace lease owner")
	cmd.Flags().BoolVar(&teardown, "teardown", false, "close the workspace after successful integration")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
func capsuleWorkspaceCloseCmd() *cobra.Command {
	var project, id, owner string
	var generation uint64
	var jsonOut bool
	cmd := &cobra.Command{Use: "close", Short: "Close a managed Capsule workspace", RunE: func(cmd *cobra.Command, args []string) error {
		m, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		in, err := m.Instances.Get(cmd.Context(), id)
		if err != nil {
			return err
		}
		result, err := capsuleWorkspaceClose(cmd.Context(), m, in, generation, owner)
		if err != nil {
			return err
		}
		if result.RetentionError != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: workspace %s closed, but purge authority needs attention: %s\n", id, result.RetentionError)
		}
		return capsuleWorkspaceWrite(cmd, result, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().Uint64Var(&generation, "generation", 0, "optional exact workspace generation; zero accepts the current generation")
	cmd.Flags().StringVar(&owner, "owner", "", "lease owner; defaults to the workspace lease owner")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
func capsuleWorkspaceWrite(cmd *cobra.Command, v any, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
	}
	fmt.Fprintln(cmd.OutOrStdout(), v)
	return nil
}

type capsuleWorkspaceViewResult struct {
	ID           string        `json:"id"`
	Generation   uint64        `json:"generation"`
	DefinitionID string        `json:"definition_id"`
	Provider     string        `json:"provider"`
	Path         string        `json:"path"`
	SourceRef    string        `json:"source_ref,omitempty"`
	Head         string        `json:"head,omitempty"`
	Branch       string        `json:"branch,omitempty"`
	State        control.State `json:"state"`
	Owner        string        `json:"owner"`

	// Head drift. `head` is what Capsule has registered; `git_head` is what the
	// workspace's git actually points at. They diverge whenever an agent uses
	// plain `git commit`, which is supported — `relation`/`next` say how to
	// reconcile without reading Go source.
	GitHead   string `json:"git_head,omitempty"`
	Relation  string `json:"relation,omitempty"`
	Dirty     bool   `json:"dirty,omitempty"`
	Ahead     int    `json:"ahead,omitempty"`
	Behind    int    `json:"behind,omitempty"`
	Drifted   bool   `json:"drifted,omitempty"`
	Diagnosis string `json:"diagnosis,omitempty"`
	Next      string `json:"next,omitempty"`

	// Populated by commit/reconcile to report what was actually absorbed.
	Committed      bool   `json:"committed,omitempty"`
	Adopted        bool   `json:"adopted,omitempty"`
	PreviousHead   string `json:"previous_head,omitempty"`
	CommitsAdopted int    `json:"commits_adopted,omitempty"`
	Summary        string `json:"summary,omitempty"`
}

type capsuleWorkspaceIntegrationResult struct {
	ID           string `json:"id"`
	Generation   uint64 `json:"generation"`
	Path         string `json:"path"`
	Owner        string `json:"owner"`
	Integrated   bool   `json:"integrated"`
	Closed       bool   `json:"closed"`
	CleanupError string `json:"cleanup_error,omitempty"`
}

type capsuleWorkspaceExecutionResult struct {
	OK              bool   `json:"ok"`
	ID              string `json:"id"`
	Generation      uint64 `json:"generation"`
	Owner           string `json:"owner"`
	CommandID       string `json:"command_id"`
	ExitCode        int    `json:"exit_code"`
	Output          string `json:"output,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	TimedOut        bool   `json:"timed_out,omitempty"`
}

type capsuleWorkspaceCloseResult struct {
	Schema           string        `json:"schema"`
	OK               bool          `json:"ok"`
	Project          string        `json:"project"`
	ID               string        `json:"id"`
	DefinitionID     string        `json:"definition_id"`
	Provider         string        `json:"provider"`
	Generation       uint64        `json:"generation"`
	Owner            string        `json:"owner"`
	State            control.State `json:"state"`
	Quarantine       string        `json:"quarantine,omitempty"`
	RetentionReceipt string        `json:"retention_receipt,omitempty"`
	RetentionError   string        `json:"retention_error,omitempty"`
}

func capsuleWorkspaceExecute(ctx context.Context, manager *control.Manager, handle control.Handle, owner, commandID string, timeout time.Duration) (control.CommandResult, error) {
	if strings.TrimSpace(commandID) == "" {
		return control.CommandResult{}, fmt.Errorf("capsule workspace exec: command id is required")
	}
	if _, err := manager.StatusOwned(ctx, handle, owner); err != nil {
		return control.CommandResult{}, err
	}
	return manager.RunCommand(ctx, handle, commandID, nil, timeout)
}

func capsuleWorkspaceClose(ctx context.Context, manager *control.Manager, in control.Instance, expectedGeneration uint64, owner string) (capsuleWorkspaceCloseResult, error) {
	if expectedGeneration != 0 && in.Generation != expectedGeneration {
		return capsuleWorkspaceCloseResult{}, fmt.Errorf("%w: instance %q generation %d, got %d", control.ErrStale, in.ID, in.Generation, expectedGeneration)
	}
	effectiveOwner := strings.TrimSpace(owner)
	if effectiveOwner == "" {
		effectiveOwner = in.Lease.Owner
	}
	handle := control.Handle{ID: in.ID, Generation: in.Generation}
	var firstProbe hygiene.RetentionProbe
	if in.Provider == string(control.SourceDevWorkspaceScript) {
		path, err := manager.WorkspacePath(ctx, handle)
		if err != nil {
			return capsuleWorkspaceCloseResult{}, err
		}
		activity, err := capsuleWorkspaceProcessProbe(ctx, []string{path})
		if err != nil || !activity.Known {
			if err == nil {
				err = fmt.Errorf("%s", activity.Reason)
			}
			return capsuleWorkspaceCloseResult{}, fmt.Errorf("workspace close liveness probe is inconclusive: %w", err)
		}
		if pids := activity.PIDsByPath[path]; len(pids) > 0 {
			return capsuleWorkspaceCloseResult{}, fmt.Errorf("workspace close refused active process(es): %v", pids)
		}
		firstProbe = hygiene.RetentionProbe{Kind: "process-command-snapshot", CapturedAt: capsuleWorkspaceCloseNow(), Safe: true}
	}
	quarantine, err := manager.CloseWithResult(ctx, handle, effectiveOwner)
	if err != nil {
		return capsuleWorkspaceCloseResult{}, err
	}
	closed, err := manager.Instances.Get(ctx, in.ID)
	if err != nil {
		return capsuleWorkspaceCloseResult{}, fmt.Errorf("workspace closed, but final lifecycle state could not be read: %w", err)
	}
	if closed.State != control.StateClosed {
		return capsuleWorkspaceCloseResult{}, fmt.Errorf("workspace close returned state %q", closed.State)
	}
	result := capsuleWorkspaceCloseResult{
		Schema:       "capsule-workspace-close/v1",
		OK:           true,
		Project:      manager.Grant.ProjectRoot,
		ID:           closed.ID,
		DefinitionID: closed.DefinitionID,
		Provider:     closed.Provider,
		Generation:   closed.Generation,
		Owner:        effectiveOwner,
		State:        closed.State,
	}
	if quarantine.Path == "" {
		return result, nil
	}
	relative, err := projectRelativeWorkspacePath(manager.Grant.ProjectRoot, quarantine.Path)
	if err != nil {
		result.RetentionError = "closed quarantine path is outside project authority: " + err.Error()
		return result, nil
	}
	result.Quarantine = relative
	activity, probeErr := capsuleWorkspaceActivityProbe(ctx, []string{quarantine.Path})
	if probeErr != nil || !activity.Known {
		if probeErr == nil {
			probeErr = fmt.Errorf("%s", activity.Reason)
		}
		result.RetentionError = "closed quarantine liveness probe is inconclusive: " + probeErr.Error()
		return result, nil
	}
	if pids := activity.PIDsByPath[quarantine.Path]; len(pids) > 0 {
		result.RetentionError = fmt.Sprintf("closed quarantine remains active in process(es): %v", pids)
		return result, nil
	}
	issuedAt := capsuleWorkspaceCloseNow()
	stateRoot, stateErr := filepath.EvalSymlinks(filepath.Join(manager.Grant.ProjectRoot, ".capsules"))
	if stateErr != nil {
		result.RetentionError = "project state root is unavailable: " + stateErr.Error()
		return result, nil
	}
	receipt := hygiene.RetentionReceipt{
		Schema:           hygiene.RetentionReceiptSchema,
		Project:          manager.Grant.ProjectRoot,
		ProjectStateRoot: stateRoot,
		WorkspaceID:      filepath.Base(quarantine.Path),
		WorkspacePath:    relative,
		Head:             quarantine.Head,
		RecoveryRef:      quarantine.RecoveryRef,
		IssuedAt:         issuedAt,
		EligibleAfter:    issuedAt.Add(hygiene.DefaultWorkspaceRetentionAge),
		ProcessSnapshot:  firstProbe,
		ActivityProbe: hygiene.RetentionProbe{
			Kind:       "closed-quarantine-activity",
			CapturedAt: issuedAt,
			Safe:       true,
		},
	}
	if failure := closed.Failure; failure != nil {
		receipt.SourceGeneration = failure.SourceGeneration
		receipt.Branch = failure.Branch
		receipt.Owner = failure.Owner
		receipt.Failure = &hygiene.RetentionFailure{Schema: failure.Schema, Kind: failure.Kind, Action: failure.Action, WorkspaceID: failure.WorkspaceID, SourceGeneration: failure.SourceGeneration, Path: failure.Path, Head: failure.Head, Branch: failure.Branch, Owner: failure.Owner, Evidence: append([]string(nil), failure.Evidence...), RecordedAt: failure.RecordedAt}
		if failure.Kind == "owner_reconcile" {
			receipt.EligibleAfter = issuedAt.Add(hygiene.OwnerReconcileRetentionAge)
		}
	}
	receiptPath, receiptErr := hygiene.WriteRetentionReceipt(manager.Grant.ProjectRoot, receipt)
	if receiptErr != nil {
		result.RetentionError = receiptErr.Error()
		return result, nil
	}
	result.RetentionReceipt = receiptPath
	return result, nil
}

func capsuleWorkspaceView(ctx context.Context, manager *control.Manager, handle control.Handle) (capsuleWorkspaceViewResult, error) {
	in, err := manager.Status(ctx, handle)
	if err != nil {
		return capsuleWorkspaceViewResult{}, err
	}
	path, err := manager.WorkspacePath(ctx, handle)
	if err != nil {
		return capsuleWorkspaceViewResult{}, err
	}
	relative, err := projectRelativeWorkspacePath(manager.Grant.ProjectRoot, path)
	if err != nil {
		return capsuleWorkspaceViewResult{}, err
	}
	view := capsuleWorkspaceViewResult{
		ID:           in.ID,
		Generation:   in.Generation,
		DefinitionID: in.DefinitionID,
		Provider:     in.Provider,
		Path:         relative,
		SourceRef:    in.SourceRef,
		Head:         in.Head,
		Branch:       in.Branch,
		State:        in.State,
		Owner:        in.Lease.Owner,
	}
	// Drift reporting is best effort: a workspace that is not a git checkout
	// (or is mid-materialization) must still return its lifecycle view.
	if drift, err := manager.InspectHead(ctx, control.Handle{ID: in.ID, Generation: in.Generation}); err == nil {
		view.GitHead = drift.GitHead
		view.Relation = string(drift.Relation)
		view.Dirty = drift.Dirty
		view.Ahead = drift.Ahead
		view.Behind = drift.Behind
		view.Drifted = drift.Relation != control.HeadInSync
		view.Diagnosis = drift.Explain()
		view.Next = drift.Next()
		if drift.Branch != "" {
			view.Branch = drift.Branch
		}
	}
	return view, nil
}

func projectRelativeWorkspacePath(projectRoot, workspacePath string) (string, error) {
	project, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", err
	}
	root := project
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if relative, ok := confinedRelativePath(root, path); ok {
		return filepath.ToSlash(relative), nil
	}

	// Hosted releases deliberately point <release>/.capsules at one stable
	// control-plane root outside the immutable release tree. WorkspacePath
	// returns the canonical target so persisted paths survive a release flip;
	// map that canonical path back through the managed project alias for the
	// user-facing, machine-path-redacted view. Only the exact .capsules alias is
	// admitted, and the descendant check remains fail-closed.
	capsuleAlias := filepath.Join(project, ".capsules")
	capsuleRoot, err := filepath.EvalSymlinks(capsuleAlias)
	if err == nil {
		if relative, ok := confinedRelativePath(capsuleRoot, path); ok {
			return filepath.ToSlash(filepath.Join(".capsules", relative)), nil
		}
	}
	return "", fmt.Errorf("capsule workspace: path is outside project scope")
}

func confinedRelativePath(root, path string) (string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func capsuleWorkspaceIntegrate(ctx context.Context, manager *control.Manager, in control.Instance, gate string, teardown bool, owner string) (capsuleWorkspaceIntegrationResult, error) {
	view, err := capsuleWorkspaceView(ctx, manager, control.Handle{ID: in.ID, Generation: in.Generation})
	if err != nil {
		return capsuleWorkspaceIntegrationResult{}, err
	}
	handle, err := manager.Integrate(ctx, control.Handle{ID: in.ID, Generation: in.Generation}, gate)
	if err != nil {
		return capsuleWorkspaceIntegrationResult{}, err
	}
	result := capsuleWorkspaceIntegrationResult{
		ID:         in.ID,
		Generation: handle.Generation,
		Path:       view.Path,
		Owner:      in.Lease.Owner,
		Integrated: true,
	}
	if !teardown {
		return result, nil
	}
	effectiveOwner := strings.TrimSpace(owner)
	if effectiveOwner == "" {
		effectiveOwner = in.Lease.Owner
	}
	result.Owner = effectiveOwner
	if err := manager.Close(ctx, handle, effectiveOwner); err != nil {
		result.CleanupError = err.Error()
		return result, nil
	}
	closed, err := manager.Instances.Get(ctx, in.ID)
	if err != nil {
		result.CleanupError = "workspace closed, but final lifecycle state could not be read: " + err.Error()
		return result, nil
	}
	result.Generation = closed.Generation
	result.Closed = true
	return result, nil
}
