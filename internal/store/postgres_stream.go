package store

// postgres_stream.go implements the [EventStream] interface on postgresStore
// (see stream.go for the contract). The events table's stream_pos BIGSERIAL
// is the cursor; LISTEN/NOTIFY on pgEventsNotifyChannel is the wake-up.
//
// ── Why the naive read would miss rows, and how appends prevent it ──────────
//
// BIGSERIAL values are assigned at INSERT time, inside the appending
// transaction, but become visible at COMMIT time — and assignment order and
// commit order can invert: tx A grabs pos 5..7, tx B grabs pos 8, B commits
// first. A reader that sees pos 8 and advances its cursor past 5..7 before A
// commits would miss A's rows forever.
//
// The fix chosen here (over pg_snapshot/xmin fencing, which needs the reader
// to track in-flight txids) is to make assignment order EQUAL commit order:
// appendEventsPgTx takes pg_advisory_xact_lock(pgStreamAppendLockKey) before
// its first INSERT, and an advisory xact lock is held until the transaction
// commits or rolls back. So for any two committed appends, the one holding
// smaller positions committed first; when a reader sees a committed row at
// pos p, every pos' < p is either already committed or belongs to a
// rolled-back transaction (a permanent, never-filled gap — sequence values
// are not reused). A plain `WHERE stream_pos > $after ORDER BY stream_pos`
// therefore never skips a row. The cost is that event appends serialize
// globally on the pg backend (they already serialized per-session via the
// sessions FOR UPDATE row lock; lock order is always session row → advisory,
// so no deadlock). TestPG_Stream_ConcurrentAppends_NoGapNoDup pins this
// contract.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"kitsoki/internal/app"
)

const (
	// pgEventsNotifyChannel is the NOTIFY channel appends signal on. The
	// payload is the session id, but WaitForEvents treats any notification
	// as "re-check the cursor" — the payload is informational only.
	pgEventsNotifyChannel = "kitsoki_events"

	// pgStreamAppendLockKey is the advisory-lock key serializing event
	// appends so stream_pos assignment order equals commit order (see the
	// file comment). Arbitrary but fixed; spells "kStream1" in ASCII.
	pgStreamAppendLockKey int64 = 0x6B_53_74_72_65_61_6D_31

	// pgStreamPollDefault is the periodic re-check interval WaitForEvents
	// falls back to when a NOTIFY is lost (or the listen conn breaks). It
	// bounds staleness, not correctness.
	pgStreamPollDefault = 3 * time.Second

	// pgStreamRetryBackoff paces WaitForEvents' retry loop after a transient
	// database error, so a down server is not hammered.
	pgStreamRetryBackoff = 250 * time.Millisecond
)

// Compile-time check: the pg store provides the optional stream capability.
var _ EventStream = (*postgresStore)(nil)

// pgStreamColumns is the shared SELECT list for stream reads; scan order must
// match scanStreamEntries.
const pgStreamColumns = `stream_pos, session_id, turn, seq, ts, kind, payload_json,
	state_path, call_id, parent_turn, episode_id, match_idx`

// ReadStream returns up to limit entries with Pos > after across all
// sessions, ascending by Pos. limit=0 means no limit.
func (s *postgresStore) ReadStream(ctx context.Context, after StreamCursor, limit int) ([]StreamEntry, error) {
	q := `SELECT ` + pgStreamColumns + `
	      FROM events
	      WHERE stream_pos > $1
	      ORDER BY stream_pos ASC`
	args := []any{int64(after)}
	if limit > 0 {
		q += " LIMIT $2"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store.ReadStream: %w", err)
	}
	defer rows.Close()
	return scanStreamEntries(rows, "store.ReadStream")
}

// ReadSessionStream is ReadStream restricted to one session; the cursor space
// is the same global stream_pos.
func (s *postgresStore) ReadSessionStream(ctx context.Context, session app.SessionID, after StreamCursor, limit int) ([]StreamEntry, error) {
	q := `SELECT ` + pgStreamColumns + `
	      FROM events
	      WHERE session_id = $1 AND stream_pos > $2
	      ORDER BY stream_pos ASC`
	args := []any{string(session), int64(after)}
	if limit > 0 {
		q += " LIMIT $3"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store.ReadSessionStream: %w", err)
	}
	defer rows.Close()
	return scanStreamEntries(rows, "store.ReadSessionStream")
}

// scanStreamEntries drains a pgStreamColumns rowset into StreamEntry values.
func scanStreamEntries(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}, op string) ([]StreamEntry, error) {
	var out []StreamEntry
	for rows.Next() {
		var (
			pos        int64
			sid        string
			turnN      int64
			seq        int
			tsMicro    int64
			kind       string
			payload    string
			statePath  string
			callID     string
			parentTurn int64
			episodeID  string
			matchIdx   int
		)
		if err := rows.Scan(&pos, &sid, &turnN, &seq, &tsMicro, &kind, &payload,
			&statePath, &callID, &parentTurn, &episodeID, &matchIdx); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", op, err)
		}
		out = append(out, StreamEntry{
			Pos:     pos,
			Session: app.SessionID(sid),
			Event: Event{
				Turn:       app.TurnNumber(turnN),
				Seq:        seq,
				Ts:         time.UnixMicro(tsMicro),
				Kind:       EventKind(kind),
				Payload:    []byte(payload),
				StatePath:  app.StatePath(statePath),
				CallID:     callID,
				ParentTurn: app.TurnNumber(parentTurn),
				EpisodeID:  episodeID,
				MatchIdx:   matchIdx,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: rows: %w", op, err)
	}
	return out, nil
}

// WaitForEvents blocks until an entry with Pos > after exists, ctx is
// cancelled, or an unrecoverable state is hit (which it treats as transient
// and retries with backoff — the pool discards broken conns, so a bounced
// server reconnects transparently). Wake-up path: LISTEN on
// pgEventsNotifyChannel with a s.streamPoll timeout, then re-check; the
// EXISTS query is the source of truth, notifications only shorten the wait.
func (s *postgresStore) WaitForEvents(ctx context.Context, after StreamCursor) error {
	for {
		exists, err := s.streamHasAfter(ctx, after)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Transient (conn lost, server restarting): back off and retry.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pgStreamRetryBackoff):
			}
			continue
		}
		if exists {
			return nil
		}
		if err := s.waitForNotify(ctx, after); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Listen conn failed; loop re-checks and acquires a fresh conn.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pgStreamRetryBackoff):
			}
		}
	}
}

// streamHasAfter reports whether any committed event has stream_pos > after.
func (s *postgresStore) streamHasAfter(ctx context.Context, after StreamCursor) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM events WHERE stream_pos > $1)`, int64(after),
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store.streamHasAfter: %w", err)
	}
	return exists, nil
}

// waitForNotify borrows one pooled connection, drops to the native *pgx.Conn
// (database/sql cannot LISTEN), and blocks in WaitForNotification until a
// notification arrives, s.streamPollInterval() elapses (benign — returns nil
// so the caller re-checks), or ctx is cancelled. To close the race between
// the caller's cursor check and the LISTEN taking effect, it re-checks the
// cursor ON the listening conn after LISTEN — a row committed in that window
// fired its NOTIFY before we were listening and would otherwise strand us a
// full poll interval. The conn is returned to the pool listen-free (UNLISTEN
// *, drained); if that cleanup fails the conn is closed so the pool discards
// rather than recycles a conn with a live subscription.
func (s *postgresStore) waitForNotify(ctx context.Context, after StreamCursor) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store.waitForNotify: acquire conn: %w", err)
	}
	defer conn.Close()

	waitCtx, cancel := context.WithTimeout(ctx, s.streamPollInterval())
	defer cancel()

	return conn.Raw(func(driverConn any) error {
		sc, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("store.waitForNotify: driver conn is %T, not *stdlib.Conn", driverConn)
		}
		pc := sc.Conn()

		if _, err := pc.Exec(ctx, `LISTEN `+pgEventsNotifyChannel); err != nil {
			return fmt.Errorf("store.waitForNotify: listen: %w", err)
		}
		defer func() {
			// Never use ctx here — cleanup must run even when ctx is done.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := pc.Exec(cleanupCtx, `UNLISTEN *`); err != nil {
				_ = pc.Close(cleanupCtx)
			}
		}()

		var exists bool
		if err := pc.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM events WHERE stream_pos > $1)`, int64(after),
		).Scan(&exists); err != nil {
			return fmt.Errorf("store.waitForNotify: recheck: %w", err)
		}
		if exists {
			return nil
		}

		_, err := pc.WaitForNotification(waitCtx)
		if err == nil {
			return nil
		}
		if errors.Is(waitCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil // poll-interval tick, not a failure; caller re-checks
		}
		return fmt.Errorf("store.waitForNotify: wait: %w", err)
	})
}

// streamPollInterval returns the fallback re-check interval; overridable per
// store instance in tests (export_test.go), default pgStreamPollDefault.
func (s *postgresStore) streamPollInterval() time.Duration {
	if s.streamPoll > 0 {
		return s.streamPoll
	}
	return pgStreamPollDefault
}
