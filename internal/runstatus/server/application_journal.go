package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	appplatform "kitsoki/internal/application"
)

type applicationJournalLine struct {
	Kind    string                       `json:"kind"`
	Receipt *appplatform.Receipt         `json:"receipt,omitempty"`
	Key     *appplatform.ReplayKey       `json:"key,omitempty"`
	Outcome *appplatform.OutcomeEnvelope `json:"outcome,omitempty"`
	Child   *appplatform.ChildRun        `json:"child,omitempty"`
	Join    *appplatform.JoinState       `json:"join,omitempty"`
	Error   string                       `json:"error,omitempty"`
}

// applicationJournal is both the durable receipt sink and replay index for one
// session. It uses the existing session artifact directory and append-only JSONL
// convention, so it survives service reconstruction and process restart.
type applicationJournal struct {
	mu     sync.Mutex
	path   string
	replay *appplatform.MemoryReplayStore
}

var applicationJournals sync.Map

func sharedApplicationJournal(path string) (*applicationJournal, error) {
	path = filepath.Clean(path)
	if existing, ok := applicationJournals.Load(path); ok {
		return existing.(*applicationJournal), nil
	}
	journal, err := openApplicationJournal(path)
	if err != nil {
		return nil, err
	}
	actual, loaded := applicationJournals.LoadOrStore(path, journal)
	if loaded {
		return actual.(*applicationJournal), nil
	}
	return journal, nil
}

func openApplicationJournal(path string) (*applicationJournal, error) {
	journal := &applicationJournal{path: path, replay: appplatform.NewMemoryReplayStore()}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return journal, nil
		}
		return nil, fmt.Errorf("application: open journal: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var line applicationJournalLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, fmt.Errorf("application: parse journal %q: %w", path, err)
		}
		if line.Kind == "replay" && line.Key != nil && line.Outcome != nil {
			if err := journal.replay.Seed(*line.Key, *line.Outcome, line.Error); err != nil {
				return nil, fmt.Errorf("application: restore replay: %w", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("application: scan journal %q: %w", path, err)
	}
	return journal, nil
}

// openWritableApplicationJournal preflights the durable append surface before
// an effectful caller is allowed to rely on it. openApplicationJournal alone
// intentionally accepts a missing file for read/reconstruction use.
func openWritableApplicationJournal(path string) (*applicationJournal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("application: create journal directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("application: open writable journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("application: sync writable journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("application: close writable journal: %w", err)
	}
	return openApplicationJournal(path)
}

func (j *applicationJournal) Record(_ context.Context, receipt appplatform.Receipt) error {
	return j.append(applicationJournalLine{Kind: "receipt", Receipt: &receipt})
}

func (j *applicationJournal) Do(
	ctx context.Context,
	key appplatform.ReplayKey,
	execute func() (appplatform.OutcomeEnvelope, error),
) (appplatform.OutcomeEnvelope, error, bool) {
	outcome, err, replayed := j.replay.Do(ctx, key, execute)
	if replayed || outcome.Receipt.ID == "" {
		return outcome, err, replayed
	}
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	if appendErr := j.append(applicationJournalLine{
		Kind: "replay", Key: &key, Outcome: &outcome, Error: errText,
	}); appendErr != nil {
		j.replay.Forget(key)
		return appplatform.OutcomeEnvelope{}, appendErr, false
	}
	return outcome, err, false
}

func (j *applicationJournal) append(line applicationJournalLine) error {
	raw, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("application: encode journal: %w", err)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return fmt.Errorf("application: create journal directory: %w", err)
	}
	file, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("application: append journal: %w", err)
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("application: write journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("application: sync journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("application: close journal: %w", err)
	}
	return nil
}

func (j *applicationJournal) recordEventState(child appplatform.ChildRun, join appplatform.JoinState, outcome *appplatform.OutcomeEnvelope, err error) error {
	if j == nil {
		return nil
	}
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	return j.append(applicationJournalLine{
		Kind: "event_job", Child: &child, Join: &join, Outcome: outcome, Error: errText,
	})
}
