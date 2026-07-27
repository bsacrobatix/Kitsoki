package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/receipt"
)

func queueCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "queue", Short: "Submit verified candidates to the Capsule merge queue"}
	cmd.AddCommand(queueSubmitCmd(), queueSubmitExternalCmd(), queueAdmissionServeCmd(), queueCapsulePromotionExecutorCmd(), queueStatusCmd(), queueProcessCmd(), queueWorkerCmd(), queueMigrateCmd(), queueSweepCmd())
	cmd.AddCommand(
		queueOpCmd("kick", "Clear a retry_wait candidate's backoff timer for an immediate retry", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Kick(op) }),
		queueOpCmd("park", "Move a candidate to needs_input so it stops delaying the train", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Park(op) }),
		queueOpCmd("resume", "Return a parked or retry-waiting candidate to the queue with a fresh attempt budget", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Resume(op) }),
		queueOpCmd("emergency", "Move a candidate into the priority emergency lane", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.MarkEmergency(op) }),
		queueOpCmd("override", "Human immediate-merge: emergency priority plus a durable, attributed gate waiver", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Override(op) }),
		queueApproveCmd(),
		queueOpCmd("unapprove", "Withdraw a steward approval and return the candidate to the approval hold", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Unapprove(op) }),
		queueOpCmd("reject", "Remove a candidate from the queue; branches and evidence are retained", func(s queue.Store, op queue.Op) (queue.Candidate, error) { return s.Reject(op) }),
		queueOpCmd("reconcile-landing", "Strictly reconcile a managed staging helper's actual landed commit", func(s queue.Store, op queue.Op) (queue.Candidate, error) {
			return s.ReconcileLanding(op)
		}),
	)
	return cmd
}

func queueApproveCmd() *cobra.Command {
	var project, queueRoot, actor, reason, manifest, tree, receiptDigest string
	cmd := &cobra.Command{Use: "approve <candidate-id>", Short: "Record a steward approval bound to the current prepared candidate", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(actor) == "" {
			actor = os.Getenv("USER")
		}
		c, err := (queue.Store{ProjectRoot: project, QueueRoot: queueRoot}).Approve(queue.ApprovalOp{ID: args[0], Actor: actor, Reason: reason, ManifestDigest: manifest, TreeSHA: tree, ReceiptDigest: receiptDigest})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(c)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&queueRoot, "queue-root", "", "exact external queue authority directory (default <project>/.capsules/queue)")
	cmd.Flags().StringVar(&actor, "actor", "", "acting steward recorded in evidence (defaults to $USER)")
	cmd.Flags().StringVar(&reason, "reason", "", "steward reason recorded in evidence")
	cmd.Flags().StringVar(&manifest, "manifest", "", "current immutable wave or release manifest digest")
	cmd.Flags().StringVar(&tree, "tree", "", "optional prospective tree SHA to verify")
	cmd.Flags().StringVar(&receiptDigest, "receipt-digest", "", "optional gate receipt digest to verify")
	_ = cmd.MarkFlagRequired("manifest")
	return cmd
}

// queueSweepCmd triages parked (needs_input / needs_conflict_input)
// candidates in bulk: classify superseded / environment_degraded / stale /
// unclassified, and — by default only report the plan (--dry-run is
// implicit; pass --apply to act). The only classification --apply ever
// acts on is superseded, via the existing audited Reject verb; every other
// classification is surfaced for a human, never auto-mutated. See
// queue.Store.Sweep's doc for why.
func queueSweepCmd() *cobra.Command {
	var project, queueRoot, actor, reason string
	var staleAfter time.Duration
	var apply bool
	cmd := &cobra.Command{Use: "sweep", Short: "Bulk-triage parked candidates: classify superseded/stale/environment-degraded, --apply to reject the superseded ones", RunE: func(cmd *cobra.Command, _ []string) error {
		store := queue.Store{ProjectRoot: project, QueueRoot: queueRoot}
		plan, err := store.Sweep(time.Time{}, staleAfter)
		if err != nil {
			return err
		}
		if !apply {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
		}
		if strings.TrimSpace(actor) == "" {
			actor = os.Getenv("USER")
		}
		acted, err := store.ApplySweep(plan, actor, reason)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Plan  queue.SweepPlan   `json:"plan"`
			Acted []queue.Candidate `json:"acted"`
		}{Plan: plan, Acted: acted})
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&queueRoot, "queue-root", "", "exact external queue authority directory (default <project>/.capsules/queue)")
	cmd.Flags().StringVar(&actor, "actor", "", "acting operator recorded in evidence for --apply (defaults to $USER)")
	cmd.Flags().StringVar(&reason, "reason", "", "override the per-entry sweep reason recorded in evidence for --apply")
	cmd.Flags().DurationVar(&staleAfter, "stale-after", queue.DefaultSweepStaleAfter, "how long a parked candidate sits untouched before it is classified stale")
	cmd.Flags().BoolVar(&apply, "apply", false, "execute the plan's proposed actions (reject superseded candidates); without this flag the sweep only reports the plan")
	return cmd
}

// queueOpCmd is the shared shape of the human-override verbs. Every verb is
// audited: the acting user and reason land in the candidate's durable
// evidence.
func queueOpCmd(verb, short string, run func(queue.Store, queue.Op) (queue.Candidate, error)) *cobra.Command {
	var project, queueRoot, actor, reason string
	cmd := &cobra.Command{Use: verb + " <candidate-id>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(actor) == "" {
			actor = os.Getenv("USER")
		}
		c, err := run(queue.Store{ProjectRoot: project, QueueRoot: queueRoot}, queue.Op{ID: args[0], Actor: actor, Reason: reason})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(c)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&queueRoot, "queue-root", "", "exact external queue authority directory (default <project>/.capsules/queue)")
	cmd.Flags().StringVar(&actor, "actor", "", "acting operator recorded in evidence (defaults to $USER)")
	cmd.Flags().StringVar(&reason, "reason", "", "human reason recorded in evidence")
	return cmd
}
func queueSubmitCmd() *cobra.Command {
	var project, queueRoot, branch, sha, receiptPath, backend, policy, manifest, runtimeInstance, runtimeReceipt, target, targetBase, targetPolicy string
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
		c, err := (queue.Store{ProjectRoot: project, QueueRoot: queueRoot}).Submit(queue.Submit{Branch: branch, SHA: sha, Receipt: r, Backend: backend, Paths: paths, TargetRef: target, TargetBaseSHAAtAdmission: targetBase, TargetPolicy: queue.TargetPolicy(targetPolicy), FinalizationPolicy: queue.FinalizationPolicy(policy), ManifestDigest: manifest, RuntimeInstance: runtimeInstance, RuntimeReceipt: runtimeReceipt, RequiredReceiptIDs: requiredReceipts})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(c)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&queueRoot, "queue-root", "", "exact external queue authority directory (default <project>/.capsules/queue)")
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

func queueSubmitExternalCmd() *cobra.Command {
	var project, queueRoot, resultPath, bundlePath, receiptPath, targetBase, targetPolicy, policy, runtimeInstance, runtimeReceipt string
	var paths, requiredReceipts []string
	cmd := &cobra.Command{
		Use:   "submit-external",
		Short: "Verify a typed external worker result and Git bundle into queue-private storage, then admit it",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := readQueueExternalResult(resultPath)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(receiptPath)
			if err != nil {
				return fmt.Errorf("queue: read receipt: %w", err)
			}
			var r receipt.Receipt
			if err := json.Unmarshal(raw, &r); err != nil {
				return fmt.Errorf("queue: parse receipt: %w", err)
			}
			candidate, anchor, err := (queue.Store{ProjectRoot: project, QueueRoot: queueRoot}).AdmitExternalBundle(cmd.Context(), queue.ExternalBundleSubmission{
				Result:     result,
				BundlePath: bundlePath,
				Submit: queue.Submit{
					Branch: result.Branch, SHA: result.CandidateSHA,
					TargetRef: result.TargetRef, TargetBaseSHAAtAdmission: targetBase,
					TargetPolicy: queue.TargetPolicy(targetPolicy), Receipt: r, ReceiptRef: receiptPath,
					Backend: "external-worker", Paths: paths,
					FinalizationPolicy: queue.FinalizationPolicy(policy), ManifestDigest: result.ManifestDigest,
					RuntimeInstance: runtimeInstance, RuntimeReceipt: runtimeReceipt,
					RequiredReceiptIDs: requiredReceipts,
				},
			})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Anchor    queue.ExternalBundleAnchor `json:"anchor"`
				Candidate queue.Candidate            `json:"candidate"`
			}{Anchor: anchor, Candidate: candidate})
		},
	}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&queueRoot, "queue-root", "", "exact external queue authority directory (default <project>/.capsules/queue)")
	cmd.Flags().StringVar(&resultPath, "result", "", "strict capsule-external-worker-result/v1 JSON file")
	cmd.Flags().StringVar(&bundlePath, "bundle", "", "already-downloaded local Git bundle")
	cmd.Flags().StringVar(&receiptPath, "receipt", "", "promotion-eligible Capsule CI receipt JSON")
	cmd.Flags().StringVar(&targetBase, "target-base-sha", "", "protected target SHA observed at admission")
	cmd.Flags().StringVar(&targetPolicy, "target-policy", string(queue.WaveAutoPolicy), "target policy: wave-auto or steward-approved")
	cmd.Flags().StringSliceVar(&paths, "path", nil, "changed path (repeatable)")
	cmd.Flags().StringVar(&policy, "finalization-policy", string(queue.AutonomousFinalization), "finalization policy: autonomous or steward_review")
	cmd.Flags().StringVar(&runtimeInstance, "runtime-instance", "", "optional exact runtime instance identifier required for review")
	cmd.Flags().StringVar(&runtimeReceipt, "runtime-receipt", "", "optional runtime receipt identifier required for review")
	cmd.Flags().StringSliceVar(&requiredReceipts, "required-receipt", nil, "additional receipt id required before approval (repeatable)")
	_ = cmd.MarkFlagRequired("result")
	_ = cmd.MarkFlagRequired("bundle")
	_ = cmd.MarkFlagRequired("receipt")
	_ = cmd.MarkFlagRequired("target-base-sha")
	return cmd
}

func readQueueExternalResult(path string) (queue.ExternalWorkerResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return queue.ExternalWorkerResult{}, fmt.Errorf("queue: inspect external result: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 64<<10 {
		return queue.ExternalWorkerResult{}, fmt.Errorf("queue: external result must be a regular non-symlink file no larger than 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return queue.ExternalWorkerResult{}, fmt.Errorf("queue: open external result: %w", err)
	}
	defer file.Close()
	var result queue.ExternalWorkerResult
	decoder := json.NewDecoder(io.LimitReader(file, (64<<10)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return queue.ExternalWorkerResult{}, fmt.Errorf("queue: parse external result: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return queue.ExternalWorkerResult{}, fmt.Errorf("queue: parse external result: trailing JSON value")
	}
	return result, nil
}
func queueStatusCmd() *cobra.Command {
	var project, queueRoot string
	var jsonOut bool
	cmd := &cobra.Command{Use: "status", Aliases: []string{"list"}, Short: "Show durable merge-queue candidates", RunE: func(cmd *cobra.Command, _ []string) error {
		state, err := (queue.Store{ProjectRoot: project, QueueRoot: queueRoot}).List()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if jsonOut {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(queue.Report(state, now))
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), queueSummaryLine(queue.Summarize(state, now))); err != nil {
			return err
		}
		for _, candidate := range state.Candidates {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), queue.StatusLine(candidate, now)); err != nil {
				return err
			}
		}
		return nil
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&queueRoot, "queue-root", "", "exact external queue authority directory (default <project>/.capsules/queue)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the durable queue record as JSON")
	return cmd
}

// queueSummaryLine is the single-line human roll-up printed above the
// per-candidate StatusLines: train depth, parked count/age, and per-phase
// and per-retry-reason counts — "where did the last N minutes go" without
// eyeballing every candidate line.
func queueSummaryLine(s queue.StatusSummary) string {
	phases := make([]string, 0, len(s.PhaseCounts))
	for phase, n := range s.PhaseCounts {
		phases = append(phases, fmt.Sprintf("%s=%d", phase, n))
	}
	sort.Strings(phases)
	line := fmt.Sprintf("train_depth=%d parked=%d", s.TrainDepth, s.ParkedCount)
	if s.OldestParkedAge != "" {
		line += " oldest_parked=" + s.OldestParkedAge
	}
	if len(phases) > 0 {
		line += " phases[" + strings.Join(phases, " ") + "]"
	}
	if len(s.RetryReasonCounts) > 0 {
		reasons := make([]string, 0, len(s.RetryReasonCounts))
		for reason, n := range s.RetryReasonCounts {
			reasons = append(reasons, fmt.Sprintf("%s=%d", reason, n))
		}
		sort.Strings(reasons)
		line += " retry_reasons[" + strings.Join(reasons, " ") + "]"
	}
	return line
}

// worker is the durable owner of preparation leases and protected finalization.
// `process` remains available for scripts that want one compatibility drain.
func queueWorkerCmd() *cobra.Command {
	var project, queueRoot, gate, target, resolver, repair, workerID, executorName, executorPipeline string
	var once bool
	var concurrency int
	var retryDelay, maxRetryDelay, envRetryDelay, maxEnvDuration time.Duration
	var maxAttempts int
	cmd := &cobra.Command{Use: "worker", Short: "Run the merge-train worker", RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(gate) != "" && strings.TrimSpace(executorName) != "" {
			return fmt.Errorf("queue worker: --gate and --executor are mutually exclusive")
		}
		if strings.TrimSpace(gate) == "" && strings.TrimSpace(executorName) == "" {
			return fmt.Errorf("queue worker: exactly one of --gate or --executor is required")
		}
		deps := queueProcessDepsWithRoot(project, queueRoot, gate, target, resolver, repair, workerID)
		if strings.TrimSpace(executorName) != "" {
			pipeline := executorPipeline
			if strings.TrimSpace(pipeline) == "" {
				pipeline = "change"
			}
			deps.Gate = queue.ExecutorGate{ProjectRoot: project, Executor: executorName, Pipeline: pipeline}
			deps.GateVersion = fmt.Sprintf("executor:%s:%s", executorName, pipeline)
		}
		deps.RetryDelay, deps.MaxRetryDelay, deps.MaxAttempts = retryDelay, maxRetryDelay, maxAttempts
		deps.EnvRetryDelay, deps.MaxEnvDuration = envRetryDelay, maxEnvDuration
		n := concurrency
		if n < 1 {
			n = 1
		}
		store := queue.Store{ProjectRoot: project, QueueRoot: queueRoot}
		if n > 1 {
			// n goroutines in this one process share the same durable
			// state.json and its file lock. That contention is brief (the
			// lock is held only for the short claim/update, never across a
			// gate run — see the queue package doc), but with the default
			// zero LockWait any two goroutines racing that file lock at the
			// same instant fail each other outright with ErrBusy instead of
			// the brief wait a real conflict deserves.
			store.LockWait = 2 * time.Second
		}
		if n == 1 {
			if err := runQueueWorkerLoop(cmd.Context(), store, deps, once); err != nil {
				return err
			}
		} else {
			// Each of the n loops claims and prepares independently under
			// its own worker-id lease (leases already isolate concurrent
			// claimants, and the B1 heartbeat keeps a long gate's lease
			// alive) — only the FIFO/emergency head ever finalizes, so
			// concurrent workers cannot land out of order or double-land.
			base := first(workerID, "queue-worker")
			var wg sync.WaitGroup
			errs := make(chan error, n)
			for i := 1; i <= n; i++ {
				workerDeps := deps
				workerDeps.WorkerID = fmt.Sprintf("%s-%d", base, i)
				wg.Add(1)
				go func(d queue.ProcessDeps) {
					defer wg.Done()
					errs <- runQueueWorkerLoop(cmd.Context(), store, d, once)
				}(workerDeps)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					return err
				}
			}
		}
		if !once {
			return nil
		}
		state, err := store.List()
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(state)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&queueRoot, "queue-root", "", "exact external queue authority directory (default <project>/.capsules/queue)")
	cmd.Flags().StringVar(&gate, "gate", "", "deterministic command run against each prepared tree")
	cmd.Flags().StringVar(&target, "target", "staging/local", "protected destination ref")
	cmd.Flags().StringVar(&resolver, "resolver", "", "bounded resolver command for protected-target continuations")
	cmd.Flags().StringVar(&repair, "repair", "", "bounded repair command for a red deterministic gate")
	cmd.Flags().StringVar(&workerID, "worker-id", "", "durable worker owner token (suffixed -1.. -N under --concurrency)")
	cmd.Flags().StringVar(&executorName, "executor", "", "named capsule CI executor (from the project's .kitsoki/ci.yaml catalog) to run the deterministic gate remotely through internal/capsule/ci, instead of --gate's local shell command; mutually exclusive with --gate")
	cmd.Flags().StringVar(&executorPipeline, "executor-pipeline", "change", "capsule CI pipeline name dispatched via --executor")
	cmd.Flags().BoolVar(&once, "once", false, "perform one claim, preparation, or finalization step per concurrent worker")
	cmd.Flags().IntVar(&concurrency, "concurrency", 1, "number of preparation workers to run concurrently in this process; each claims and prepares independently, only the FIFO/emergency head ever finalizes")
	cmd.Flags().DurationVar(&retryDelay, "retry-delay", queue.DefaultRetryDelay, "base backoff before a failed candidate is retried")
	cmd.Flags().DurationVar(&maxRetryDelay, "max-retry-delay", queue.DefaultMaxRetryDelay, "backoff ceiling for repeated failures")
	cmd.Flags().IntVar(&maxAttempts, "max-attempts", queue.DefaultMaxAttempts, "attempts before a failing candidate parks as needs_input")
	cmd.Flags().DurationVar(&envRetryDelay, "env-retry-delay", queue.DefaultEnvRetryDelay, "fixed backoff before retrying an environmental failure (fetch/lock/workspace-create); does not consume the attempt budget")
	cmd.Flags().DurationVar(&maxEnvDuration, "max-env-duration", queue.DefaultMaxEnvDuration, "wall-clock bound on a persistent environmental-failure streak before parking as needs_input")
	return cmd
}

// runQueueWorkerLoop drives one worker's claim/prepare/finalize loop to
// completion (--once: exactly one step) or until ctx is cancelled. It never
// prints; the caller decides when and how often to report state, so running
// several of these concurrently under --concurrency does not interleave or
// duplicate output.
func runQueueWorkerLoop(ctx context.Context, store queue.Store, deps queue.ProcessDeps, once bool) error {
	worker := queue.Worker{Store: store, Deps: deps}
	for {
		progressed, err := worker.RunOnce(ctx)
		if err != nil {
			return err
		}
		if once {
			return nil
		}
		delay := 250 * time.Millisecond
		if !progressed {
			// There is no filesystem watcher on the durable queue. Polling once
			// per second bounds admission latency while keeping an idle worker
			// from repeatedly decoding a multi-megabyte queue state four times a
			// second.
			delay = time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

func first(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// process uses the managed staging-capsule lifecycle and requires an explicit
// deterministic gate. It has no raw-main fallback.
func queueProcessCmd() *cobra.Command {
	var project, queueRoot, gate, target, resolver, repair string
	cmd := &cobra.Command{Use: "process", Aliases: []string{"drain"}, Short: "Process candidates through a configured protected integration", RunE: func(cmd *cobra.Command, _ []string) error {
		store := queue.Store{ProjectRoot: project, QueueRoot: queueRoot}
		state, err := store.Process(cmd.Context(), queueProcessDepsWithRoot(project, queueRoot, gate, target, resolver, repair, ""))
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(state)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&queueRoot, "queue-root", "", "exact external queue authority directory (default <project>/.capsules/queue)")
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
	return queueProcessDepsWithRoot(project, "", gate, target, resolver, repair, workerID)
}

func queueProcessDepsWithRoot(project, queueRoot, gate, target, resolver, repair, workerID string) queue.ProcessDeps {
	if strings.TrimSpace(target) == "" {
		target = "staging/local"
	}
	deps := queue.ProcessDeps{
		Gate:        queue.ShellGate{Command: gate},
		WorkerID:    workerID,
		GateVersion: gate,
		TargetRef:   target,
		GateMemo:    queue.FileGateMemo{ProjectRoot: project, QueueRoot: queueRoot},
	}
	deps.Integration = queue.ProtectedIntegration{
		ProjectRoot:     project,
		QueueRoot:       queueRoot,
		TargetRef:       target,
		ResolverCommand: resolver,
	}
	deps.Finalizer = queue.ProtectedFinalizer{ProjectRoot: project, TargetRef: target}
	if strings.TrimSpace(repair) != "" {
		deps.Repairer = queue.ShellRepairer{Command: repair}
	}
	return deps
}

// queueMigrateCmd is the only v1 upgrade path. It requires an explicit
// operator-selected target before the queue state is rewritten as v2.
func queueMigrateCmd() *cobra.Command {
	var project, queueRoot, target, base, policy string
	cmd := &cobra.Command{Use: "migrate", Short: "Explicitly migrate a legacy merge queue to target-bound v2", RunE: func(cmd *cobra.Command, _ []string) error {
		state, err := (queue.Store{ProjectRoot: project, QueueRoot: queueRoot, LegacyTargetRef: target, LegacyTargetBaseSHAAtAdmission: base, LegacyTargetPolicy: queue.TargetPolicy(policy)}).List()
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(state)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&queueRoot, "queue-root", "", "exact external queue authority directory (default <project>/.capsules/queue)")
	cmd.Flags().StringVar(&target, "target", "", "target ref to bind every legacy candidate to")
	cmd.Flags().StringVar(&base, "target-base-sha", "", "target SHA observed at legacy migration")
	cmd.Flags().StringVar(&policy, "target-policy", string(queue.WaveAutoPolicy), "target policy: wave-auto or steward-approved")
	_ = cmd.MarkFlagRequired("target")
	return cmd
}
