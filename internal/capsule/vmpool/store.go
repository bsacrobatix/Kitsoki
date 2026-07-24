package vmpool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"kitsoki/internal/atomicfile"
)

// StateSchema identifies the durable vmpool state format.
const StateSchema = "capsule-vmpool/v1"

// State is the durable pool state persisted to disk.
type State struct {
	Schema  string   `json:"schema"`
	Workers []Worker `json:"workers"`
}

// ErrBusy indicates the state lock is held by a concurrent caller and could
// not be acquired within Store.LockWait.
var ErrBusy = errors.New("vmpool: serializer busy")

// Store persists pool state under <ProjectRoot>/.capsules/vmpool/state.json,
// serialized against concurrent callers with a sibling state.lock lock file.
// It mirrors the durable-state idiom used by internal/capsule/queue.Store.
// A zero LockWait (the default) makes a single attempt and returns a typed
// ErrBusy immediately on contention, matching queue.Store's contract;
// callers that want retrying contention tolerance set LockWait explicitly.
type Store struct {
	ProjectRoot string
	LockWait    time.Duration
}

// Load reads the durable state, tolerating a missing state file by returning
// an empty, schema-stamped State. Load does not take the lock: callers that
// need a consistent read-modify-write should use Update instead.
func (s Store) Load() (State, error) {
	_, path, err := s.paths()
	if err != nil {
		return State{}, err
	}
	return read(path)
}

// Update runs fn against the current durable state while holding the store
// lock, then atomically persists the (possibly mutated) state. If fn returns
// an error, the update is aborted and the state on disk is left unchanged.
func (s Store) Update(fn func(*State) error) error {
	dir, path, err := s.paths()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("vmpool: create state dir: %w", err)
	}
	unlock, err := lock(filepath.Join(dir, "state.lock"), s.LockWait)
	if err != nil {
		return err
	}
	defer unlock()

	state, err := read(path)
	if err != nil {
		return err
	}
	if err := fn(&state); err != nil {
		return err
	}
	return write(path, state)
}

func (s Store) paths() (string, string, error) {
	root, err := filepath.Abs(s.ProjectRoot)
	if err != nil {
		return "", "", fmt.Errorf("vmpool: resolve project root: %w", err)
	}
	dir := filepath.Join(root, ".capsules", "vmpool")
	return dir, filepath.Join(dir, "state.json"), nil
}

func read(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{Schema: StateSchema, Workers: []Worker{}}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("vmpool: read state: %w", err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("vmpool: parse state: %w", err)
	}
	if state.Schema == "" {
		state.Schema = StateSchema
	}
	if state.Workers == nil {
		state.Workers = []Worker{}
	}
	return state, nil
}

func write(path string, state State) error {
	if state.Schema == "" {
		state.Schema = StateSchema
	}
	if state.Workers == nil {
		state.Workers = []Worker{}
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("vmpool: marshal state: %w", err)
	}
	if err := atomicfile.WriteFile(path, append(raw, '\n'), 0o600, 0o755); err != nil {
		return fmt.Errorf("vmpool: commit state: %w", err)
	}
	return nil
}

// lock acquires an exclusive lock file at path, retrying with a short sleep
// until wait elapses. Mirrors internal/capsule/queue's lock helper.
func lock(path string, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return func() { _ = f.Close(); _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("vmpool: acquire serializer: %w", err)
		}
		if wait <= 0 || time.Now().After(deadline) {
			return nil, fmt.Errorf("vmpool: acquire serializer: %w: %w", ErrBusy, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// WorkerByID returns the worker with the given ID, if present.
func (st State) WorkerByID(id string) (Worker, bool) {
	for _, w := range st.Workers {
		if w.ID == id {
			return w, true
		}
	}
	return Worker{}, false
}

// ActiveCount returns the number of workers not in a terminal status.
func (st State) ActiveCount() int {
	n := 0
	for _, w := range st.Workers {
		if !w.Status.Terminal() {
			n++
		}
	}
	return n
}

// indexByID returns the index of the worker with the given ID, or -1.
func (st *State) indexByID(id string) int {
	for i := range st.Workers {
		if st.Workers[i].ID == id {
			return i
		}
	}
	return -1
}
