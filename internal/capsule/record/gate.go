package record

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/receipt"
	"kitsoki/internal/capsule/reconcile"
)

// PromotionGate verifies that a persisted local receipt is intact, promotion
// eligible, and was produced for the exact reconciliation candidate.
// Unsigned local receipts are accepted by default; a project policy can supply
// a signer and require its signature without changing the reconciliation API.
type PromotionGate struct {
	ProjectRoot      string
	Signer           receipt.Signer
	RequireSignature bool
}

func (g PromotionGate) Verify(_ context.Context, receiptID string, plan reconcile.Plan) error {
	if receiptID == "" {
		return fmt.Errorf("capsule sync: required gate receipt is missing")
	}
	r, err := g.receipt(receiptID)
	if err != nil {
		return err
	}
	verified := receipt.Verify(r, g.Signer, g.RequireSignature)
	if verified.Status != "valid" || !verified.PromotionEligible {
		return fmt.Errorf("capsule sync: gate receipt is not promotion eligible")
	}
	if r.Envelope.SourceDigest != plan.Candidate {
		return fmt.Errorf("capsule sync: gate receipt candidate does not match reconciliation plan")
	}
	if r.JobID == "" || filepath.Base(r.JobID) != r.JobID {
		return fmt.Errorf("capsule sync: gate receipt has an invalid job id")
	}
	record, err := (ci.FileRunStore{ProjectRoot: g.ProjectRoot}).Get(r.JobID)
	if err != nil {
		return fmt.Errorf("capsule sync: gate receipt run record: %w", err)
	}
	if record.JobID != r.JobID || record.ReceiptID != receiptID || record.ReceiptVerification != "valid" {
		return fmt.Errorf("capsule sync: gate receipt is not the verified result for its run")
	}
	return nil
}

func (g PromotionGate) receipt(id string) (receipt.Receipt, error) {
	dir := filepath.Join(g.ProjectRoot, ".capsules", "ci")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return receipt.Receipt{}, fmt.Errorf("capsule sync: read gate receipts: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !hasReceiptSuffix(entry.Name()) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return receipt.Receipt{}, err
		}
		var r receipt.Receipt
		if err := json.Unmarshal(raw, &r); err != nil {
			return receipt.Receipt{}, fmt.Errorf("capsule sync: parse gate receipt: %w", err)
		}
		if r.ReceiptID == id {
			return r, nil
		}
	}
	return receipt.Receipt{}, fmt.Errorf("capsule sync: gate receipt %q not found", id)
}

func hasReceiptSuffix(name string) bool {
	return len(name) > len(".receipt.json") && name[len(name)-len(".receipt.json"):] == ".receipt.json"
}

var _ reconcile.GateVerifier = PromotionGate{}
