package vmpool

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreLoadEmptyWhenMissing(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Schema != StateSchema {
		t.Fatalf("schema=%q, want %q", state.Schema, StateSchema)
	}
	if len(state.Workers) != 0 {
		t.Fatalf("workers=%#v, want empty", state.Workers)
	}
}

func TestStoreUpdateRoundTrip(t *testing.T) {
	root := t.TempDir()
	store := Store{ProjectRoot: root}
	w := Worker{ID: "vm-job1", JobID: "job1", Status: StatusCreating, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

	err := store.Update(func(state *State) error {
		state.Workers = append(state.Workers, w)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Workers) != 1 || got.Workers[0].ID != w.ID {
		t.Fatalf("workers=%#v", got.Workers)
	}
	if got.Schema != StateSchema {
		t.Fatalf("schema=%q", got.Schema)
	}

	// Persisted file exists at the documented path.
	statePath := filepath.Join(root, ".capsules", "vmpool", "state.json")
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state.json not written: %v", err)
	}
	// The lock file must not linger after a successful Update.
	lockPath := filepath.Join(root, ".capsules", "vmpool", "state.lock")
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock file left behind: err=%v", err)
	}
}

func TestStoreUpdateAbortsOnError(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sentinel := errors.New("boom")

	err := store.Update(func(state *State) error {
		state.Workers = append(state.Workers, Worker{ID: "vm-job1"})
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v, want %v", err, sentinel)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Workers) != 0 {
		t.Fatalf("workers=%#v, want no persisted change", got.Workers)
	}
}

func TestStoreConcurrentUpdateSerializesBothWrites(t *testing.T) {
	// LockWait is set explicitly (mirroring queue's contention test): the
	// default zero LockWait intentionally makes only one attempt and fails
	// fast on contention, so retrying contention tolerance is opt-in.
	store := Store{ProjectRoot: t.TempDir(), LockWait: time.Second}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"vm-job1", "vm-job2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			errs <- store.Update(func(state *State) error {
				state.Workers = append(state.Workers, Worker{ID: id, JobID: id, Status: StatusCreating})
				return nil
			})
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Workers) != 2 {
		t.Fatalf("workers=%#v, want both concurrent updates preserved", got.Workers)
	}
}

// TestStoreUpdateReturnsBusyWithinBoundOnLockContention mirrors the queue
// store's contract: a caller with a zero LockWait must get a fast,
// errors.Is-detectable ErrBusy rather than hanging when the state lock is
// already held by someone else.
func TestStoreUpdateReturnsBusyWithinBoundOnLockContention(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".capsules", "vmpool")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(dir, "state.lock")
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close(); _ = os.Remove(lockFile) }()

	store := Store{ProjectRoot: root}
	done := make(chan error, 1)
	go func() {
		done <- store.Update(func(state *State) error { return nil })
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("want ErrBusy, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Update did not return within bound while the lock was held")
	}
}

func TestStateWorkerByIDAndActiveCount(t *testing.T) {
	state := State{Workers: []Worker{
		{ID: "vm-a", Status: StatusCreating},
		{ID: "vm-b", Status: StatusReady},
		{ID: "vm-c", Status: StatusDestroyed},
		{ID: "vm-d", Status: StatusFailed},
	}}

	if _, ok := state.WorkerByID("vm-missing"); ok {
		t.Fatal("expected not found")
	}
	w, ok := state.WorkerByID("vm-b")
	if !ok || w.Status != StatusReady {
		t.Fatalf("worker=%#v ok=%v", w, ok)
	}
	if got := state.ActiveCount(); got != 2 {
		t.Fatalf("ActiveCount=%d, want 2 (creating + ready, excluding terminal)", got)
	}
}
