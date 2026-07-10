package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kitsoki/internal/artifactjob"
)

const RunIndexSchema = "capsule-ci-run-index/v1"

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

type RunIndex struct {
	Schema string          `json:"schema"`
	Runs   []RunProjection `json:"runs"`
}

type RunProjection struct {
	JobID               string             `json:"job_id"`
	Status              artifactjob.Status `json:"status"`
	Story               string             `json:"story,omitempty"`
	Workspace           string             `json:"workspace,omitempty"`
	Pipeline            string             `json:"pipeline,omitempty"`
	Outcome             string             `json:"outcome,omitempty"`
	PromotionEligible   bool               `json:"promotion_eligible"`
	ReceiptID           string             `json:"receipt_id,omitempty"`
	ReceiptVerification string             `json:"receipt_verification,omitempty"`
	EnvelopeDigest      string             `json:"envelope_digest,omitempty"`
	SourceDigest        string             `json:"source_digest,omitempty"`
	StoryDigest         string             `json:"story_digest,omitempty"`
	EnvironmentDigest   string             `json:"environment_digest,omitempty"`
	TracePath           string             `json:"trace_path,omitempty"`
	ReceiptPath         string             `json:"receipt_path,omitempty"`
	UpdatedAt           time.Time          `json:"updated_at,omitempty"`
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

func (s FileRunStore) Index() (RunIndex, error) {
	records, err := s.List()
	if err != nil {
		return RunIndex{}, err
	}
	out := RunIndex{Schema: RunIndexSchema, Runs: []RunProjection{}}
	for _, record := range records {
		out.Runs = append(out.Runs, s.Project(record))
	}
	return out, nil
}

func (s FileRunStore) Project(record RunRecord) RunProjection {
	job := record.Result.Job
	verdict := record.Result.Verdict
	envDigest := record.Result.Envelope.Environment.Digest
	projection := RunProjection{
		JobID:               record.JobID,
		Status:              job.Status,
		Story:               job.Story,
		Workspace:           string(job.WorkspaceInstanceID),
		Pipeline:            verdict.Pipeline,
		Outcome:             verdict.Outcome,
		PromotionEligible:   verdict.PromotionEligible,
		ReceiptID:           record.ReceiptID,
		ReceiptVerification: record.ReceiptVerification,
		EnvelopeDigest:      record.Result.Envelope.Digest,
		SourceDigest:        verdict.SourceDigest,
		StoryDigest:         verdict.StoryDigest,
		EnvironmentDigest:   envDigest,
		UpdatedAt:           job.UpdatedAt,
	}
	if projection.SourceDigest == "" {
		projection.SourceDigest = record.Result.Envelope.SourceDigest
	}
	if projection.StoryDigest == "" {
		projection.StoryDigest = record.Result.Envelope.StoryDigest
	}
	if projection.EnvironmentDigest == "" {
		projection.EnvironmentDigest = envDigest
	}
	if record.JobID != "" {
		if rel := s.rel(filepath.Join(s.ProjectRoot, ".capsules", "ci", record.JobID+".trace.json")); rel != "" {
			projection.TracePath = rel
		}
		if rel := s.rel(filepath.Join(s.ProjectRoot, ".capsules", "ci", record.JobID+".receipt.json")); rel != "" {
			projection.ReceiptPath = rel
		}
	}
	return projection
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

func (s FileRunStore) rel(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	rel, err := filepath.Rel(s.ProjectRoot, path)
	if err != nil || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}
