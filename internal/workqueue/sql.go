package workqueue

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type dialect uint8

const (
	sqlite dialect = iota
	postgres
)

type SQLStore struct {
	db      *sql.DB
	dialect dialect
	now     func() time.Time
	nextID  func() string
}
type Option func(*SQLStore)

func WithClock(now func() time.Time) Option {
	return func(s *SQLStore) {
		if now != nil {
			s.now = now
		}
	}
}
func WithIDGenerator(next func() string) Option {
	return func(s *SQLStore) {
		if next != nil {
			s.nextID = next
		}
	}
}

func NewSQLiteStore(db *sql.DB, opts ...Option) (*SQLStore, error) {
	return newStore(db, sqlite, opts...)
}
func NewPostgresStore(db *sql.DB, opts ...Option) (*SQLStore, error) {
	return newStore(db, postgres, opts...)
}
func newStore(db *sql.DB, d dialect, opts ...Option) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("workqueue: nil database")
	}
	s := &SQLStore{db: db, dialect: d, now: time.Now, nextID: func() string { return fmt.Sprintf("wq-%d", time.Now().UnixNano()) }}
	for _, opt := range opts {
		opt(s)
	}
	if d == sqlite {
		db.SetMaxOpenConns(1)
	}
	stmts := []string{}
	if d == postgres {
		stmts = []string{"CREATE SCHEMA IF NOT EXISTS workqueue", `CREATE TABLE IF NOT EXISTS workqueue.jobs (id TEXT PRIMARY KEY, application_id TEXT NOT NULL, queue_name TEXT NOT NULL, idempotency_key TEXT NOT NULL, payload BYTEA NOT NULL, payload_digest TEXT NOT NULL, capabilities JSONB NOT NULL, state TEXT NOT NULL, priority INTEGER NOT NULL, attempts INTEGER NOT NULL, max_attempts INTEGER NOT NULL, available_at BIGINT NOT NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_expires_at BIGINT, fence BIGINT NOT NULL DEFAULT 0, receipt JSONB, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL, UNIQUE(application_id,queue_name,idempotency_key))`, `CREATE INDEX IF NOT EXISTS workqueue_claim ON workqueue.jobs(application_id,queue_name,state,available_at,priority DESC,created_at)`}
	} else {
		stmts = []string{`CREATE TABLE IF NOT EXISTS workqueue_jobs (id TEXT PRIMARY KEY, application_id TEXT NOT NULL, queue_name TEXT NOT NULL, idempotency_key TEXT NOT NULL, payload BLOB NOT NULL, payload_digest TEXT NOT NULL, capabilities TEXT NOT NULL, state TEXT NOT NULL, priority INTEGER NOT NULL, attempts INTEGER NOT NULL, max_attempts INTEGER NOT NULL, available_at INTEGER NOT NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_expires_at INTEGER, fence INTEGER NOT NULL DEFAULT 0, receipt TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE(application_id,queue_name,idempotency_key)) STRICT`, `CREATE INDEX IF NOT EXISTS workqueue_claim ON workqueue_jobs(application_id,queue_name,state,available_at,priority DESC,created_at)`}
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return nil, fmt.Errorf("workqueue schema: %w", err)
		}
	}
	return s, nil
}

func (s *SQLStore) Enqueue(ctx context.Context, r EnqueueRequest) (Job, error) {
	r.ApplicationID, r.Queue, r.IdempotencyKey = strings.TrimSpace(r.ApplicationID), strings.TrimSpace(r.Queue), strings.TrimSpace(r.IdempotencyKey)
	if r.ApplicationID == "" || r.Queue == "" || r.IdempotencyKey == "" || r.MaxAttempts < 0 {
		return Job{}, ErrInvalid
	}
	if r.MaxAttempts == 0 {
		r.MaxAttempts = 1
	}
	if r.AvailableAt.IsZero() {
		r.AvailableAt = s.now()
	}
	caps := normalized(r.RequiredCapabilities)
	if r.Payload == nil {
		r.Payload = []byte{}
	}
	rawCaps, _ := json.Marshal(caps)
	sum := sha256.Sum256(r.Payload)
	digest := hex.EncodeToString(sum[:])
	now := s.now().UTC()
	payload := make([]byte, len(r.Payload))
	copy(payload, r.Payload)
	j := Job{ID: s.nextID(), ApplicationID: r.ApplicationID, Queue: r.Queue, IdempotencyKey: r.IdempotencyKey, Payload: payload, PayloadDigest: []byte(digest), RequiredCapabilities: caps, State: StateQueued, Priority: r.Priority, MaxAttempts: r.MaxAttempts, AvailableAt: r.AvailableAt.UTC(), CreatedAt: now, UpdatedAt: now}
	q := s.q(`INSERT INTO jobs (id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,state,priority,attempts,max_attempts,available_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(application_id,queue_name,idempotency_key) DO NOTHING`)
	res, err := s.db.ExecContext(ctx, q, j.ID, j.ApplicationID, j.Queue, j.IdempotencyKey, j.Payload, digest, string(rawCaps), string(j.State), j.Priority, 0, j.MaxAttempts, j.AvailableAt.UnixMilli(), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return Job{}, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return j, nil
	}
	existing, err := s.getByKey(ctx, r.ApplicationID, r.Queue, r.IdempotencyKey)
	if err != nil {
		return Job{}, err
	}
	if string(existing.PayloadDigest) != digest || !sameStrings(existing.RequiredCapabilities, caps) ||
		existing.Priority != r.Priority || existing.MaxAttempts != r.MaxAttempts || !existing.AvailableAt.Equal(r.AvailableAt.UTC()) {
		return Job{}, ErrConflict
	}
	return existing, nil
}

func (s *SQLStore) ClaimNext(ctx context.Context, r ClaimRequest) (*Lease, error) {
	if r.AvailableCapacity <= 0 {
		return nil, nil
	}
	if r.LeaseDuration <= 0 {
		return nil, ErrInvalid
	}
	now := s.now().UTC()
	until := now.Add(r.LeaseDuration)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var leased int
	err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM jobs WHERE application_id=? AND queue_name=? AND state='leased' AND lease_owner=? AND lease_expires_at>?`), r.ApplicationID, r.Queue, r.WorkerID, now.UnixMilli()).Scan(&leased)
	if err != nil {
		return nil, err
	}
	if leased >= r.AvailableCapacity {
		return nil, tx.Commit()
	}
	capJSON, _ := json.Marshal(normalized(r.Capabilities))
	query := `SELECT id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,state,priority,attempts,max_attempts,available_at,lease_owner,lease_expires_at,fence,receipt,created_at,updated_at FROM jobs WHERE application_id=? AND queue_name=? AND ((state='queued' AND available_at<=?) OR (state='leased' AND lease_expires_at<=?))`
	if s.dialect == postgres {
		query += ` AND capabilities <@ ?::jsonb ORDER BY priority DESC,available_at,created_at LIMIT 1 FOR UPDATE SKIP LOCKED`
	} else {
		query += ` AND NOT EXISTS (SELECT 1 FROM json_each(capabilities) need WHERE NOT EXISTS (SELECT 1 FROM json_each(?) have WHERE have.value=need.value)) ORDER BY priority DESC,available_at,created_at LIMIT 1`
	}
	rows, err := tx.QueryContext(ctx, s.q(query), r.ApplicationID, r.Queue, now.UnixMilli(), now.UnixMilli(), string(capJSON))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		j, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		j.State = StateLeased
		j.Attempts++
		j.LeaseOwner = r.WorkerID
		j.LeaseExpiresAt = &until
		j.Fence++
		j.UpdatedAt = now
		res, err := tx.ExecContext(ctx, s.q(`UPDATE jobs SET state=?,attempts=?,lease_owner=?,lease_expires_at=?,fence=?,updated_at=? WHERE id=? AND fence=? AND (state='queued' OR (state='leased' AND lease_expires_at<=?))`), j.State, j.Attempts, j.LeaseOwner, until.UnixMilli(), j.Fence, now.UnixMilli(), j.ID, j.Fence-1, now.UnixMilli())
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			continue
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &Lease{Job: j}, nil
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *SQLStore) Heartbeat(ctx context.Context, id, owner string, fence int64, d time.Duration) (Job, error) {
	if d <= 0 {
		return Job{}, ErrInvalid
	}
	now := s.now().UTC()
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE jobs SET lease_expires_at=?,updated_at=? WHERE id=? AND state='leased' AND lease_owner=? AND fence=?`), now.Add(d).UnixMilli(), now.UnixMilli(), id, owner, fence)
	if err != nil {
		return Job{}, err
	}
	return s.afterCAS(ctx, res, id)
}
func (s *SQLStore) Complete(ctx context.Context, id, owner string, fence int64, r Receipt) (Job, error) {
	if r.Schema != ReceiptSchema || r.Outcome == "" || r.JobID != id || r.Fence != fence || r.Attempt < 1 {
		return Job{}, ErrInvalid
	}
	if r.Outcome == "shipped" && (r.BundleRef == "" || r.BundleDigest == "") {
		return Job{}, ErrInvalid
	}
	raw, _ := json.Marshal(r)
	now := s.now().UTC()
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE jobs SET state='succeeded',receipt=?,lease_owner='',lease_expires_at=NULL,updated_at=? WHERE id=? AND state='leased' AND lease_owner=? AND fence=?`), string(raw), now.UnixMilli(), id, owner, fence)
	if err != nil {
		return Job{}, err
	}
	return s.afterCAS(ctx, res, id)
}
func (s *SQLStore) Fail(ctx context.Context, id, owner string, fence int64, f Failure) (Job, error) {
	j, err := s.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if j.State != StateLeased || j.LeaseOwner != owner || j.Fence != fence {
		return Job{}, ErrStaleLease
	}
	now := s.now().UTC()
	state := StateFailed
	available := now
	if f.Retryable && j.Attempts < j.MaxAttempts {
		state = StateQueued
		available = now.Add(backoff(j.Attempts))
	} else if f.Receipt.Schema != ReceiptSchema || f.Receipt.Outcome == "" || f.Receipt.JobID != id || f.Receipt.Fence != fence || f.Receipt.Attempt != j.Attempts {
		return Job{}, ErrInvalid
	}
	var receipt any
	if state == StateFailed {
		raw, _ := json.Marshal(f.Receipt)
		receipt = string(raw)
	}
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE jobs SET state=?,available_at=?,receipt=?,lease_owner='',lease_expires_at=NULL,updated_at=? WHERE id=? AND state='leased' AND lease_owner=? AND fence=?`), state, available.UnixMilli(), receipt, now.UnixMilli(), id, owner, fence)
	if err != nil {
		return Job{}, err
	}
	return s.afterCAS(ctx, res, id)
}
func (s *SQLStore) Cancel(ctx context.Context, id string, receipt Receipt) (Job, error) {
	j, err := s.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if receipt.Schema != ReceiptSchema || receipt.Outcome == "" || receipt.JobID != id || receipt.Attempt != j.Attempts || receipt.Fence != j.Fence {
		return Job{}, ErrInvalid
	}
	raw, _ := json.Marshal(receipt)
	now := s.now().UTC()
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE jobs SET state='cancelled',receipt=?,lease_owner='',lease_expires_at=NULL,updated_at=? WHERE id=? AND state IN ('queued','leased')`), string(raw), now.UnixMilli(), id)
	if err != nil {
		return Job{}, err
	}
	return s.afterCAS(ctx, res, id)
}
func (s *SQLStore) afterCAS(ctx context.Context, res sql.Result, id string) (Job, error) {
	n, _ := res.RowsAffected()
	if n != 1 {
		return Job{}, ErrStaleLease
	}
	return s.Get(ctx, id)
}
func (s *SQLStore) Get(ctx context.Context, id string) (Job, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,state,priority,attempts,max_attempts,available_at,lease_owner,lease_expires_at,fence,receipt,created_at,updated_at FROM jobs WHERE id=?`), id)
	j, err := scan(row)
	if err == sql.ErrNoRows {
		return Job{}, ErrNotFound
	}
	return j, err
}
func (s *SQLStore) getByKey(ctx context.Context, a, q, k string) (Job, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,state,priority,attempts,max_attempts,available_at,lease_owner,lease_expires_at,fence,receipt,created_at,updated_at FROM jobs WHERE application_id=? AND queue_name=? AND idempotency_key=?`), a, q, k)
	j, err := scan(row)
	if err == sql.ErrNoRows {
		return Job{}, ErrNotFound
	}
	return j, err
}
func (s *SQLStore) List(ctx context.Context, f ListFilter) ([]Job, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,state,priority,attempts,max_attempts,available_at,lease_owner,lease_expires_at,fence,receipt,created_at,updated_at FROM jobs WHERE application_id=? AND queue_name=? ORDER BY created_at LIMIT ?`), f.ApplicationID, f.Queue, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	wanted := setStates(f.States)
	for rows.Next() {
		j, e := scan(rows)
		if e != nil {
			return nil, e
		}
		if len(wanted) == 0 || wanted[j.State] {
			out = append(out, j)
		}
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scan(r scanner) (Job, error) {
	var j Job
	var caps, receipt sql.NullString
	var lease sql.NullInt64
	var avail, created, updated int64
	err := r.Scan(&j.ID, &j.ApplicationID, &j.Queue, &j.IdempotencyKey, &j.Payload, &j.PayloadDigest, &caps, &j.State, &j.Priority, &j.Attempts, &j.MaxAttempts, &avail, &j.LeaseOwner, &lease, &j.Fence, &receipt, &created, &updated)
	if err != nil {
		return j, err
	}
	json.Unmarshal([]byte(caps.String), &j.RequiredCapabilities)
	j.AvailableAt = time.UnixMilli(avail).UTC()
	j.CreatedAt = time.UnixMilli(created).UTC()
	j.UpdatedAt = time.UnixMilli(updated).UTC()
	if lease.Valid {
		t := time.UnixMilli(lease.Int64).UTC()
		j.LeaseExpiresAt = &t
	}
	if receipt.Valid && receipt.String != "" {
		var x Receipt
		if json.Unmarshal([]byte(receipt.String), &x) == nil {
			j.Receipt = &x
		}
	}
	return j, nil
}
func (s *SQLStore) q(q string) string {
	if s.dialect == sqlite {
		return strings.ReplaceAll(q, "jobs", "workqueue_jobs")
	}
	q = strings.ReplaceAll(q, "jobs", "workqueue.jobs")
	var b strings.Builder
	n := 0
	for _, c := range q {
		if c == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}
func normalized(in []string) []string {
	m := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" {
			m[v] = true
		}
	}
	o := make([]string, 0, len(m))
	for v := range m {
		o = append(o, v)
	}
	sort.Strings(o)
	return o
}
func set(in []string) map[string]bool {
	m := map[string]bool{}
	for _, v := range in {
		m[v] = true
	}
	return m
}
func matches(need []string, have map[string]bool) bool {
	for _, v := range need {
		if !have[v] {
			return false
		}
	}
	return true
}
func setStates(in []State) map[State]bool {
	m := map[State]bool{}
	for _, v := range in {
		m[v] = true
	}
	return m
}
func sameStrings(a, b []string) bool {
	return len(a) == len(b) && strings.Join(a, "\x00") == strings.Join(b, "\x00")
}
func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	return time.Second * time.Duration(1<<(attempt-1))
}
