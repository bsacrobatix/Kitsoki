package admissionserver

import (
	"bytes"
	"encoding/json"
	"fmt"

	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/receipt"
)

// SealHandoff builds the exact, canonical handoff accepted by the admission
// service.  It is deliberately shared by the native Capsule-promotion client
// and worker-owned integrations so their object and replay identities cannot
// drift.
func SealHandoff(result queue.ExternalWorkerResult, receiptRaw []byte) (Handoff, error) {
	parsed, err := parseReceipt(receiptRaw)
	if err != nil {
		return Handoff{}, err
	}
	if verification := receipt.Verify(parsed, nil, false); verification.Status != "valid" || !verification.PromotionEligible {
		return Handoff{}, fmt.Errorf("queue admission: receipt is not valid and promotion eligible")
	}
	if parsed.ReceiptID != result.ReceiptID || parsed.JobID != result.JobID ||
		parsed.Envelope.SourceDigest != result.CandidateSHA || parsed.Verdict.SourceDigest != result.CandidateSHA {
		return Handoff{}, fmt.Errorf("queue admission: receipt does not bind the exact external result")
	}
	resultRaw, err := canonicalJSON(result)
	if err != nil {
		return Handoff{}, err
	}
	handoff := Handoff{
		Schema:       HandoffSchema,
		HandoffKey:   "runs/" + result.ExecutionID + "/artifacts/integration-train-external-admission-handoff.json",
		Result:       result,
		ResultDigest: digestBytes(resultRaw),
		Receipt: HandoffReceipt{
			ReceiptID: parsed.ReceiptID, JobID: parsed.JobID, SourceSHA: result.CandidateSHA,
			ContentDigest: parsed.Integrity.ContentDigest, RawDigest: digestBytes(receiptRaw),
		},
		Bundle: HandoffBundle{Key: result.BundleKey, Digest: result.BundleDigest, Bytes: result.BundleBytes, Head: result.CandidateSHA, BaseSHA: result.BaseSHA},
	}
	unsigned := handoff
	unsigned.HandoffDigest = ""
	raw, err := json.Marshal(unsigned)
	if err != nil {
		return Handoff{}, err
	}
	var generic map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&generic); err != nil {
		return Handoff{}, err
	}
	delete(generic, "handoff_digest")
	canonical, err := canonicalJSON(generic)
	if err != nil {
		return Handoff{}, err
	}
	handoff.HandoffDigest = digestBytes(canonical)
	return handoff, nil
}
