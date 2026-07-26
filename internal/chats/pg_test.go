package chats_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"kitsoki/internal/chats"
	"kitsoki/internal/clock"
	"kitsoki/internal/dbruntime/pgtest"
)

// openPGStore constructs a Postgres-backed chats.Store on the given database.
// Multiple stores may share one db to model separate processes contending for
// the same server (each Store gets its own lease-holder token).
func openPGStore(t *testing.T, db *sql.DB, fake *clock.Fake) *chats.Store {
	t.Helper()
	cs, err := chats.NewPostgresStore(db, chats.WithClock(fake))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	return cs
}

func TestPostgresStore_ChatLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := pgtest.Open(t)
	fake := clock.NewFake(time.Unix(1000, 0))
	cs := openPGStore(t, db, fake)

	c, err := cs.Create(ctx, "app1", "agent", "PROJ-1", "My Chat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := cs.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "My Chat" || got.Status != string(chats.ChatActive) {
		t.Errorf("unexpected chat %+v", got)
	}
	if _, err := cs.Get(ctx, "nope"); !errors.Is(err, chats.ErrChatNotFound) {
		t.Errorf("Get missing: want ErrChatNotFound, got %v", err)
	}

	// Resolve finds the existing non-fork chat rather than creating.
	r, created, err := cs.Resolve(ctx, "app1", "agent", "PROJ-1", "ignored")
	if err != nil || created || r.ID != c.ID {
		t.Fatalf("Resolve existing: id=%v created=%v err=%v", r, created, err)
	}

	// Append messages with advancing clock; transcript order and cursor.
	for i, content := range []string{"hello", "world", "again"} {
		fake.Advance(time.Second)
		m, err := cs.AppendMessage(ctx, c.ID, "user", content, map[string]any{"i": float64(i)})
		if err != nil {
			t.Fatalf("AppendMessage %d: %v", i, err)
		}
		if m.Seq != i {
			t.Errorf("AppendMessage %d: seq = %d", i, m.Seq)
		}
	}
	msgs, err := cs.Transcript(ctx, c.ID, 0)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(msgs) != 3 || msgs[0].Content != "hello" || msgs[2].Content != "again" {
		t.Fatalf("Transcript: got %+v", msgs)
	}
	if msgs[1].Metadata["i"] != float64(1) {
		t.Errorf("metadata roundtrip: %+v", msgs[1].Metadata)
	}
	tail, err := cs.Transcript(ctx, c.ID, 2)
	if err != nil || len(tail) != 1 || tail[0].Content != "again" {
		t.Fatalf("Transcript since 2: %+v err=%v", tail, err)
	}
	if seq, err := cs.LatestSeq(ctx, c.ID); err != nil || seq != 2 {
		t.Fatalf("LatestSeq: %d err=%v", seq, err)
	}

	// Fork copies the transcript.
	fork, err := cs.Fork(ctx, c.ID, "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if fork.ParentChatID != c.ID || fork.Title != "My Chat (fork)" {
		t.Errorf("Fork chat: %+v", fork)
	}
	forkMsgs, err := cs.Transcript(ctx, fork.ID, 0)
	if err != nil || len(forkMsgs) != 3 {
		t.Fatalf("Fork transcript: %d msgs err=%v", len(forkMsgs), err)
	}

	// Rename / Archive; archived chats are invisible to Resolve.
	if err := cs.Rename(ctx, c.ID, "Renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := cs.SetClaudeSessionID(ctx, c.ID, "sess-1"); err != nil {
		t.Fatalf("SetClaudeSessionID: %v", err)
	}
	if err := cs.Archive(ctx, c.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	fake.Advance(time.Second)
	r2, created, err := cs.Resolve(ctx, "app1", "agent", "PROJ-1", "fresh")
	if err != nil || !created {
		t.Fatalf("Resolve after archive: created=%v err=%v", created, err)
	}
	if r2.ID == c.ID || r2.ID == fork.ID {
		// The fork has parent_chat_id set, so it must not be resolved either.
		t.Errorf("Resolve after archive reused %s", r2.ID)
	}

	// GetOrEnsure inserts a placeholder row via ON CONFLICT DO NOTHING.
	ph, err := cs.GetOrEnsure(ctx, "01PLACEHOLDER0000000000000")
	if err != nil {
		t.Fatalf("GetOrEnsure: %v", err)
	}
	if ph.Title != "untitled chat" {
		t.Errorf("GetOrEnsure placeholder: %+v", ph)
	}

	// List filters and orders by last_active_at DESC.
	all, err := cs.List(ctx, "app1", "agent", "PROJ-1")
	if err != nil || len(all) != 3 { // original, fork, fresh
		t.Fatalf("List: %d chats err=%v", len(all), err)
	}

	// Reopening against the same database is an idempotent no-op.
	if _, err := chats.NewPostgresStore(db); err != nil {
		t.Fatalf("reopen NewPostgresStore: %v", err)
	}
}

func TestPostgresStore_QueueOrdering(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := pgtest.Open(t)
	fake := clock.NewFake(time.Unix(2000, 0))
	cs := openPGStore(t, db, fake)

	c, err := cs.Create(ctx, "app1", "agent", "", "queue chat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var ids []string
	for _, payload := range []string{"first", "second", "third"} {
		fake.Advance(time.Second)
		d, err := cs.Enqueue(ctx, chats.EnqueueOptions{
			ChatID:          c.ID,
			Transport:       chats.DriveTransportMCP,
			Payload:         payload,
			OriginSessionID: "sess-q",
		})
		if err != nil {
			t.Fatalf("Enqueue %q: %v", payload, err)
		}
		ids = append(ids, d.DriveID)
	}

	pending, err := cs.ListDrives(ctx, c.ID, chats.ListDrivesFilter{Statuses: []chats.DriveStatus{chats.DriveStatusPending}})
	if err != nil || len(pending) != 3 {
		t.Fatalf("ListDrives pending: %d err=%v", len(pending), err)
	}
	for i, d := range pending {
		if d.DriveID != ids[i] {
			t.Errorf("pending[%d] = %s, want %s (FIFO by received_at)", i, d.DriveID, ids[i])
		}
	}

	// Dequeue claims strictly oldest-first.
	d1, err := cs.Dequeue(ctx, c.ID)
	if err != nil || d1.DriveID != ids[0] || d1.Status != chats.DriveStatusDispatching {
		t.Fatalf("Dequeue #1: %+v err=%v", d1, err)
	}
	if err := cs.MarkDriveDone(ctx, d1.DriveID, 7); err != nil {
		t.Fatalf("MarkDriveDone: %v", err)
	}
	d2, err := cs.Dequeue(ctx, c.ID)
	if err != nil || d2.DriveID != ids[1] {
		t.Fatalf("Dequeue #2: %+v err=%v", d2, err)
	}
	if err := cs.MarkDriveFailed(ctx, d2.DriveID, "boom"); err != nil {
		t.Fatalf("MarkDriveFailed: %v", err)
	}

	// ClaimDrive by id; a dispatching drive is not dismissable.
	if _, err := cs.ClaimDrive(ctx, ids[2]); err != nil {
		t.Fatalf("ClaimDrive: %v", err)
	}
	if err := cs.MarkDriveDismissed(ctx, ids[2]); !errors.Is(err, chats.ErrDriveStateMismatch) {
		t.Errorf("dismiss dispatching: want ErrDriveStateMismatch, got %v", err)
	}
	if err := cs.MarkDriveDone(ctx, ids[2], 9); err != nil {
		t.Fatalf("MarkDriveDone #3: %v", err)
	}

	if _, err := cs.Dequeue(ctx, c.ID); !errors.Is(err, chats.ErrNoPendingDrive) {
		t.Errorf("Dequeue empty: want ErrNoPendingDrive, got %v", err)
	}
	if _, err := cs.GetDrive(ctx, "missing"); !errors.Is(err, chats.ErrDriveNotFound) {
		t.Errorf("GetDrive missing: want ErrDriveNotFound, got %v", err)
	}

	// Terminal detail survives the round trip.
	done, err := cs.GetDrive(ctx, ids[0])
	if err != nil || done.ResultSeq == nil || *done.ResultSeq != 7 {
		t.Fatalf("GetDrive done: %+v err=%v", done, err)
	}
	failed, err := cs.GetDrive(ctx, ids[1])
	if err != nil || failed.ErrorMessage != "boom" {
		t.Fatalf("GetDrive failed: %+v err=%v", failed, err)
	}

	// Session-scoped listing sees every drive in FIFO order.
	bySession, err := cs.ListDrivesBySession(ctx, "sess-q", nil)
	if err != nil || len(bySession) != 3 || bySession[0].DriveID != ids[0] {
		t.Fatalf("ListDrivesBySession: %d err=%v", len(bySession), err)
	}
	byOrigin, err := cs.ListDrivesByOrigin(ctx, []chats.DriveStatus{chats.DriveStatusFailed})
	if err != nil || len(byOrigin) != 1 || byOrigin[0].DriveID != ids[1] {
		t.Fatalf("ListDrivesByOrigin: %+v err=%v", byOrigin, err)
	}
}

func TestPostgresStore_LockContentionAndLeaseTakeover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := pgtest.Open(t)
	fake1 := clock.NewFake(time.Unix(3000, 0))
	fake2 := clock.NewFake(time.Unix(3000, 0))
	// Two stores on one database model two processes contending for the
	// same chat; each carries its own lease-holder token.
	cs1 := openPGStore(t, db, fake1)
	cs2 := openPGStore(t, db, fake2)

	c, err := cs1.Create(ctx, "app1", "agent", "", "locked chat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Holder blocks inside the lock until told to release.
	acquired := make(chan struct{})
	releaseNow := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- cs1.WithLock(ctx, c.ID, func(context.Context) error {
			close(acquired)
			<-releaseNow
			return nil
		})
	}()
	<-acquired

	// Unexpired lease: the second store is busy — including a heartbeat,
	// which only the holder may issue.
	if err := cs2.WithLock(ctx, c.ID, func(context.Context) error { return nil }); !errors.Is(err, chats.ErrChatBusy) {
		t.Errorf("contended WithLock: want ErrChatBusy, got %v", err)
	}
	if err := cs1.Heartbeat(ctx, c.ID); err != nil {
		t.Errorf("holder Heartbeat: %v", err)
	}
	if err := cs2.Heartbeat(ctx, c.ID); err == nil {
		t.Error("non-holder Heartbeat: want error, got nil")
	}

	// After release the lock is free again.
	close(releaseNow)
	if err := <-holderDone; err != nil {
		t.Fatalf("holder WithLock: %v", err)
	}
	if err := cs2.WithLock(ctx, c.ID, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("WithLock after release: %v", err)
	}

	// Lease takeover: cs1 acquires and "crashes" (never releases within the
	// lease TTL); once cs2's clock passes expires_at it takes the lease over
	// without any PID probing.
	acquired2 := make(chan struct{})
	release2 := make(chan struct{})
	done2 := make(chan error, 1)
	go func() {
		done2 <- cs1.WithLock(ctx, c.ID, func(context.Context) error {
			close(acquired2)
			<-release2
			return nil
		})
	}()
	<-acquired2
	if err := cs2.WithLock(ctx, c.ID, func(context.Context) error { return nil }); !errors.Is(err, chats.ErrChatBusy) {
		t.Fatalf("pre-expiry WithLock: want ErrChatBusy, got %v", err)
	}
	fake2.Advance(time.Minute) // > pgLeaseTTL
	if err := cs2.WithLock(ctx, c.ID, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("takeover WithLock after expiry: %v", err)
	}
	// Let the abandoned holder finish; its scoped release must not error
	// even though its lease row was taken over and already deleted.
	close(release2)
	if err := <-done2; err != nil {
		t.Fatalf("abandoned holder WithLock: %v", err)
	}
}

// TestPostgresStore_WithLockHeartbeatRenewal proves a live holder is never
// taken over mid-fn: WithLock's background heartbeat renews the lease, so
// even after both clocks sail past the original expires_at a contender stays
// busy. Takeover (previous test) therefore requires the holder to stop
// beating — crash or partition — not merely to run longer than the TTL.
func TestPostgresStore_WithLockHeartbeatRenewal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := pgtest.Open(t)
	base := time.Unix(4000, 0)
	fake1 := clock.NewFake(base)
	fake2 := clock.NewFake(base)
	cs1 := openPGStore(t, db, fake1)
	cs1.SetLeaseHeartbeatIntervalForTest(10 * time.Millisecond)
	cs2 := openPGStore(t, db, fake2)

	c, err := cs1.Create(ctx, "app1", "agent", "", "long drive")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	acquired := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- cs1.WithLock(ctx, c.ID, func(context.Context) error {
			close(acquired)
			<-release
			return nil
		})
	}()
	<-acquired

	// Sail both clocks a full minute past acquisition (twice the lease TTL).
	// The holder's next beat stamps expires_at from fake1's new now; wait
	// until that renewal is visible before contending, so the check below is
	// deterministic rather than racing the first post-advance tick.
	fake1.Advance(time.Minute)
	fake2.Advance(time.Minute)
	staleExpiry := base.Add(time.Minute).UnixMicro() // any renewal lands past this
	deadline := time.Now().Add(10 * time.Second)
	for {
		var expiresAt int64
		if err := db.QueryRow(
			`SELECT expires_at FROM chats.chat_locks WHERE chat_id = $1`, c.ID,
		).Scan(&expiresAt); err != nil {
			t.Fatalf("read lease expiry: %v", err)
		}
		if expiresAt > staleExpiry {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease never renewed: expires_at=%d still <= %d", expiresAt, staleExpiry)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The renewed lease keeps the contender out even though the original
	// expiry has long passed on its clock.
	if err := cs2.WithLock(ctx, c.ID, func(context.Context) error { return nil }); !errors.Is(err, chats.ErrChatBusy) {
		t.Fatalf("contended WithLock after renewal: want ErrChatBusy, got %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("holder WithLock: %v", err)
	}
	if err := cs2.WithLock(ctx, c.ID, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("WithLock after release: %v", err)
	}
}
