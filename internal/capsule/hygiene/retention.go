package hygiene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kitsoki/internal/atomicfile"
)

const (
	RetentionReceiptSchema = "capsule-workspace-retention/v1"
	PurgeReceiptSchema     = "capsule-workspace-purge/v1"
	RetentionClearSchema   = "capsule-workspace-retention-clear/v1"
	purgeIntentSchema      = "capsule-workspace-purge-intent/v1"
	defaultPurgeMaxBytes   = int64(8 << 30)
	defaultPurgeMinFree    = int64(16 << 20)
	purgeFreeSlack         = int64(1 << 20)
)

// RetentionProbe records an independent, durable close-time liveness fact.
// Purge still repeats live activity checks; these fields prove that closure
// itself was not authorized from an unknown process snapshot.
type RetentionProbe struct {
	Kind       string    `json:"kind"`
	CapturedAt time.Time `json:"captured_at"`
	Safe       bool      `json:"safe"`
}

// RetentionReceipt is produced by the dispatch finalizer/reaper before close.
// It binds purge to one exact closed quarantine and recovery ref.
type RetentionReceipt struct {
	Schema           string         `json:"schema"`
	Project          string         `json:"project"`
	ProjectStateRoot string         `json:"project_state_root,omitempty"`
	WorkspaceID      string         `json:"workspace_id"`
	WorkspacePath    string         `json:"workspace_path"`
	Head             string         `json:"head"`
	RecoveryRef      string         `json:"recovery_ref"`
	IssuedAt         time.Time      `json:"issued_at"`
	EligibleAfter    time.Time      `json:"eligible_after"`
	ProcessSnapshot  RetentionProbe `json:"process_snapshot"`
	ActivityProbe    RetentionProbe `json:"activity_probe"`
}

// ProbeWorkspaceActivity exposes the same fail-closed lsof inventory used by
// cleanup so native close can retain two point-in-time liveness proofs around
// the atomic quarantine rename.
func ProbeWorkspaceActivity(ctx context.Context, paths []string) (WorkspaceActivity, error) {
	return readWorkspaceActivity(ctx, paths)
}

// ProbeWorkspaceProcessCommands is independent of the open-file inventory: it
// takes one complete process-command snapshot and reports exact workspace paths
// still named by a running command. The post-rename lsof probe remains
// authoritative for cwd and descriptor ownership that command lines omit.
func ProbeWorkspaceProcessCommands(ctx context.Context, paths []string) (WorkspaceActivity, error) {
	activity := WorkspaceActivity{PIDsByPath: map[string][]int{}}
	ps, err := exec.LookPath("ps")
	if err != nil {
		activity.Reason = "ps is unavailable"
		return activity, nil
	}
	output, err := exec.CommandContext(ctx, ps, "-axww", "-o", "pid=,command=").Output()
	if err != nil {
		return WorkspaceActivity{}, fmt.Errorf("workspace process snapshot: %w", err)
	}
	for lineNumber, raw := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		fields := strings.Fields(raw)
		if len(fields) < 2 {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			return WorkspaceActivity{}, fmt.Errorf("workspace process snapshot line %d is malformed", lineNumber+1)
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			return WorkspaceActivity{}, fmt.Errorf("workspace process snapshot line %d has an invalid pid", lineNumber+1)
		}
		command := strings.TrimSpace(strings.TrimPrefix(raw, fields[0]))
		for _, path := range paths {
			if strings.Contains(command, path) {
				activity.PIDsByPath[path] = append(activity.PIDsByPath[path], pid)
			}
		}
	}
	activity.Known = true
	return activity, nil
}

// WriteRetentionReceipt persists one immutable project-scoped close receipt.
// Repeating the exact receipt is idempotent; a same-identity byte conflict is
// never overwritten.
func WriteRetentionReceipt(projectRoot string, receipt RetentionReceipt) (string, error) {
	root, err := canonicalRoot(projectRoot)
	if err != nil {
		return "", err
	}
	if err := validateRetentionReceiptBinding(root, receipt); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", fmt.Errorf("capsule retention: encode receipt: %w", err)
	}
	raw = append(raw, '\n')
	relative := filepath.ToSlash(filepath.Join(".capsules", "retention", "receipts", receipt.WorkspaceID+".json"))
	path := filepath.Join(root, filepath.FromSlash(relative))
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if !bytes.Equal(existing, raw) {
			return "", fmt.Errorf("capsule retention: immutable receipt conflict at %s", relative)
		}
		return relative, nil
	} else if !os.IsNotExist(readErr) {
		return "", fmt.Errorf("capsule retention: inspect receipt: %w", readErr)
	}
	if err := atomicfile.WriteFile(path, raw, 0o600, 0o700); err != nil {
		return "", fmt.Errorf("capsule retention: write receipt: %w", err)
	}
	return relative, nil
}

type PurgeOptions struct {
	ProjectRoot           string
	Receipt               RetentionReceipt
	MinAge                time.Duration
	KeepWorkspaces        int
	MaxBytes              int64
	CurrentPath           string
	PinnedWorkspaceIDs    []string
	ReadWorkspaceActivity func(context.Context, []string) (WorkspaceActivity, error)
	ReadDiskUsage         func(string) (DiskUsage, error)
	CloseWorkspace        func(context.Context, string, Candidate) error
	MinOperationFreeBytes int64
	Now                   func() time.Time
}

type PurgeResult struct {
	Schema        string     `json:"schema"`
	OK            bool       `json:"ok"`
	Status        string     `json:"status"`
	ReasonCode    string     `json:"reason_code,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	WorkspaceID   string     `json:"workspace_id"`
	WorkspacePath string     `json:"workspace_path"`
	Head          string     `json:"head"`
	RecoveryRef   string     `json:"recovery_ref"`
	IntentPath    string     `json:"intent_path,omitempty"`
	PurgedAt      time.Time  `json:"purged_at"`
	Bytes         int64      `json:"bytes,omitempty"`
	AlreadyAbsent bool       `json:"already_absent,omitempty"`
	Candidate     *Candidate `json:"candidate,omitempty"`
	DiskBefore    int64      `json:"disk_before_bytes,omitempty"`
	DiskAfter     int64      `json:"disk_after_bytes,omitempty"`
}

// RetentionClearResult is the bounded, archive-free result emitted by the
// operator cleanup command. A malformed or ineligible receipt is visible as a
// typed skip; it never prevents independent valid receipts from being purged.
type RetentionClearResult struct {
	Schema     string               `json:"schema"`
	Migrations []RetentionMigration `json:"migrations"`
	Purged     []PurgeResult        `json:"purged"`
	Skipped    []PurgeResult        `json:"skipped"`
}

// RetentionMigration makes a receiptless historical quarantine visible. A
// migration writes only its bounded receipt; it never archives or deletes the
// workspace. A later clear must still pass the ordinary receipt age, liveness,
// recovery-ref, and exact-path checks before it can purge anything.
type RetentionMigration struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	ReceiptPath   string `json:"receipt_path,omitempty"`
	Status        string `json:"status"`
	ReasonCode    string `json:"reason_code,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

type purgeIntent struct {
	Schema          string    `json:"schema"`
	ReceiptDigest   string    `json:"receipt_digest"`
	WorkspaceID     string    `json:"workspace_id"`
	OriginalPath    string    `json:"original_path"`
	PurgingPath     string    `json:"purging_path"`
	Head            string    `json:"head"`
	Branch          string    `json:"branch"`
	Target          string    `json:"target"`
	RecoveryRef     string    `json:"recovery_ref"`
	Bytes           int64     `json:"bytes"`
	Status          string    `json:"status"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
	DiskBeforeBytes int64     `json:"disk_before_bytes,omitempty"`
	DiskAfterBytes  int64     `json:"disk_after_bytes,omitempty"`
}

// PurgeClosedWorkspace removes exactly one receipt-bound closed quarantine
// without creating another archive. It records a bounded monotonic intent,
// atomically isolates the exact path, repeats the live safety proofs, and can
// resume an interrupted closed-purging state from that intent.
func PurgeClosedWorkspace(ctx context.Context, opts PurgeOptions) (PurgeResult, error) {
	root, err := canonicalRoot(opts.ProjectRoot)
	if err != nil {
		return PurgeResult{}, err
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	minAge := opts.MinAge
	if minAge == 0 {
		minAge = defaultWorkspaceAge
	}
	keep := opts.KeepWorkspaces
	if keep == 0 {
		keep = defaultKeepWorkspaces
	}
	maxBytes := opts.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultPurgeMaxBytes
	}
	minFree := opts.MinOperationFreeBytes
	if minFree == 0 {
		minFree = defaultPurgeMinFree
	}
	receipt := opts.Receipt
	if err := validateRetentionReceipt(root, receipt, now, minAge); err != nil {
		return PurgeResult{}, err
	}
	result := PurgeResult{
		Schema:        PurgeReceiptSchema,
		Status:        "planned",
		WorkspaceID:   receipt.WorkspaceID,
		WorkspacePath: receipt.WorkspacePath,
		Head:          receipt.Head,
		RecoveryRef:   receipt.RecoveryRef,
		PurgedAt:      now,
	}

	digest, err := retentionReceiptDigest(receipt)
	if err != nil {
		return PurgeResult{}, err
	}
	intentRel := filepath.ToSlash(filepath.Join(".capsules", "retention", "purges", digest+".json"))
	intentPath := filepath.Join(root, filepath.FromSlash(intentRel))
	result.IntentPath = intentRel
	intent, hasIntent, err := readPurgeIntent(intentPath, digest, receipt)
	if err != nil {
		return PurgeResult{}, err
	}

	originalRel := receipt.WorkspacePath
	purgingRel := filepath.ToSlash(filepath.Join(filepath.Dir(receipt.WorkspacePath), "closed-purging-"+strings.TrimPrefix(receipt.WorkspaceID, "closed-")))
	if strings.HasPrefix(receipt.WorkspaceID, "closed-purging-") {
		purgingRel = receipt.WorkspacePath
	}
	if hasIntent {
		originalRel = intent.OriginalPath
		purgingRel = intent.PurgingPath
	}
	originalPath := filepath.Join(root, filepath.FromSlash(originalRel))
	purgingPath := filepath.Join(root, filepath.FromSlash(purgingRel))
	originalExists, err := regularDirectoryState(originalPath)
	if err != nil {
		return PurgeResult{}, err
	}
	purgingExists := originalExists
	if purgingPath != originalPath {
		purgingExists, err = regularDirectoryState(purgingPath)
		if err != nil {
			return PurgeResult{}, err
		}
	}
	if originalPath != purgingPath && originalExists && purgingExists {
		return skipPurge(result, "path_conflict", "both the closed and closed-purging paths exist", nil), nil
	}
	var legacyShellIsolation *Candidate
	if !hasIntent && !originalExists && !purgingExists {
		candidate, found, discoverErr := discoverLegacyShellIsolation(ctx, root, receipt, opts, now, minAge)
		if discoverErr != nil {
			return skipPurge(result, "legacy_isolation_conflict", discoverErr.Error(), nil), nil
		}
		if found {
			legacyShellIsolation = &candidate
			purgingRel = candidate.Path
			purgingPath = filepath.Join(root, filepath.FromSlash(purgingRel))
			purgingExists = true
		}
	}
	if !originalExists && !purgingExists {
		if !hasIntent {
			return skipPurge(result, "missing_without_intent", "workspace is absent without a matching monotonic purge intent", nil), nil
		}
		intent.Status = "purged"
		if intent.CompletedAt.IsZero() {
			intent.CompletedAt = now
			if err := writePurgeIntent(intentPath, intent); err != nil {
				return PurgeResult{}, err
			}
		}
		result.OK = true
		result.Status = "purged"
		result.AlreadyAbsent = true
		result.Bytes = intent.Bytes
		result.DiskBefore = intent.DiskBeforeBytes
		result.DiskAfter = intent.DiskAfterBytes
		return result, nil
	}

	hygieneOpts := Options{
		ProjectRoot:                  root,
		KeepRuns:                     -1,
		KeepWorkspaces:               keep,
		MinWorkspaceAge:              minAge,
		MeasureWorkspaceBytes:        maxBytes > 0,
		PinnedWorkspaceIDs:           append([]string(nil), opts.PinnedWorkspaceIDs...),
		CurrentPath:                  opts.CurrentPath,
		ReadWorkspaceActivity:        opts.ReadWorkspaceActivity,
		CloseWorkspace:               opts.CloseWorkspace,
		Now:                          opts.Now,
		AllowReceiptBoundClosedPurge: true,
	}
	var candidate Candidate
	if legacyShellIsolation != nil {
		candidate = *legacyShellIsolation
	} else if purgingExists && hasIntent && originalPath != purgingPath {
		candidate, err = recheckPurgingIntent(ctx, root, purgingPath, intent, opts)
		if err != nil {
			return skipPurge(result, "interrupted_purge_unsafe", err.Error(), nil), nil
		}
	} else {
		plan, buildErr := BuildPlan(ctx, hygieneOpts)
		if buildErr != nil {
			return PurgeResult{}, buildErr
		}
		found := false
		for _, item := range plan.Candidates {
			if item.Kind == "workspace" && item.WorkspaceID == receipt.WorkspaceID && item.Path == receipt.WorkspacePath {
				candidate = item
				found = true
				break
			}
		}
		if !found {
			return skipPurge(result, "not_in_inventory", "receipt workspace is not present in the managed hygiene inventory", nil), nil
		}
		if !candidate.Safe {
			return skipPurge(result, "unsafe_candidate", candidate.Reason, &candidate), nil
		}
		if !candidate.Legacy || !strings.HasPrefix(filepath.Base(candidate.Path), "closed-") ||
			strings.HasPrefix(filepath.Base(candidate.Path), "closed-recovered-") {
			return skipPurge(result, "unsupported_quarantine", "archive-free purge accepts only an ordinary closed managed quarantine", &candidate), nil
		}
		if candidate.Head != receipt.Head {
			return skipPurge(result, "changed_head", fmt.Sprintf("workspace head changed: got %s want %s", candidate.Head, receipt.Head), &candidate), nil
		}
		if maxBytes > 0 && candidate.BytesKnown && candidate.Bytes > maxBytes {
			return skipPurge(result, "byte_limit", fmt.Sprintf("workspace size %d exceeds per-command limit %d", candidate.Bytes, maxBytes), &candidate), nil
		}
		if err := validateReceiptRecoveryRef(ctx, root, filepath.Join(root, filepath.FromSlash(candidate.Path)), receipt); err != nil {
			return skipPurge(result, "recovery_ref", err.Error(), &candidate), nil
		}
		fresh, safe, recheckErr := recheckWorkspace(ctx, root, hygieneOpts, candidate)
		if recheckErr != nil {
			return skipPurge(result, "recheck_failed", recheckErr.Error(), &candidate), nil
		}
		if !safe {
			return skipPurge(result, "became_unsafe", fresh.Reason, &fresh), nil
		}
		candidate = fresh
		if candidate.Head != receipt.Head {
			return skipPurge(result, "changed_head", "workspace head changed during recheck", &candidate), nil
		}
	}
	result.Candidate = &candidate

	diskReader := opts.ReadDiskUsage
	if diskReader == nil {
		diskReader = readDiskUsage
	}
	before, err := diskReader(root)
	if err != nil || !before.Known {
		reason := "disk usage is unavailable"
		if err != nil {
			reason = err.Error()
		}
		return skipPurge(result, "disk_unknown", reason, &candidate), nil
	}
	result.DiskBefore = before.FreeBytes
	if minFree > 0 && before.FreeBytes < minFree {
		return skipPurge(result, "disk_operation_floor", fmt.Sprintf("free space %d is below the bounded purge operation floor %d", before.FreeBytes, minFree), &candidate), nil
	}

	if !hasIntent {
		status := "isolating"
		if legacyShellIsolation != nil {
			status = "isolated"
		}
		intent = purgeIntent{
			Schema:          purgeIntentSchema,
			ReceiptDigest:   digest,
			WorkspaceID:     receipt.WorkspaceID,
			OriginalPath:    receipt.WorkspacePath,
			PurgingPath:     purgingRel,
			Head:            receipt.Head,
			Branch:          candidate.Branch,
			Target:          candidate.Target,
			RecoveryRef:     receipt.RecoveryRef,
			Bytes:           candidate.Bytes,
			Status:          status,
			StartedAt:       now,
			DiskBeforeBytes: before.FreeBytes,
		}
		if err := writePurgeIntent(intentPath, intent); err != nil {
			return PurgeResult{}, err
		}
	}
	afterIntent, err := diskReader(root)
	if err != nil || !afterIntent.Known || afterIntent.FreeBytes+purgeFreeSlack < before.FreeBytes {
		return skipPurge(result, "intent_not_monotonic", "bounded purge intent write materially reduced available space", &candidate), nil
	}

	if originalPath != purgingPath && originalExists {
		if err := os.Rename(originalPath, purgingPath); err != nil {
			return PurgeResult{}, fmt.Errorf("capsule retention: atomically isolate quarantine: %w", err)
		}
		intent.Status = "isolated"
		if err := writePurgeIntent(intentPath, intent); err != nil {
			return PurgeResult{}, err
		}
		candidate.Path = purgingRel
	}
	if _, err := recheckPurgingIntent(ctx, root, purgingPath, intent, opts); err != nil {
		return skipPurge(result, "isolated_became_unsafe", err.Error(), &candidate), nil
	}
	if opts.CloseWorkspace != nil {
		if err := opts.CloseWorkspace(ctx, root, candidate); err != nil {
			return PurgeResult{}, fmt.Errorf("capsule retention: archive-free remover: %w", err)
		}
	} else if err := os.RemoveAll(purgingPath); err != nil {
		return PurgeResult{}, fmt.Errorf("capsule retention: archive-free remove: %w", err)
	}
	if _, err := os.Lstat(purgingPath); !os.IsNotExist(err) {
		return PurgeResult{}, fmt.Errorf("capsule retention: archive-free remover returned success but quarantine remains")
	}
	after, err := diskReader(root)
	if err != nil || !after.Known {
		return PurgeResult{}, fmt.Errorf("capsule retention: post-purge disk usage unavailable")
	}
	intent.Status = "purged"
	intent.CompletedAt = now
	intent.DiskAfterBytes = after.FreeBytes
	if err := writePurgeIntent(intentPath, intent); err != nil {
		return PurgeResult{}, err
	}
	result.DiskAfter = after.FreeBytes
	if after.FreeBytes+purgeFreeSlack < before.FreeBytes {
		result.OK = false
		result.Status = "purged"
		result.ReasonCode = "post_purge_space_regressed"
		result.Reason = fmt.Sprintf("available space regressed from %d to %d", before.FreeBytes, after.FreeBytes)
		result.Bytes = candidate.Bytes
		return result, nil
	}
	result.OK = true
	result.Status = "purged"
	result.Bytes = candidate.Bytes
	return result, nil
}

// ClearRetainedWorkspaces is the only cleanup-clear deletion path. It reads
// bounded immutable close receipts and delegates every deletion to
// PurgeClosedWorkspace; it never invokes a workspace provider or shell
// teardown, so ignored/review/Git payloads cannot be copied before removal.
func ClearRetainedWorkspaces(ctx context.Context, opts PurgeOptions) (RetentionClearResult, error) {
	root, err := canonicalRoot(opts.ProjectRoot)
	if err != nil {
		return RetentionClearResult{}, err
	}
	result := RetentionClearResult{
		Schema:     RetentionClearSchema,
		Migrations: []RetentionMigration{},
		Purged:     []PurgeResult{},
		Skipped:    []PurgeResult{},
	}
	// Some pre-receipt legacy close operations left a correctly quarantined
	// checkout and its recovery ref, but no close receipt. Migrate only that
	// exact, observable shape. This deliberately happens before listing
	// receipts so a first clear records the new receipt (and its cooling window)
	// rather than deleting a historical payload immediately.
	migrations, err := migrateReceiptlessClosedQuarantines(ctx, root, opts)
	if err != nil {
		return RetentionClearResult{}, err
	}
	result.Migrations = migrations
	dir := filepath.Join(root, ".capsules", "retention", "receipts")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return RetentionClearResult{}, fmt.Errorf("capsule retention: list receipts: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		receipt, readErr := readBoundedRetentionReceipt(path)
		if readErr != nil {
			result.Skipped = append(result.Skipped, PurgeResult{
				Schema:      PurgeReceiptSchema,
				Status:      "skipped",
				ReasonCode:  "invalid_receipt",
				Reason:      readErr.Error(),
				WorkspaceID: strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			})
			continue
		}
		if entry.Name() != receipt.WorkspaceID+".json" {
			result.Skipped = append(result.Skipped, PurgeResult{
				Schema:      PurgeReceiptSchema,
				Status:      "skipped",
				ReasonCode:  "receipt_identity_mismatch",
				Reason:      "receipt filename does not match its immutable workspace identity",
				WorkspaceID: receipt.WorkspaceID,
			})
			continue
		}
		purgeOpts := opts
		purgeOpts.ProjectRoot = root
		purgeOpts.Receipt = receipt
		purged, purgeErr := PurgeClosedWorkspace(ctx, purgeOpts)
		if purgeErr != nil {
			result.Skipped = append(result.Skipped, PurgeResult{
				Schema:        PurgeReceiptSchema,
				Status:        "skipped",
				ReasonCode:    "invalid_or_ineligible_receipt",
				Reason:        purgeErr.Error(),
				WorkspaceID:   receipt.WorkspaceID,
				WorkspacePath: receipt.WorkspacePath,
				Head:          receipt.Head,
				RecoveryRef:   receipt.RecoveryRef,
			})
			continue
		}
		if purged.Status == "purged" {
			result.Purged = append(result.Purged, purged)
		} else {
			result.Skipped = append(result.Skipped, purged)
		}
	}
	return result, nil
}

func migrateReceiptlessClosedQuarantines(ctx context.Context, root string, opts PurgeOptions) ([]RetentionMigration, error) {
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	entries, err := os.ReadDir(workspaceRoot)
	if os.IsNotExist(err) {
		return []RetentionMigration{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("capsule retention: list legacy quarantines: %w", err)
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	minAge := opts.MinAge
	if minAge == 0 {
		minAge = defaultWorkspaceAge
	}
	activityReader := opts.ReadWorkspaceActivity
	if activityReader == nil {
		activityReader = ProbeWorkspaceActivity
	}
	result := []RetentionMigration{}
	for _, entry := range entries {
		id := entry.Name()
		if !strings.HasPrefix(id, "closed-") || strings.HasPrefix(id, "closed-purging-") || strings.HasPrefix(id, "closed-recovered-") {
			continue
		}
		path := filepath.Join(workspaceRoot, id)
		relative := filepath.ToSlash(filepath.Join(".capsules", "workspaces", id))
		record := RetentionMigration{WorkspaceID: id, WorkspacePath: relative, Status: "skipped"}
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			record.ReasonCode = "invalid_quarantine_path"
			record.Reason = "legacy closed quarantine is not a regular directory"
			result = append(result, record)
			continue
		}
		receiptPath := filepath.Join(root, ".capsules", "retention", "receipts", id+".json")
		if _, receiptErr := os.Lstat(receiptPath); receiptErr == nil {
			continue
		} else if !os.IsNotExist(receiptErr) {
			record.ReasonCode = "receipt_inspect_failed"
			record.Reason = receiptErr.Error()
			result = append(result, record)
			continue
		}
		legacy, recognized, legacyErr := readLegacyWorkspaceWithReceiptlessClosedAuthority(ctx, root, path)
		if !recognized || legacyErr != nil {
			record.ReasonCode = "legacy_authority_unproven"
			if legacyErr != nil {
				record.Reason = legacyErr.Error()
			} else {
				record.Reason = "legacy Capsule manifests are missing"
			}
			result = append(result, record)
			continue
		}
		if pathContains(path, opts.CurrentPath) {
			record.ReasonCode = "current_workspace"
			record.Reason = "legacy closed quarantine contains the current process directory"
			result = append(result, record)
			continue
		}
		status, statusErr := gitStatus(ctx, path)
		if statusErr != nil || strings.TrimSpace(status) != "" {
			record.ReasonCode = "workspace_not_clean"
			record.Reason = "legacy closed quarantine is dirty or Git status is unknown"
			result = append(result, record)
			continue
		}
		process, processErr := ProbeWorkspaceProcessCommands(ctx, []string{path})
		activity, activityErr := activityReader(ctx, []string{path})
		if processErr != nil || !process.Known || activityErr != nil || !activity.Known {
			record.ReasonCode = "liveness_unproven"
			record.Reason = "two independent liveness probes are required to migrate a legacy quarantine"
			result = append(result, record)
			continue
		}
		if len(process.PIDsByPath[path]) != 0 || len(activity.PIDsByPath[path]) != 0 {
			record.ReasonCode = "workspace_active"
			record.Reason = "legacy closed quarantine is still named by a running process"
			result = append(result, record)
			continue
		}
		recoveryRef := "refs/kitsoki/workspace-teardown-recovery/" + legacy.Head
		recovery, recoveryErr := gitText(ctx, root, "rev-parse", "--verify", recoveryRef+"^{commit}")
		if recoveryErr != nil || recovery != legacy.Head {
			record.ReasonCode = "recovery_ref_unproven"
			record.Reason = "legacy closed quarantine has no exact durable recovery ref"
			result = append(result, record)
			continue
		}
		stateRoot, stateErr := filepath.EvalSymlinks(filepath.Join(root, ".capsules"))
		if stateErr != nil {
			record.ReasonCode = "state_root_unavailable"
			record.Reason = stateErr.Error()
			result = append(result, record)
			continue
		}
		receipt := RetentionReceipt{
			Schema:           RetentionReceiptSchema,
			Project:          root,
			ProjectStateRoot: stateRoot,
			WorkspaceID:      id,
			WorkspacePath:    relative,
			Head:             legacy.Head,
			RecoveryRef:      recoveryRef,
			IssuedAt:         now,
			EligibleAfter:    now.Add(minAge),
			ProcessSnapshot:  RetentionProbe{Kind: "process-command-snapshot", CapturedAt: now, Safe: true},
			ActivityProbe:    RetentionProbe{Kind: "open-file-scan", CapturedAt: now, Safe: true},
		}
		written, writeErr := WriteRetentionReceipt(root, receipt)
		if writeErr != nil {
			record.ReasonCode = "receipt_write_failed"
			record.Reason = writeErr.Error()
			result = append(result, record)
			continue
		}
		record.Status = "migrated"
		record.ReceiptPath = written
		record.Reason = "legacy closed quarantine now has a bounded receipt; it remains retained through the cooling window"
		result = append(result, record)
	}
	return result, nil
}

func readBoundedRetentionReceipt(path string) (RetentionReceipt, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return RetentionReceipt{}, fmt.Errorf("inspect receipt: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64<<10 {
		return RetentionReceipt{}, fmt.Errorf("receipt is not a bounded regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return RetentionReceipt{}, fmt.Errorf("read receipt: %w", err)
	}
	var receipt RetentionReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return RetentionReceipt{}, fmt.Errorf("parse receipt: %w", err)
	}
	return receipt, nil
}

// discoverLegacyShellIsolation migrates only the exact interrupted shape
// created by the historical dev-workspace --purge-quarantine implementation:
// closed-purging-<receipt workspace id>-<numeric pid>. The immutable receipt,
// recovery ref, clean Git head, merge containment, age, and inactivity must
// all still agree. More than one matching path is ambiguous and fails closed.
func discoverLegacyShellIsolation(
	ctx context.Context,
	root string,
	receipt RetentionReceipt,
	opts PurgeOptions,
	now time.Time,
	minAge time.Duration,
) (Candidate, bool, error) {
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		return Candidate{}, false, fmt.Errorf("list workspace root: %w", err)
	}
	prefix := "closed-purging-" + receipt.WorkspaceID + "-"
	var path string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		suffix := strings.TrimPrefix(entry.Name(), prefix)
		if suffix == "" || strings.Trim(suffix, "0123456789") != "" {
			continue
		}
		if path != "" {
			return Candidate{}, false, fmt.Errorf("more than one legacy shell isolation matches receipt %s", receipt.WorkspaceID)
		}
		path = filepath.Join(workspaceRoot, entry.Name())
	}
	if path == "" {
		return Candidate{}, false, nil
	}
	activity, activityErr := workspaceActivity(ctx, Options{
		ReadWorkspaceActivity: opts.ReadWorkspaceActivity,
	}, []string{path})
	if activityErr != nil {
		activity = WorkspaceActivity{Reason: activityErr.Error()}
	}
	current := opts.CurrentPath
	if current == "" {
		current, _ = os.Getwd()
	}
	candidate, inspectErr := inspectWorkspace(
		ctx,
		root,
		path,
		nil,
		current,
		stringSet(opts.PinnedWorkspaceIDs),
		now,
		normalizeAge(minAge),
		activity,
		false,
		true,
		true,
		nil,
	)
	if inspectErr != nil {
		return Candidate{}, false, inspectErr
	}
	if !candidate.Safe {
		return Candidate{}, false, fmt.Errorf("legacy shell isolation is unsafe: %s", candidate.Reason)
	}
	if candidate.Head != receipt.Head {
		return Candidate{}, false, fmt.Errorf("legacy shell isolation head does not match receipt")
	}
	if err := validateReceiptRecoveryRef(ctx, root, path, receipt); err != nil {
		return Candidate{}, false, err
	}
	return candidate, true, nil
}

func skipPurge(result PurgeResult, code, reason string, candidate *Candidate) PurgeResult {
	result.OK = false
	result.Status = "skipped"
	result.ReasonCode = code
	result.Reason = reason
	result.Candidate = candidate
	return result
}

func retentionReceiptDigest(receipt RetentionReceipt) (string, error) {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return "", fmt.Errorf("capsule retention: encode receipt digest: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func regularDirectoryState(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("capsule retention: inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("capsule retention: managed quarantine path is not a regular directory: %s", path)
	}
	return true, nil
}

func readPurgeIntent(path, digest string, receipt RetentionReceipt) (purgeIntent, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return purgeIntent{}, false, nil
	}
	if err != nil {
		return purgeIntent{}, false, fmt.Errorf("capsule retention: read purge intent: %w", err)
	}
	if len(raw) > 64<<10 {
		return purgeIntent{}, false, fmt.Errorf("capsule retention: purge intent exceeds 64 KiB")
	}
	var intent purgeIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return purgeIntent{}, false, fmt.Errorf("capsule retention: parse purge intent: %w", err)
	}
	if intent.Schema != purgeIntentSchema || intent.ReceiptDigest != digest ||
		intent.WorkspaceID != receipt.WorkspaceID || intent.Head != receipt.Head ||
		intent.RecoveryRef != receipt.RecoveryRef ||
		(intent.Status != "isolating" && intent.Status != "isolated" && intent.Status != "purged") {
		return purgeIntent{}, false, fmt.Errorf("capsule retention: purge intent does not match the exact receipt")
	}
	return intent, true, nil
}

func writePurgeIntent(path string, intent purgeIntent) error {
	raw, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return fmt.Errorf("capsule retention: encode purge intent: %w", err)
	}
	if len(raw) > 64<<10 {
		return fmt.Errorf("capsule retention: purge intent exceeds 64 KiB")
	}
	if err := atomicfile.WriteFile(path, append(raw, '\n'), 0o600, 0o700); err != nil {
		return fmt.Errorf("capsule retention: write purge intent: %w", err)
	}
	return nil
}

func recheckPurgingIntent(ctx context.Context, root, path string, intent purgeIntent, opts PurgeOptions) (Candidate, error) {
	exists, err := regularDirectoryState(path)
	if err != nil || !exists {
		if err != nil {
			return Candidate{}, err
		}
		return Candidate{}, fmt.Errorf("isolated quarantine is absent")
	}
	if pathContains(path, opts.CurrentPath) {
		return Candidate{}, fmt.Errorf("isolated quarantine contains the current process directory")
	}
	for _, pinned := range opts.PinnedWorkspaceIDs {
		if pinned == intent.WorkspaceID || pinned == filepath.Base(path) {
			return Candidate{}, fmt.Errorf("isolated quarantine is pinned")
		}
	}
	status, err := gitStatus(ctx, path)
	if err != nil || strings.TrimSpace(status) != "" {
		return Candidate{}, fmt.Errorf("isolated quarantine is dirty or Git status is unknown")
	}
	head, err := gitText(ctx, path, "rev-parse", "--verify", "HEAD")
	if err != nil || head != intent.Head {
		return Candidate{}, fmt.Errorf("isolated quarantine head changed")
	}
	branch, err := gitText(ctx, path, "branch", "--show-current")
	if err != nil || branch != intent.Branch {
		return Candidate{}, fmt.Errorf("isolated quarantine branch changed")
	}
	contained, err := gitAncestor(ctx, root, intent.Head, "refs/heads/"+intent.Target)
	if err != nil || !contained {
		return Candidate{}, fmt.Errorf("isolated quarantine head is not contained in %s", intent.Target)
	}
	recovery, err := gitText(ctx, root, "rev-parse", "--verify", intent.RecoveryRef+"^{commit}")
	if err != nil || recovery != intent.Head {
		return Candidate{}, fmt.Errorf("isolated quarantine recovery ref is missing or changed")
	}
	reader := opts.ReadWorkspaceActivity
	if reader == nil {
		reader = readWorkspaceActivity
	}
	activity, err := reader(ctx, []string{path})
	if err != nil || !activity.Known {
		return Candidate{}, fmt.Errorf("isolated quarantine activity is unknown")
	}
	if pids := workspaceActivityPIDs(activity, path); len(pids) > 0 {
		return Candidate{}, fmt.Errorf("isolated quarantine is active in process(es): %v", pids)
	}
	return Candidate{
		ID:            "workspace:" + intent.WorkspaceID,
		Kind:          "workspace",
		Path:          filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator))),
		WorkspaceID:   intent.WorkspaceID,
		Status:        "clean",
		Safe:          true,
		Managed:       true,
		Legacy:        true,
		Merged:        true,
		Branch:        intent.Branch,
		Target:        intent.Target,
		Head:          intent.Head,
		Bytes:         intent.Bytes,
		BytesKnown:    true,
		ActivityKnown: true,
		Reason:        "receipt-bound isolated quarantine is safe to resume archive-free purge",
	}, nil
}

func validateRetentionReceipt(root string, receipt RetentionReceipt, now time.Time, minAge time.Duration) error {
	if err := validateRetentionReceiptBinding(root, receipt); err != nil {
		return err
	}
	if receipt.IssuedAt.IsZero() || receipt.EligibleAfter.IsZero() ||
		receipt.ProcessSnapshot.CapturedAt.IsZero() || receipt.ActivityProbe.CapturedAt.IsZero() {
		return fmt.Errorf("capsule retention: receipt timestamps are incomplete")
	}
	if receipt.EligibleAfter.Before(receipt.IssuedAt.Add(minAge)) {
		return fmt.Errorf("capsule retention: receipt eligibility is shorter than minimum age %s", minAge)
	}
	if now.Before(receipt.EligibleAfter) {
		return fmt.Errorf("capsule retention: receipt is too young until %s", receipt.EligibleAfter.UTC().Format(time.RFC3339))
	}
	if receipt.ProcessSnapshot.Kind == "" || receipt.ActivityProbe.Kind == "" ||
		receipt.ProcessSnapshot.Kind == receipt.ActivityProbe.Kind ||
		!receipt.ProcessSnapshot.Safe || !receipt.ActivityProbe.Safe {
		return fmt.Errorf("capsule retention: two distinct safe close-time probes are required")
	}
	if receipt.ProcessSnapshot.CapturedAt.After(receipt.IssuedAt) || receipt.ActivityProbe.CapturedAt.After(receipt.IssuedAt) {
		return fmt.Errorf("capsule retention: probe timestamps cannot postdate receipt issuance")
	}
	if receipt.IssuedAt.Sub(receipt.ProcessSnapshot.CapturedAt) > 5*time.Minute ||
		receipt.IssuedAt.Sub(receipt.ActivityProbe.CapturedAt) > 5*time.Minute {
		return fmt.Errorf("capsule retention: close-time probes are stale")
	}
	return nil
}

func validateRetentionReceiptBinding(root string, receipt RetentionReceipt) error {
	if receipt.Schema != RetentionReceiptSchema {
		return fmt.Errorf("capsule retention: receipt schema %q, want %q", receipt.Schema, RetentionReceiptSchema)
	}
	project, err := canonicalRoot(receipt.Project)
	projectMatches := err == nil && project == root
	if strings.TrimSpace(receipt.ProjectStateRoot) != "" {
		stateRoot, stateErr := canonicalPath(receipt.ProjectStateRoot)
		trustedStateRoot, trustedErr := canonicalPath(filepath.Join(root, ".capsules"))
		if stateErr != nil || trustedErr != nil || stateRoot != trustedStateRoot {
			return fmt.Errorf("capsule retention: receipt project state root does not match the trusted project")
		}
	} else if !projectMatches {
		return fmt.Errorf("capsule retention: receipt project does not match the trusted project root")
	}
	if receipt.WorkspaceID == "" || filepath.Base(receipt.WorkspaceID) != receipt.WorkspaceID || !strings.HasPrefix(receipt.WorkspaceID, "closed-") {
		return fmt.Errorf("capsule retention: receipt workspace id is not a closed quarantine identity")
	}
	wantPath := filepath.ToSlash(filepath.Join(".capsules", "workspaces", receipt.WorkspaceID))
	if receipt.WorkspacePath != wantPath {
		return fmt.Errorf("capsule retention: receipt workspace path %q, want %q", receipt.WorkspacePath, wantPath)
	}
	if !isObjectID(receipt.Head) {
		return fmt.Errorf("capsule retention: receipt head is not an object id")
	}
	if strings.TrimSpace(receipt.RecoveryRef) == "" {
		return fmt.Errorf("capsule retention: recovery ref is required")
	}
	return nil
}

func validateReceiptRecoveryRef(ctx context.Context, root, path string, receipt RetentionReceipt) error {
	if strings.HasPrefix(filepath.Base(path), "closed-recovered-") {
		var marker recoveredQuarantineManifest
		if err := readJSON(filepath.Join(path, ".kitsoki-recovered-quarantine.json"), &marker); err != nil {
			return fmt.Errorf("capsule retention: recovered quarantine manifest: %w", err)
		}
		if marker.RecoveryRef != receipt.RecoveryRef {
			return fmt.Errorf("capsule retention: recovered quarantine ref does not match receipt")
		}
		return nil
	}
	want := "refs/kitsoki/workspace-teardown-recovery/" + receipt.Head
	if receipt.RecoveryRef != want {
		return fmt.Errorf("capsule retention: recovery ref %q, want %q", receipt.RecoveryRef, want)
	}
	head, err := gitText(ctx, root, "rev-parse", "--verify", receipt.RecoveryRef+"^{commit}")
	if err != nil || head != receipt.Head {
		return fmt.Errorf("capsule retention: recovery ref is missing or changed")
	}
	return nil
}
