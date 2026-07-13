// Package queue persists merge-candidate admission state for the Capsule
// reconciler.  It deliberately owns no git transport: callers provide the
// verified candidate SHA and receipt, then a backend-specific dispatcher can
// construct and test the speculative merge tree.
package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kitsoki/internal/capsule/receipt"
)

const Schema = "capsule-merge-queue/v1"

type Candidate struct {
	ID        string    `json:"id"`
	Branch    string    `json:"branch"`
	SHA       string    `json:"sha"`
	ReceiptID string    `json:"receipt_id"`
	Backend   string    `json:"backend"`
	Paths     []string  `json:"paths,omitempty"`
	Status    string    `json:"status"`
	Submitted time.Time `json:"submitted_at"`
}

type State struct {
	Schema     string      `json:"schema"`
	Candidates []Candidate `json:"candidates"`
}

type Submit struct {
	Branch  string
	SHA     string
	Receipt receipt.Receipt
	Backend string
	Paths   []string
	Now     time.Time
}

// Store is a small, intentionally portable durable queue.  Its lock is an
// exclusive lock-file so independent CLI processes cannot lose submissions.
type Store struct{ ProjectRoot string }

func (s Store) Submit(in Submit) (Candidate, error) {
	if err := validate(in); err != nil {
		return Candidate{}, err
	}
	root, err := filepath.Abs(s.ProjectRoot)
	if err != nil {
		return Candidate{}, err
	}
	dir := filepath.Join(root, ".capsules", "queue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Candidate{}, err
	}
	unlock, err := lock(filepath.Join(dir, "state.lock"))
	if err != nil {
		return Candidate{}, err
	}
	defer unlock()
	state, err := read(filepath.Join(dir, "state.json"))
	if err != nil {
		return Candidate{}, err
	}
	for _, c := range state.Candidates {
		if c.SHA == in.SHA && c.Status == "queued" {
			return c, nil
		}
	}
	now := in.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	c := Candidate{ID: candidateID(in.SHA, in.Receipt.ReceiptID), Branch: in.Branch, SHA: in.SHA, ReceiptID: in.Receipt.ReceiptID, Backend: defaultBackend(in.Backend), Paths: cleanPaths(in.Paths), Status: "queued", Submitted: now}
	state.Candidates = append(state.Candidates, c)
	if err := write(filepath.Join(dir, "state.json"), state); err != nil {
		return Candidate{}, err
	}
	return c, nil
}

func (s Store) List() (State, error) {
	root, err := filepath.Abs(s.ProjectRoot)
	if err != nil {
		return State{}, err
	}
	return read(filepath.Join(root, ".capsules", "queue", "state.json"))
}

func validate(in Submit) error {
	if strings.TrimSpace(in.Branch) == "" || filepath.Base(in.Branch) != in.Branch {
		return fmt.Errorf("queue: branch is required and must be a single ref segment")
	}
	if len(in.SHA) != 40 || strings.Trim(in.SHA, "0123456789abcdef") != "" {
		return fmt.Errorf("queue: candidate SHA must be a lowercase full git SHA")
	}
	if got := receipt.Verify(in.Receipt, nil, false); got.Status != "valid" || !got.PromotionEligible {
		return fmt.Errorf("queue: receipt is not a valid promotion-eligible capsule CI receipt")
	}
	if in.Receipt.Envelope.SourceDigest != in.SHA {
		return fmt.Errorf("queue: receipt candidate SHA does not match submission")
	}
	return nil
}

func read(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{Schema: Schema, Candidates: []Candidate{}}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("queue: parse state: %w", err)
	}
	if state.Schema != Schema {
		return State{}, fmt.Errorf("queue: unsupported state schema %q", state.Schema)
	}
	return state, nil
}
func write(path string, state State) error {
	state.Schema = Schema
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("queue: another serializer owns %s: %w", path, err)
	}
	return func() { _ = f.Close(); _ = os.Remove(path) }, nil
}
func candidateID(sha, receiptID string) string {
	sum := sha256.Sum256([]byte(sha + "\n" + receiptID))
	return "queue-" + hex.EncodeToString(sum[:])[:12]
}
func defaultBackend(v string) string {
	if strings.TrimSpace(v) == "" {
		return "local"
	}
	return v
}
func cleanPaths(paths []string) []string {
	out := append([]string(nil), paths...)
	sort.Strings(out)
	return out
}
