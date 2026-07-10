package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RunRecord is the durable, project-local projection used by `capsule ci
// status`. Artifact-job remains the durable run identity during a live process;
// this record makes the completed envelope/verdict recoverable without a new
// registry schema.
type RunRecord struct {
	JobID  string    `json:"job_id"`
	Result RunResult `json:"result"`
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
	return os.WriteFile(filepath.Join(dir, record.JobID+".json"), append(raw, '\n'), 0o600)
}
func (s FileRunStore) Get(id string) (RunRecord, error) {
	raw, err := os.ReadFile(filepath.Join(s.ProjectRoot, ".capsules", "ci", id+".json"))
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
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := s.Get(entry.Name()[:len(entry.Name())-5])
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JobID > out[j].JobID })
	return out, nil
}
