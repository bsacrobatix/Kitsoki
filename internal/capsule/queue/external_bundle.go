package queue

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kitsoki/internal/atomicfile"
)

const (
	ExternalWorkerResultSchema = "capsule-external-worker-result/v1"
	ExternalBundleAnchorSchema = "capsule-queue-external-bundle-anchor/v1"
	DefaultMaxExternalBundle   = int64(512 << 20)
)

var externalDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ExternalWorkerResult is the strict, transport-independent identity emitted
// by a disposable worker. BundleKey is an opaque object-store locator for
// audit only; callers download it and supply BundlePath separately so no
// credentials or remote-listing behavior enter queue state.
type ExternalWorkerResult struct {
	Schema         string `json:"schema"`
	ExecutionID    string `json:"execution_id"`
	JobID          string `json:"job_id"`
	TrainID        string `json:"train_id"`
	ManifestDigest string `json:"manifest_digest"`
	Branch         string `json:"branch"`
	CandidateSHA   string `json:"candidate_sha"`
	BaseSHA        string `json:"base_sha"`
	TargetRef      string `json:"target_ref"`
	ReceiptID      string `json:"receipt_id"`
	BundleDigest   string `json:"bundle_digest"`
	BundleBytes    int64  `json:"bundle_bytes"`
	BundleKey      string `json:"bundle_key,omitempty"`
}

// ExternalBundleAnchor is immutable proof that one exact worker result was
// imported into the queue-owned object repository only after full verification.
type ExternalBundleAnchor struct {
	Schema           string               `json:"schema"`
	ID               string               `json:"id"`
	Result           ExternalWorkerResult `json:"result"`
	ObjectRepository string               `json:"object_repository"`
	Ref              string               `json:"ref"`
	CreatedAt        time.Time            `json:"created_at"`
}

// ExternalBundleSubmission pairs an already-downloaded local bundle with the
// ordinary receipt-bound queue submission. BundlePath is deliberately never
// persisted.
type ExternalBundleSubmission struct {
	Result     ExternalWorkerResult
	BundlePath string
	Submit     Submit
}

// ExternalBundleAdmitter exposes a narrow interruption seam for deterministic
// crash-recovery tests. Production callers use Store.AdmitExternalBundle.
type ExternalBundleAdmitter struct {
	Store          Store
	MaxBundleBytes int64
	Interrupt      func(stage string) error
}

func (s Store) AdmitExternalBundle(ctx context.Context, in ExternalBundleSubmission) (Candidate, ExternalBundleAnchor, error) {
	return (ExternalBundleAdmitter{Store: s}).Admit(ctx, in)
}

func (a ExternalBundleAdmitter) Admit(ctx context.Context, in ExternalBundleSubmission) (Candidate, ExternalBundleAnchor, error) {
	if err := validateExternalSubmission(in, a.maxBundleBytes()); err != nil {
		return Candidate{}, ExternalBundleAnchor{}, err
	}
	root, err := filepath.Abs(a.Store.ProjectRoot)
	if err != nil {
		return Candidate{}, ExternalBundleAnchor{}, err
	}
	queueDir := filepath.Join(root, ".capsules", "queue")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		return Candidate{}, ExternalBundleAnchor{}, err
	}
	unlock, err := lock(filepath.Join(queueDir, "external-objects.lock"), a.Store.LockWait)
	if err != nil {
		return Candidate{}, ExternalBundleAnchor{}, err
	}
	anchor, err := a.importLocked(ctx, root, queueDir, in)
	unlock()
	if err != nil {
		return Candidate{}, ExternalBundleAnchor{}, err
	}

	submit := in.Submit
	submit.SourceAnchorID = anchor.ID
	candidate, err := a.Store.Submit(submit)
	if err != nil {
		return Candidate{}, anchor, err
	}
	return candidate, anchor, nil
}

func (a ExternalBundleAdmitter) importLocked(ctx context.Context, root, queueDir string, in ExternalBundleSubmission) (ExternalBundleAnchor, error) {
	id, err := externalAnchorID(in.Result)
	if err != nil {
		return ExternalBundleAnchor{}, err
	}
	anchorPath := filepath.Join(queueDir, "external-anchors", id+".json")
	if existing, found, err := readExternalBundleAnchor(anchorPath); err != nil {
		return ExternalBundleAnchor{}, err
	} else if found {
		if existing.Result != in.Result {
			return ExternalBundleAnchor{}, fmt.Errorf("queue: external bundle anchor %s result mismatch", id)
		}
		if err := verifyExternalAnchor(ctx, root, existing); err != nil {
			return ExternalBundleAnchor{}, err
		}
		return existing, nil
	}

	repository := filepath.Join(queueDir, "external-objects.git")
	if err := ensureExternalObjectRepository(ctx, repository); err != nil {
		return ExternalBundleAnchor{}, err
	}
	staged, err := copyVerifiedExternalBundle(in.BundlePath, queueDir, in.Result, a.maxBundleBytes())
	if err != nil {
		return ExternalBundleAnchor{}, err
	}
	defer os.Remove(staged)
	if _, err := gitOutput(ctx, repository, "bundle", "verify", staged); err != nil {
		return ExternalBundleAnchor{}, fmt.Errorf("queue: verify external git bundle: %w", err)
	}
	if err := verifyExternalBundleHeads(ctx, repository, staged, in.Result.CandidateSHA); err != nil {
		return ExternalBundleAnchor{}, err
	}

	tmpRef := "refs/kitsoki/external-imports/" + id
	finalRef := "refs/kitsoki/external-results/" + id
	defer func() { _, _ = gitOutput(context.Background(), repository, "update-ref", "-d", tmpRef) }()
	refspec := in.Result.CandidateSHA + ":" + tmpRef
	if _, err := gitOutput(ctx, repository, "fetch", "--no-tags", "--no-write-fetch-head", staged, refspec); err != nil {
		return ExternalBundleAnchor{}, fmt.Errorf("queue: import verified external bundle: %w", err)
	}
	if err := verifyExternalLineage(ctx, repository, tmpRef, in.Result); err != nil {
		return ExternalBundleAnchor{}, err
	}
	if err := a.interrupt("after-import"); err != nil {
		return ExternalBundleAnchor{}, err
	}

	if current, refErr := gitOutput(ctx, repository, "rev-parse", "--verify", finalRef+"^{commit}"); refErr == nil {
		if strings.TrimSpace(current) != in.Result.CandidateSHA {
			return ExternalBundleAnchor{}, fmt.Errorf("queue: immutable external result ref %s changed", finalRef)
		}
	} else {
		zero := strings.Repeat("0", 40)
		if _, err := gitOutput(ctx, repository, "update-ref", finalRef, in.Result.CandidateSHA, zero); err != nil {
			return ExternalBundleAnchor{}, fmt.Errorf("queue: publish immutable external result ref: %w", err)
		}
	}
	if err := a.interrupt("after-ref"); err != nil {
		return ExternalBundleAnchor{}, err
	}

	anchor := ExternalBundleAnchor{
		Schema:           ExternalBundleAnchorSchema,
		ID:               id,
		Result:           in.Result,
		ObjectRepository: filepath.ToSlash(filepath.Join(".capsules", "queue", "external-objects.git")),
		Ref:              finalRef,
		CreatedAt:        time.Now().UTC(),
	}
	raw, err := json.MarshalIndent(anchor, "", "  ")
	if err != nil {
		return ExternalBundleAnchor{}, err
	}
	if err := atomicfile.WriteFile(anchorPath, append(raw, '\n'), 0o600, 0o700); err != nil {
		return ExternalBundleAnchor{}, fmt.Errorf("queue: persist external bundle anchor: %w", err)
	}
	return anchor, nil
}

func (a ExternalBundleAdmitter) maxBundleBytes() int64 {
	if a.MaxBundleBytes > 0 && a.MaxBundleBytes <= DefaultMaxExternalBundle {
		return a.MaxBundleBytes
	}
	return DefaultMaxExternalBundle
}

func (a ExternalBundleAdmitter) interrupt(stage string) error {
	if a.Interrupt == nil {
		return nil
	}
	return a.Interrupt(stage)
}

func validateExternalSubmission(in ExternalBundleSubmission, maxBytes int64) error {
	r := in.Result
	if err := validateExternalWorkerResult(r, maxBytes); err != nil {
		return err
	}
	if strings.TrimSpace(in.BundlePath) == "" {
		return fmt.Errorf("queue: downloaded external bundle path is required")
	}
	if r.Branch != in.Submit.Branch || r.CandidateSHA != in.Submit.SHA ||
		r.TargetRef != in.Submit.targetRef() || r.ReceiptID != in.Submit.Receipt.ReceiptID ||
		r.JobID != in.Submit.Receipt.JobID ||
		r.ManifestDigest != strings.TrimSpace(in.Submit.ManifestDigest) {
		return fmt.Errorf("queue: external worker result does not match receipt-bound queue submission")
	}
	return validate(in.Submit)
}

func validateExternalWorkerResult(r ExternalWorkerResult, maxBytes int64) error {
	if r.Schema != ExternalWorkerResultSchema {
		return fmt.Errorf("queue: unsupported external worker result schema %q", r.Schema)
	}
	for name, value := range map[string]string{
		"execution_id": r.ExecutionID, "job_id": r.JobID, "train_id": r.TrainID,
		"manifest_digest": r.ManifestDigest, "branch": r.Branch,
		"target_ref": r.TargetRef, "receipt_id": r.ReceiptID,
	} {
		if !validExternalText(value) {
			return fmt.Errorf("queue: external worker result %s is required and must be a bounded single-line value", name)
		}
	}
	if !validFullSHA(r.CandidateSHA) || !validFullSHA(r.BaseSHA) || r.CandidateSHA == r.BaseSHA {
		return fmt.Errorf("queue: external worker result requires distinct lowercase full candidate and base SHAs")
	}
	if !externalDigestPattern.MatchString(r.BundleDigest) {
		return fmt.Errorf("queue: external worker result bundle_digest must be sha256")
	}
	if !externalDigestPattern.MatchString(r.ManifestDigest) {
		return fmt.Errorf("queue: external worker result manifest_digest must be sha256")
	}
	if r.BundleBytes <= 0 || r.BundleBytes > maxBytes {
		return fmt.Errorf("queue: external worker result bundle_bytes %d exceeds allowed maximum %d", r.BundleBytes, maxBytes)
	}
	if r.BundleKey != "" && (!validExternalText(r.BundleKey) || filepath.IsAbs(r.BundleKey) ||
		strings.Contains(r.BundleKey, `\`) || strings.Contains(r.BundleKey, "://") ||
		strings.ContainsAny(r.BundleKey, "?#") || hasTraversal(r.BundleKey)) {
		return fmt.Errorf("queue: external worker result bundle_key is invalid")
	}
	return nil
}

func validExternalText(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 512 && !strings.ContainsAny(value, "\x00\n\r")
}

func validFullSHA(value string) bool {
	return len(value) == 40 && strings.Trim(value, "0123456789abcdef") == ""
}

func hasTraversal(value string) bool {
	for _, part := range strings.Split(filepath.ToSlash(value), "/") {
		if part == ".." || part == "." || part == "" {
			return true
		}
	}
	return false
}

func externalAnchorID(result ExternalWorkerResult) (string, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "external-" + hex.EncodeToString(sum[:16]), nil
}

func ensureExternalObjectRepository(ctx context.Context, repository string) error {
	if info, err := os.Lstat(repository); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("queue: external object repository must be a private directory")
		}
		if err := os.Chmod(repository, 0o700); err != nil {
			return fmt.Errorf("queue: make external object repository private: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(filepath.Join(repository, "HEAD")); err == nil {
		if _, err := gitOutput(ctx, repository, "rev-parse", "--is-bare-repository"); err != nil {
			return fmt.Errorf("queue: verify external object repository: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(repository), 0o700); err != nil {
		return err
	}
	if _, err := gitOutput(ctx, filepath.Dir(repository), "init", "--bare", repository); err != nil {
		return fmt.Errorf("queue: initialize external object repository: %w", err)
	}
	if err := os.Chmod(repository, 0o700); err != nil {
		return fmt.Errorf("queue: make external object repository private: %w", err)
	}
	return nil
}

func copyVerifiedExternalBundle(source, queueDir string, result ExternalWorkerResult, maxBytes int64) (string, error) {
	linkInfo, err := os.Lstat(source)
	if err != nil {
		return "", fmt.Errorf("queue: inspect external bundle: %w", err)
	}
	if !linkInfo.Mode().IsRegular() || linkInfo.Mode()&os.ModeSymlink != 0 || linkInfo.Size() != result.BundleBytes || linkInfo.Size() > maxBytes {
		return "", fmt.Errorf("queue: external bundle must be a regular non-symlink file with the declared bounded size")
	}
	input, err := os.Open(source)
	if err != nil {
		return "", fmt.Errorf("queue: open external bundle: %w", err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(linkInfo, info) {
		return "", fmt.Errorf("queue: external bundle changed identity while opening")
	}
	temp, err := os.CreateTemp(queueDir, ".external-import-*.bundle")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	ok := false
	defer func() {
		_ = temp.Close()
		if !ok {
			_ = os.Remove(tempPath)
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(input, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("queue: copy external bundle: %w", err)
	}
	if n != result.BundleBytes {
		return "", fmt.Errorf("queue: external bundle changed while reading")
	}
	if got := "sha256:" + hex.EncodeToString(hash.Sum(nil)); got != result.BundleDigest {
		return "", fmt.Errorf("queue: external bundle digest mismatch")
	}
	if err := temp.Sync(); err != nil {
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return "", err
	}
	ok = true
	return tempPath, nil
}

func verifyExternalBundleHeads(ctx context.Context, repository, bundlePath, candidateSHA string) error {
	out, err := gitOutput(ctx, repository, "bundle", "list-heads", bundlePath)
	if err != nil {
		return fmt.Errorf("queue: list external bundle heads: %w", err)
	}
	var heads []string
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return fmt.Errorf("queue: malformed external bundle head")
		}
		heads = append(heads, fields[0])
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(heads) != 1 || heads[0] != candidateSHA {
		return fmt.Errorf("queue: external bundle advertises %d heads; exact candidate head is required", len(heads))
	}
	return nil
}

func verifyExternalLineage(ctx context.Context, repository, candidateRef string, result ExternalWorkerResult) error {
	candidate, err := gitOutput(ctx, repository, "rev-parse", "--verify", candidateRef+"^{commit}")
	if err != nil || strings.TrimSpace(candidate) != result.CandidateSHA {
		return fmt.Errorf("queue: imported external candidate head mismatch")
	}
	if _, err := gitOutput(ctx, repository, "cat-file", "-e", result.BaseSHA+"^{commit}"); err != nil {
		return fmt.Errorf("queue: external bundle is missing declared base commit: %w", err)
	}
	if _, err := gitOutput(ctx, repository, "merge-base", "--is-ancestor", result.BaseSHA, result.CandidateSHA); err != nil {
		return fmt.Errorf("queue: external candidate is not descended from declared base: %w", err)
	}
	return nil
}

func readExternalBundleAnchor(path string) (ExternalBundleAnchor, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return ExternalBundleAnchor{}, false, nil
	}
	if err != nil {
		return ExternalBundleAnchor{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 64<<10 {
		return ExternalBundleAnchor{}, false, fmt.Errorf("queue: external bundle anchor must be a bounded regular non-symlink file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ExternalBundleAnchor{}, false, err
	}
	var anchor ExternalBundleAnchor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&anchor); err != nil {
		return ExternalBundleAnchor{}, false, fmt.Errorf("queue: parse external bundle anchor: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ExternalBundleAnchor{}, false, fmt.Errorf("queue: parse external bundle anchor: trailing JSON value")
	}
	if anchor.Schema != ExternalBundleAnchorSchema || anchor.ID == "" || anchor.Ref == "" {
		return ExternalBundleAnchor{}, false, fmt.Errorf("queue: invalid external bundle anchor")
	}
	if err := validateExternalWorkerResult(anchor.Result, DefaultMaxExternalBundle); err != nil {
		return ExternalBundleAnchor{}, false, fmt.Errorf("queue: invalid external bundle anchor result: %w", err)
	}
	expectedID, err := externalAnchorID(anchor.Result)
	if err != nil || expectedID != anchor.ID {
		return ExternalBundleAnchor{}, false, fmt.Errorf("queue: external bundle anchor identity does not match its result")
	}
	if anchor.Ref != "refs/kitsoki/external-results/"+anchor.ID || anchor.CreatedAt.IsZero() {
		return ExternalBundleAnchor{}, false, fmt.Errorf("queue: invalid external bundle anchor authority")
	}
	return anchor, true, nil
}

func (s Store) externalAnchor(ctx context.Context, id string) (ExternalBundleAnchor, error) {
	if !strings.HasPrefix(id, "external-") || len(id) != len("external-")+32 || strings.Trim(id[len("external-"):], "0123456789abcdef") != "" {
		return ExternalBundleAnchor{}, fmt.Errorf("queue: invalid external bundle anchor id")
	}
	root, err := filepath.Abs(s.ProjectRoot)
	if err != nil {
		return ExternalBundleAnchor{}, err
	}
	path := filepath.Join(root, ".capsules", "queue", "external-anchors", id+".json")
	anchor, found, err := readExternalBundleAnchor(path)
	if err != nil {
		return ExternalBundleAnchor{}, err
	}
	if !found || anchor.ID != id {
		return ExternalBundleAnchor{}, fmt.Errorf("queue: external bundle anchor %s is unavailable", id)
	}
	if err := verifyExternalAnchor(ctx, root, anchor); err != nil {
		return ExternalBundleAnchor{}, err
	}
	return anchor, nil
}

func verifyExternalAnchor(ctx context.Context, root string, anchor ExternalBundleAnchor) error {
	repository := filepath.Join(root, filepath.FromSlash(anchor.ObjectRepository))
	wantRepository := filepath.Join(root, ".capsules", "queue", "external-objects.git")
	if filepath.Clean(repository) != filepath.Clean(wantRepository) {
		return fmt.Errorf("queue: external bundle anchor object repository escapes queue authority")
	}
	got, err := gitOutput(ctx, repository, "rev-parse", "--verify", anchor.Ref+"^{commit}")
	if err != nil || strings.TrimSpace(got) != anchor.Result.CandidateSHA {
		return fmt.Errorf("queue: external bundle anchor ref does not resolve to the exact candidate")
	}
	return verifyExternalLineage(ctx, repository, anchor.Ref, anchor.Result)
}

func (s Store) materializeExternalCandidate(ctx context.Context, workspace string, candidate Candidate) error {
	if candidate.SourceAnchorID == "" {
		return nil
	}
	anchor, err := s.externalAnchor(ctx, candidate.SourceAnchorID)
	if err != nil {
		return err
	}
	repository := filepath.Join(mustAbs(s.ProjectRoot), filepath.FromSlash(anchor.ObjectRepository))
	if _, err := gitOutput(ctx, workspace, "fetch", "--no-tags", "--no-write-fetch-head", repository, anchor.Ref); err != nil {
		return fmt.Errorf("queue: materialize external candidate in managed workspace: %w", err)
	}
	if _, err := gitOutput(ctx, workspace, "cat-file", "-e", candidate.SHA+"^{commit}"); err != nil {
		return fmt.Errorf("queue: materialized external candidate is unavailable: %w", err)
	}
	return nil
}
