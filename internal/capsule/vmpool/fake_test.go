package vmpool

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestFakeCreateAssignsSequentialIDs(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	first, err := f.Create(ctx, CreateParams{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.Create(ctx, CreateParams{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "1" || second.ID != "2" {
		t.Fatalf("expected sequential IDs 1, 2; got %q, %q", first.ID, second.ID)
	}
	if first.Status != "new" || first.PublicIP != "" || first.PrivateIP != "" {
		t.Fatalf("expected new instance with empty IPs, got %#v", first)
	}
}

func TestFakeActivateAndFail(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	inst, err := f.Create(ctx, CreateParams{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}

	f.Activate(inst.ID, "1.2.3.4", "10.0.0.1")
	got, found, err := f.Get(ctx, inst.ID)
	if err != nil || !found {
		t.Fatalf("Get after Activate: found=%v err=%v", found, err)
	}
	if got.Status != "active" || got.PublicIP != "1.2.3.4" || got.PrivateIP != "10.0.0.1" {
		t.Fatalf("unexpected instance after Activate: %#v", got)
	}

	f.Fail(inst.ID)
	got, found, err = f.Get(ctx, inst.ID)
	if err != nil || !found {
		t.Fatalf("Get after Fail: found=%v err=%v", found, err)
	}
	if got.Status != "errored" {
		t.Fatalf("expected errored status, got %q", got.Status)
	}

	// Activate/Fail on an unknown instance is a no-op, not a panic.
	f.Activate("does-not-exist", "1.1.1.1", "2.2.2.2")
	f.Fail("does-not-exist")
}

func TestFakeGetUnknownInstance(t *testing.T) {
	f := NewFake()
	_, found, err := f.Get(context.Background(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected found=false for unknown instance")
	}
}

func TestFakeDestroyIsIdempotent(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	inst, err := f.Create(ctx, CreateParams{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Destroy(ctx, inst.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.Destroy(ctx, inst.ID); err != nil {
		t.Fatalf("second destroy of same instance must not error: %v", err)
	}
	if err := f.Destroy(ctx, "never-existed"); err != nil {
		t.Fatalf("destroy of unknown instance must not error: %v", err)
	}
	if _, found, _ := f.Get(ctx, inst.ID); found {
		t.Fatal("expected instance to be gone after Destroy")
	}
	if got := f.Destroyed(); len(got) != 3 {
		t.Fatalf("expected 3 recorded destroy calls, got %v", got)
	}
}

func TestFakeListByTag(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	a, err := f.Create(ctx, CreateParams{Name: "a", Tags: []string{"kitsoki-worker", "extra"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Create(ctx, CreateParams{Name: "b", Tags: []string{"other"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Create(ctx, CreateParams{Name: "c", Tags: []string{"kitsoki-worker"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = b

	got, err := f.ListByTag(ctx, "kitsoki-worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != a.ID || got[1].ID != c.ID {
		t.Fatalf("expected [%s %s] in order, got %#v", a.ID, c.ID, got)
	}

	none, err := f.ListByTag(ctx, "no-such-tag")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no matches, got %#v", none)
	}
}

func TestFakeSnapshotIsDeterministicAndRecorded(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	inst, err := f.Create(ctx, CreateParams{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}

	imageID, err := f.Snapshot(ctx, inst.ID, "base-image")
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.Snapshot(ctx, inst.ID, "base-image")
	if err != nil {
		t.Fatal(err)
	}
	if imageID != again {
		t.Fatalf("expected deterministic image ID, got %q then %q", imageID, again)
	}

	calls := f.Snapshots()
	if len(calls) != 2 || calls[0].InstanceID != inst.ID || calls[0].Name != "base-image" || calls[0].ImageID != imageID {
		t.Fatalf("unexpected recorded snapshot calls: %#v", calls)
	}

	if _, err := f.Snapshot(ctx, "missing", "x"); err == nil {
		t.Fatal("expected error snapshotting an unknown instance")
	}
}

func TestFakeResolveImage(t *testing.T) {
	f := NewFake()
	f.Images = map[string]string{"base-image": "12345"}

	resolved, err := f.ResolveImage(context.Background(), "base-image")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "12345" {
		t.Fatalf("expected mapped resolution, got %q", resolved)
	}

	passthrough, err := f.ResolveImage(context.Background(), "ubuntu-22-04-x64")
	if err != nil {
		t.Fatal(err)
	}
	if passthrough != "ubuntu-22-04-x64" {
		t.Fatalf("expected slug pass-through, got %q", passthrough)
	}
}

func TestFakeCreatedReturnsIndependentCopies(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	params := CreateParams{Name: "a", Tags: []string{"t1"}, SSHKeyIDs: []string{"1"}}
	if _, err := f.Create(ctx, params); err != nil {
		t.Fatal(err)
	}

	// Mutating the caller's slice after Create must not affect what Fake
	// recorded, and mutating the returned snapshot must not affect Fake's
	// internal state either.
	params.Tags[0] = "mutated"

	got := f.Created()
	if len(got) != 1 || got[0].Tags[0] != "t1" {
		t.Fatalf("expected recorded params insulated from caller mutation, got %#v", got)
	}
	got[0].Tags[0] = "mutated-again"
	got2 := f.Created()
	if got2[0].Tags[0] != "t1" {
		t.Fatalf("expected Created() to return fresh copies, got %#v", got2)
	}
}

func TestFakeCustomNowStampsCreatedAt(t *testing.T) {
	f := NewFake()
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f.Now = func() time.Time { return fixed }

	inst, err := f.Create(context.Background(), CreateParams{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if !inst.CreatedAt.Equal(fixed) {
		t.Fatalf("expected CreatedAt=%v, got %v", fixed, inst.CreatedAt)
	}
}

func TestFakeConcurrentUse(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	const n = 50

	var wg sync.WaitGroup
	ids := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst, err := f.Create(ctx, CreateParams{Name: "worker", Tags: []string{"kitsoki-worker"}})
			if err != nil {
				t.Error(err)
				return
			}
			f.Activate(inst.ID, "1.1.1.1", "2.2.2.2")
			ids <- inst.ID
		}()
	}
	wg.Wait()
	close(ids)

	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate instance ID %q under concurrent Create", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Fatalf("expected %d unique instances, got %d", n, len(seen))
	}
	list, err := f.ListByTag(ctx, "kitsoki-worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != n {
		t.Fatalf("expected %d instances by tag, got %d", n, len(list))
	}
}
