package record

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/receipt"
	capsuletrace "kitsoki/internal/capsule/trace"
)

type Stored struct {
	Receipt      receipt.Receipt      `json:"receipt"`
	Verification receipt.Verification `json:"verification"`
	TracePath    string               `json:"trace_path"`
	ReceiptPath  string               `json:"receipt_path"`
}
type PersistOptions struct {
	Signer receipt.Signer
}

func Persist(project string, result ci.RunResult) (Stored, error) {
	return PersistWithOptions(project, result, PersistOptions{})
}

func PersistWithOptions(project string, result ci.RunResult, opts PersistOptions) (Stored, error) {
	if result.Job.ID == "" {
		return Stored{}, fmt.Errorf("capsule record: job id is required")
	}
	dir := filepath.Join(project, ".capsules", "ci")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Stored{}, err
	}
	trace := capsuletrace.NewDocument(
		capsuletrace.Event{Kind: capsuletrace.KindCIStarted, JobID: string(result.Job.ID), EnvelopeDigest: result.Envelope.Digest},
		capsuletrace.Event{Kind: capsuletrace.KindCIVerdict, JobID: string(result.Job.ID), EnvelopeDigest: result.Envelope.Digest, Outcome: result.Verdict.Outcome},
	)
	raw, err := capsuletrace.MarshalDocument(trace)
	if err != nil {
		return Stored{}, err
	}
	tracePath := filepath.Join(dir, string(result.Job.ID)+".trace.json")
	if err := os.WriteFile(tracePath, append(raw, '\n'), 0o600); err != nil {
		return Stored{}, err
	}
	sum := sha256.Sum256(raw)
	built, verification, err := receipt.Build(receipt.BuildInput{Job: result.Job, Envelope: result.Envelope, Verdict: result.Verdict, Artifacts: []receipt.Artifact{{Handle: result.Execution.VerdictArtifact, Kind: "verdict"}}, TraceDigest: "sha256:" + hex.EncodeToString(sum[:])})
	if err != nil {
		return Stored{}, err
	}
	built, err = signByPolicy(project, built, opts.Signer)
	if err != nil {
		return Stored{}, err
	}
	policy, err := receiptPolicy(project)
	if err != nil {
		return Stored{}, err
	}
	verification = receipt.Verify(built, opts.Signer, policy.RequireSignature)
	if verification.Status != "valid" {
		return Stored{}, fmt.Errorf("capsule record: receipt %s", verification.Status)
	}
	receiptPath := filepath.Join(dir, string(result.Job.ID)+".receipt.json")
	encoded, err := json.MarshalIndent(built, "", "  ")
	if err != nil {
		return Stored{}, err
	}
	if err := os.WriteFile(receiptPath, append(encoded, '\n'), 0o600); err != nil {
		return Stored{}, err
	}
	return Stored{Receipt: built, Verification: verification, TracePath: tracePath, ReceiptPath: receiptPath}, nil
}

func signByPolicy(project string, r receipt.Receipt, signer receipt.Signer) (receipt.Receipt, error) {
	policy, err := receiptPolicy(project)
	if err != nil {
		return r, err
	}
	if !policy.RequireSignature {
		return r, nil
	}
	if signer == nil {
		return r, fmt.Errorf("capsule record: receipt signature required by project policy")
	}
	if policy.Signer != "" && signer.Name() != policy.Signer {
		return r, fmt.Errorf("capsule record: signer %q does not satisfy project policy %q", signer.Name(), policy.Signer)
	}
	return receipt.Sign(r, signer)
}

func receiptPolicy(project string) (ci.ReceiptPolicy, error) {
	cfg, err := ci.Load(project)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ci.ReceiptPolicy{}, nil
		}
		return ci.ReceiptPolicy{}, err
	}
	return cfg.Receipt, nil
}
