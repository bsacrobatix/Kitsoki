package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GateMemo caches passing deterministic-gate results by exact tree + gate
// identity, so a candidate that reprepares onto a tree the gate has already
// validated — a stale base whose fresh classification is still
// LocalAhead/UpToDate (see the train-stacking cascade), or a retry after an
// unrelated environmental failure — never re-runs a gate that already
// proved green. Only passing results are ever stored: a cache hit can
// never mask a fix, since nothing is memoized until it has already passed.
// A nil GateMemo in ProcessDeps disables memoization entirely.
type GateMemo interface {
	Lookup(treeSHA, gateVersion string) (GateResult, bool)
	Store(treeSHA, gateVersion string, result GateResult) error
}

// FileGateMemo is the durable, file-based GateMemo: one JSON entry per
// (treeSHA, gateVersion) pair under .capsules/queue/gate-memo, alongside
// the queue's own durable state. Entries are content-addressed and never
// expire on their own — a tree's content and a gate's identity are exactly
// what a gate result is a deterministic function of; there is nothing else
// for a cache entry to go stale against. Operators who change what a gate
// version name means without also changing the name are responsible for
// that collision, exactly as GateVersion already assumes elsewhere.
type FileGateMemo struct {
	ProjectRoot string
}

type gateMemoEntry struct {
	Passed      bool      `json:"passed"`
	Evidence    []string  `json:"evidence,omitempty"`
	Log         string    `json:"log,omitempty"`
	GateVersion string    `json:"gate_version"`
	CachedAt    time.Time `json:"cached_at"`
}

func (m FileGateMemo) Lookup(treeSHA, gateVersion string) (GateResult, bool) {
	path, ok := m.path(treeSHA, gateVersion)
	if !ok {
		return GateResult{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return GateResult{}, false
	}
	var entry gateMemoEntry
	if err := json.Unmarshal(raw, &entry); err != nil || !entry.Passed {
		return GateResult{}, false
	}
	evidence := append(append([]string(nil), entry.Evidence...),
		fmt.Sprintf("queue:gate-reused=memo tree=%s gate_version=%s cached_at=%s", treeSHA, gateVersion, entry.CachedAt.Format(time.RFC3339)))
	return GateResult{Passed: true, Evidence: evidence, Log: entry.Log, GateVersion: first(entry.GateVersion, gateVersion)}, true
}

func (m FileGateMemo) Store(treeSHA, gateVersion string, result GateResult) error {
	if !result.Passed {
		return nil
	}
	path, ok := m.path(treeSHA, gateVersion)
	if !ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	entry := gateMemoEntry{Passed: true, Evidence: result.Evidence, Log: result.Log, GateVersion: first(result.GateVersion, gateVersion), CachedAt: time.Now().UTC()}
	raw, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gate-memo-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(raw, '\n')); err == nil {
		err = tmp.Chmod(0o600)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (m FileGateMemo) path(treeSHA, gateVersion string) (string, bool) {
	if strings.TrimSpace(treeSHA) == "" || strings.TrimSpace(gateVersion) == "" {
		return "", false
	}
	root, err := filepath.Abs(m.ProjectRoot)
	if err != nil {
		return "", false
	}
	key := strings.TrimPrefix(fingerprint(treeSHA, gateVersion), "sha256:")
	return filepath.Join(root, ".capsules", "queue", "gate-memo", key+".json"), true
}
