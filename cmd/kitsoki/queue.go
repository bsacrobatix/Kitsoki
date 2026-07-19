package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/receipt"
)

func queueCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "queue", Short: "Submit verified candidates to the Capsule merge queue"}
	cmd.AddCommand(queueSubmitCmd(), queueStatusCmd(), queueProcessCmd(), queueWorkerCmd(), queueMigrateCmd())
	cmd.AddCommand(
		queueOpCmd("kick", "Clear a retry_wait candidate's backoff timer for an immediate retry", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Kick(op) }),
		queueOpCmd("park", "Move a candidate to needs_input so it stops delaying the train", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Park(op) }),
		queueOpCmd("resume", "Return a parked or retry-waiting candidate to the queue with a fresh attempt budget", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Resume(op) }),
		queueOpCmd("emergency", "Move a candidate into the priority emergency lane", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.MarkEmergency(op) }),
		queueOpCmd("override", "Human immediate-merge: emergency priority plus a durable, attributed gate waiver", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Override(op) }),
		queueApproveCmd(),
		queueOpCmd("unapprove", "Withdraw a steward approval and return the candidate to the approval hold", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Unapprove(op) }),
		queueOpCmd("reject", "Remove a candidate from the queue; branches and evidence are retained", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Reject(op) }),
	)
	return cmd
}

func queueApproveCmd() *cobra.Command {
	var project, actor, reason, manifest, tree, receiptDigest string
	cmd := &cobra.Command{Use: "approve <candidate-id>", Short: "Record a steward approval bound to the current prepared candidate", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(actor) == "" {
			actor = os.Getenv("USER")
		}
		c, err := (queue.Store{ProjectRoot: project}).Approve(queue.ApprovalOp{ID: args[0], Actor: actor, Reason: reason, ManifestDigest: manifest, TreeSHA: tree, ReceiptDigest: receiptDigest})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(c)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&actor, "actor", "", "acting steward recorded in evidence (defaults to $USER)")
	cmd.Flags().StringVar(&reason, "reason", "", "steward reason recorded in evidence")
	cmd.Flags().StringVar(&manifest, "manifest", "", "current immutable wave or release manifest digest")
	cmd.Flags().StringVar(&tree, "tree", "", "optional prospective tree SHA to verify")
	cmd.Flags().StringVar(&receiptDigest, "receipt-digest", "", "optional gate receipt digest to verify")
	_ = cmd.MarkFlagRequired("manifest")
	return cmd
}

// queueOpCmd is the shared shape of the human-override verbs. Every verb is
// audited: the acting user and reason land in the candidate's durable
// evidence.
func queueOpCmd(verb, short string, run func(queue.Store, queue.Op) (queue.Candidate, error)) *cobra.Command {
	var project, actor, reason string
	cmd := &cobra.Command{Use: verb + " <candidate-id>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(actor) == "" {
			actor = os.Getenv("USER")
		}
		c, err := run(queue.Store{ProjectRoot: project}, queue.Op{ID: args[0], Actor: actor, Reason: reason})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(c)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&actor, "actor", "", "acting operator recorded in evidence (defaults to $USER)")
	cmd.Flags().StringVar(&reason, "reason", "", "human reason recorded in evidence")
	return cmd
}
func queueSubmitCmd() *cobra.Command {
	var project, branch, sha, receiptPath, backend, policy, manifest, runtimeInstance, runtimeReceipt, target, targetBase, targetPolicy string
	var paths []string
	var requiredReceipts []string
	cmd := &cobra.Command{Use: "submit", Short: "Admit a receipt-bound candidate", RunE: func(cmd *cobra.Command, _ []string) error {
		raw, err := os.ReadFile(receiptPath)
		if err != nil {
			return fmt.Errorf("queue: read receipt: %w", err)
		}
		var r receipt.Receipt
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("queue: parse receipt: %w", err)
		}
		c, err := (queue.Store{ProjectRoot: project}).Submit(queue.Submit{Branch: branch, SHA: sha, Receipt: r, Backend: backend, Paths: paths, TargetRef: target, TargetBaseSHAAtAdmission: targetBase, TargetPolicy: queue.TargetPolicy(targetPolicy), FinalizationPolicy: queue.FinalizationPolicy(policy), ManifestDigest: manifest, RuntimeInstance: runtimeInstance, RuntimeReceipt: runtimeReceipt, RequiredReceiptIDs: requiredReceipts})
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
	cmd.Flags().StringVar(&target, "target", "staging/local", "protected destination ref bound at admission")
	cmd.Flags().StringVar(&targetBase, "target-base-sha", "", "protected target SHA observed at admission")
	cmd.Flags().StringVar(&targetPolicy, "target-policy", string(queue.WaveAutoPolicy), "target policy: wave-auto or steward-approved")
	cmd.Flags().StringSliceVar(&paths, "path", nil, "changed path (repeatable)")
	cmd.Flags().StringVar(&policy, "finalization-policy", string(queue.AutonomousFinalization), "finalization policy: autonomous or steward_review")
	cmd.Flags().StringVar(&manifest, "manifest", "", "immutable wave or release manifest digest required by steward_review")
	cmd.Flags().StringVar(&runtimeInstance, "runtime-instance", "", "optional exact runtime instance identifier required for review")
	cmd.Flags().StringVar(&runtimeReceipt, "runtime-receipt", "", "optional runtime receipt identifier required for review")
	cmd.Flags().StringSliceVar(&requiredReceipts, "required-receipt", nil, "additional receipt id required before approval (repeatable)")
	_ = cmd.MarkFlagRequired("branch")
	_ = cmd.MarkFlagRequired("sha")
	_ = cmd.MarkFlagRequired("receipt")
	return cmd
}
func queueStatusCmd() *cobra.Command {
	var project string
	var jsonOut bool
	cmd := &cobra.Command{Use: "status", Aliases: []string{"list"}, Short: "Show durable merge-queue candidates", RunE: func(cmd *cobra.Command, _ []string) error {
		state, err := (queue.Store{ProjectRoot: project}).List()
		if err != nil {
			return err
		}
		if jsonOut {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(state)
		}
		for _, candidate := range state.Candidates {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), queue.StatusLine(candidate, time.Now().UTC())); err != nil {
				return err
			}
		}
		return nil
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the durable queue record as JSON")
	return cmd
}

// worker is the durable owner of preparation leases and protected finalization.
// `process` remains available for scripts that want one compatibility drain.
func queueWorkerCmd() *cobra.Command {
	var project, gate, target, resolver, repair, workerID string
	var once bool
	var retryDelay, maxRetryDelay time.Duration
	var maxAttempts int
	cmd := &cobra.Command{Use: "worker", Short: "Run the merge-train worker", RunE: func(cmd *cobra.Command, _ []string) error {
		deps := queueProcessDeps(project, gate, target, resolver, repair, workerID)
		deps.RetryDelay, deps.MaxRetryDelay, deps.MaxAttempts = retryDelay, maxRetryDelay, maxAttempts
		worker := queue.Worker{Store: queue.Store{ProjectRoot: project}, Deps: deps}
		for {
			progressed, err := worker.RunOnce(cmd.Context())
			if err != nil {
				return err
			}
			if once {
				state, err := (queue.Store{ProjectRoot: project}).List()
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(state)
			}
			if !progressed {
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(250 * time.Millisecond):
				}
				continue
			}
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&gate, "gate", "", "deterministic command run against each prepared tree")
	cmd.Flags().StringVar(&target, "target", "staging/local", "protected destination ref")
	cmd.Flags().StringVar(&resolver, "resolver", "", "bounded resolver command for protected-target continuations")
	cmd.Flags().StringVar(&repair, "repair", "", "bounded repair command for a red deterministic gate")
	cmd.Flags().StringVar(&workerID, "worker-id", "", "durable worker owner token")
	cmd.Flags().BoolVar(&once, "once", false, "perform one claim, preparation, or finalization step")
	cmd.Flags().DurationVar(&retryDelay, "retry-delay", queue.DefaultRetryDelay, "base backoff before a failed candidate is retried")
	cmd.Flags().DurationVar(&maxRetryDelay, "max-retry-delay", queue.DefaultMaxRetryDelay, "backoff ceiling for repeated failures")
	cmd.Flags().IntVar(&maxAttempts, "max-attempts", queue.DefaultMaxAttempts, "attempts before a failing candidate parks as needs_input")
	_ = cmd.MarkFlagRequired("gate")
	return cmd
}

// process uses the managed staging-capsule lifecycle and requires an explicit
// deterministic gate. It has no raw-main fallback.
func queueProcessCmd() *cobra.Command {
	var project, gate, target, resolver, repair string
	cmd := &cobra.Command{Use: "process", Aliases: []string{"drain"}, Short: "Process candidates through a configured protected integration", RunE: func(cmd *cobra.Command, _ []string) error {
		state, err := (queue.Store{ProjectRoot: project}).Process(cmd.Context(), queueProcessDeps(project, gate, target, resolver, repair, ""))
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(state)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&gate, "gate", "", "deterministic command run against each speculative tree")
	cmd.Flags().StringVar(&target, "target", "staging/local", "protected destination ref")
	cmd.Flags().StringVar(&resolver, "resolver", "", "bounded resolver command for protected-target continuations")
	cmd.Flags().StringVar(&repair, "repair", "", "bounded repair command for a red deterministic gate")
	_ = cmd.MarkFlagRequired("gate")
	return cmd
}

// queueProcessDeps preserves the existing staging/local drain by default. An
// explicit destination switches both preparation and finalization to the same
// protected ref, so the final compare-and-swap cannot land somewhere else.
func queueProcessDeps(project, gate, target, resolver, repair, workerID string) queue.ProcessDeps {
	if strings.TrimSpace(target) == "" {
		target = "staging/local"
	}
	deps := queue.ProcessDeps{
		Gate:        queue.ShellGate{Command: gate},
		WorkerID:    workerID,
		GateVersion: gate,
		TargetRef:   target,
	}
	if target == "staging/local" {
		deps.Integration = queue.StagingIntegration{ProjectRoot: project, GateCommand: gate}
	} else {
		deps.Integration = queue.ProtectedIntegration{
			ProjectRoot:     project,
			TargetRef:       target,
			ResolverCommand: resolver,
		}
		deps.Finalizer = queue.ProtectedFinalizer{ProjectRoot: project, TargetRef: target}
	}
	if strings.TrimSpace(repair) != "" {
		deps.Repairer = queue.ShellRepairer{Command: repair}
	}
	return deps
}

// queueMigrateCmd is the only v1 upgrade path. It requires an explicit
// operator-selected target before the queue state is rewritten as v2.
func queueMigrateCmd() *cobra.Command {
	var project, target, base, policy string
	cmd := &cobra.Command{Use: "migrate", Short: "Explicitly migrate a legacy merge queue to target-bound v2", RunE: func(cmd *cobra.Command, _ []string) error {
		state, err := (queue.Store{ProjectRoot: project, LegacyTargetRef: target, LegacyTargetBaseSHAAtAdmission: base, LegacyTargetPolicy: queue.TargetPolicy(policy)}).List()
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(state)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&target, "target", "", "target ref to bind every legacy candidate to")
	cmd.Flags().StringVar(&base, "target-base-sha", "", "target SHA observed at legacy migration")
	cmd.Flags().StringVar(&policy, "target-policy", string(queue.WaveAutoPolicy), "target policy: wave-auto or steward-approved")
	_ = cmd.MarkFlagRequired("target")
	return cmd
}
