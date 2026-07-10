package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kitsoki/internal/artifactjob"
)

// RunRecord is the durable, project-local projection used by `capsule ci
// status`. Artifact-job remains the durable run identity during a live process;
// this record makes the completed envelope/verdict recoverable without a new
// registry schema.
type RunRecord struct {
	JobID               string    `json:"job_id"`
	Result              RunResult `json:"result"`
	ReceiptID           string    `json:"receipt_id,omitempty"`
	ReceiptVerification string    `json:"receipt_verification,omitempty"`
}
type FileRunStore struct{ ProjectRoot string }

func (s FileRunStore) Write(record RunRecord) error {
	if record.JobID == "" {
		return fmt.Errorf("capsule ci: job id is required")
	}
	dir := filepath.Join(s.ProjectRoot, ".capsules", "ci")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, record.JobID+".run.json"), append(raw, '\n'), 0o600)
}
func (s FileRunStore) Get(id string) (RunRecord, error) {
	raw, err := os.ReadFile(filepath.Join(s.ProjectRoot, ".capsules", "ci", id+".run.json"))
	if os.IsNotExist(err) {
		raw, err = os.ReadFile(filepath.Join(s.ProjectRoot, ".capsules", "ci", id+".json"))
	}
	if err != nil {
		return RunRecord{}, err
	}
	var record RunRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return RunRecord{}, err
	}
	return record, nil
}
func (s FileRunStore) List() ([]RunRecord, error) {
	entries, err := os.ReadDir(filepath.Join(s.ProjectRoot, ".capsules", "ci"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []RunRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || strings.HasSuffix(entry.Name(), ".receipt.json") || strings.HasSuffix(entry.Name(), ".trace.json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".run.json")
		id = strings.TrimSuffix(id, ".json")
		record, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		if string(record.Result.Job.ID) != record.JobID {
			continue
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JobID > out[j].JobID })
	return out, nil
}

// Cancel records a visible terminal cancel for a parked/running CI job. Active
// remote providers receive the same request through ExecutorProvider.Cancel;
// this local store path makes cancellation durable even when no worker remains.
func (s FileRunStore) Cancel(id string) (RunRecord, error) {
	record, err := s.Get(id)
	if err != nil {
		return RunRecord{}, err
	}
	switch record.Result.Job.Status {
	case artifactjob.StatusRunning, artifactjob.StatusAwaitingInput, artifactjob.StatusInterrupted:
	default:
		return RunRecord{}, fmt.Errorf("capsule ci: job %s cannot be cancelled from %s", id, record.Result.Job.Status)
	}
	record.Result.Job.Status = artifactjob.StatusCancelled
	record.Result.Verdict.Outcome = "cancelled"
	record.Result.Verdict.PromotionEligible = false
	if err := s.Write(record); err != nil {
		return RunRecord{}, err
	}
	return record, nil
}
