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

	"kitsoki/internal/ulid"
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
	bundles BundleValidator
	// clockInjected is test-only posture. Production PostgreSQL uses database
	// time so every claimant evaluates lease expiry against one authority.
	clockInjected bool
}
type Option func(*SQLStore)

func WithClock(now func() time.Time) Option {
	return func(s *SQLStore) {
		if now != nil {
			s.now = now
			s.clockInjected = true
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

func WithBundleValidator(validator BundleValidator) Option {
	return func(s *SQLStore) {
		s.bundles = validator
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
	s := &SQLStore{
		db: db, dialect: d, now: time.Now,
		nextID: func() string { return "wq_" + ulid.New() },
	}
	for _, opt := range opts {
		opt(s)
	}
	if d == sqlite {
		db.SetMaxOpenConns(1)
	}
	stmts := []string{}
	if d == postgres {
		stmts = []string{"CREATE SCHEMA IF NOT EXISTS workqueue", `CREATE TABLE IF NOT EXISTS workqueue.jobs (id TEXT PRIMARY KEY, application_id TEXT NOT NULL, queue_name TEXT NOT NULL, idempotency_key TEXT NOT NULL, payload BYTEA NOT NULL, payload_digest TEXT NOT NULL, capabilities JSONB NOT NULL, produces_code INTEGER NOT NULL, state TEXT NOT NULL, priority INTEGER NOT NULL, attempts INTEGER NOT NULL, max_attempts INTEGER NOT NULL, available_at BIGINT NOT NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_expires_at BIGINT, fence BIGINT NOT NULL DEFAULT 0, receipt JSONB, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL, UNIQUE(application_id,queue_name,idempotency_key))`, `CREATE INDEX IF NOT EXISTS workqueue_claim ON workqueue.jobs(application_id,queue_name,state,available_at,priority DESC,created_at)`, `CREATE TABLE IF NOT EXISTS workqueue.capsule_dispatches (work_ref TEXT PRIMARY KEY, run_ref TEXT NOT NULL, execution_ref TEXT NOT NULL DEFAULT '', payload_digest TEXT NOT NULL)`}
	} else {
		stmts = []string{`CREATE TABLE IF NOT EXISTS workqueue_jobs (id TEXT PRIMARY KEY, application_id TEXT NOT NULL, queue_name TEXT NOT NULL, idempotency_key TEXT NOT NULL, payload BLOB NOT NULL, payload_digest TEXT NOT NULL, capabilities TEXT NOT NULL, produces_code INTEGER NOT NULL, state TEXT NOT NULL, priority INTEGER NOT NULL, attempts INTEGER NOT NULL, max_attempts INTEGER NOT NULL, available_at INTEGER NOT NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_expires_at INTEGER, fence INTEGER NOT NULL DEFAULT 0, receipt TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE(application_id,queue_name,idempotency_key)) STRICT`, `CREATE INDEX IF NOT EXISTS workqueue_claim ON workqueue_jobs(application_id,queue_name,state,available_at,priority DESC,created_at)`, `CREATE TABLE IF NOT EXISTS workqueue_capsule_dispatches (work_ref TEXT PRIMARY KEY, run_ref TEXT NOT NULL, execution_ref TEXT NOT NULL DEFAULT '', payload_digest TEXT NOT NULL) STRICT`}
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return nil, fmt.Errorf("workqueue schema: %w", err)
		}
	}
	if d == postgres {
		if _, err := db.Exec(
			`ALTER TABLE workqueue.jobs ADD COLUMN IF NOT EXISTS produces_code INTEGER NOT NULL DEFAULT 0`,
		); err != nil {
			return nil, fmt.Errorf("workqueue compatibility migration: %w", err)
		}
	} else {
		if _, err := db.Exec(
			`ALTER TABLE workqueue_jobs ADD COLUMN produces_code INTEGER NOT NULL DEFAULT 0`,
		); err != nil && !strings.Contains(
			strings.ToLower(err.Error()), "duplicate column",
		) {
			return nil, fmt.Errorf("workqueue compatibility migration: %w", err)
		}
	}
	return s, nil
}

func (s *SQLStore) GetCapsuleDispatch(ctx context.Context, workRef string) (CapsuleDispatch, error) {
	var d CapsuleDispatch
	err := s.db.QueryRowContext(ctx, s.q(`SELECT work_ref,run_ref,execution_ref,payload_digest FROM capsule_dispatches WHERE work_ref=?`), workRef).Scan(&d.WorkRef, &d.RunRef, &d.ExecutionRef, &d.PayloadDigest)
	if err == sql.ErrNoRows {
		return CapsuleDispatch{}, ErrNotFound
	}
	return d, err
}

func (s *SQLStore) PutCapsuleDispatch(ctx context.Context, d CapsuleDispatch) error {
	if strings.TrimSpace(d.WorkRef) == "" || strings.TrimSpace(d.RunRef) == "" || strings.TrimSpace(d.PayloadDigest) == "" {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO capsule_dispatches (work_ref,run_ref,execution_ref,payload_digest) VALUES (?,?,?,?) ON CONFLICT(work_ref) DO UPDATE SET run_ref=excluded.run_ref,execution_ref=excluded.execution_ref,payload_digest=excluded.payload_digest`), d.WorkRef, d.RunRef, d.ExecutionRef, d.PayloadDigest)
	return err
}

func (s *SQLStore) DeleteCapsuleDispatch(ctx context.Context, workRef string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM capsule_dispatches WHERE work_ref=?`), workRef)
	return err
}

func (s *SQLStore) Enqueue(ctx context.Context, r EnqueueRequest) (Job, error) {
	r.ApplicationID, r.Queue, r.IdempotencyKey = strings.TrimSpace(r.ApplicationID), strings.TrimSpace(r.Queue), strings.TrimSpace(r.IdempotencyKey)
	if r.ApplicationID == "" || r.Queue == "" || r.IdempotencyKey == "" || r.MaxAttempts < 0 {
		return Job{}, ErrInvalid
	}
	if r.MaxAttempts == 0 {
		r.MaxAttempts = 1
	}
	now, err := s.currentTime(ctx, s.db)
	if err != nil {
		return Job{}, err
	}
	availableAtExplicit := !r.AvailableAt.IsZero()
	if !availableAtExplicit {
		r.AvailableAt = now
	}
	caps := normalized(r.RequiredCapabilities)
	if r.Payload == nil {
		r.Payload = []byte{}
	}
	rawCaps, _ := json.Marshal(caps)
	sum := sha256.Sum256(r.Payload)
	digest := hex.EncodeToString(sum[:])
	payload := make([]byte, len(r.Payload))
	copy(payload, r.Payload)
	j := Job{ID: s.nextID(), ApplicationID: r.ApplicationID, Queue: r.Queue, IdempotencyKey: r.IdempotencyKey, Payload: payload, PayloadDigest: []byte(digest), RequiredCapabilities: caps, ProducesCode: r.ProducesCode, State: StateQueued, Priority: r.Priority, MaxAttempts: r.MaxAttempts, AvailableAt: r.AvailableAt.UTC(), CreatedAt: now, UpdatedAt: now}
	q := s.q(`INSERT INTO jobs (id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,produces_code,state,priority,attempts,max_attempts,available_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(application_id,queue_name,idempotency_key) DO NOTHING`)
	res, err := s.db.ExecContext(ctx, q, j.ID, j.ApplicationID, j.Queue, j.IdempotencyKey, j.Payload, digest, string(rawCaps), boolInt(r.ProducesCode), string(j.State), j.Priority, 0, j.MaxAttempts, j.AvailableAt.UnixMilli(), now.UnixMilli(), now.UnixMilli())
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
		existing.ProducesCode != r.ProducesCode || existing.Priority != r.Priority ||
		existing.MaxAttempts != r.MaxAttempts ||
		(availableAtExplicit && !existing.AvailableAt.Equal(r.AvailableAt.UTC())) {
		return Job{}, ErrConflict
	}
	existing.Replayed = true
	return existing, nil
}

func (s *SQLStore) ClaimNext(ctx context.Context, r ClaimRequest) (*Lease, error) {
	r.ApplicationID = strings.TrimSpace(r.ApplicationID)
	r.Queue = strings.TrimSpace(r.Queue)
	r.WorkerID = strings.TrimSpace(r.WorkerID)
	if r.ApplicationID == "" || r.Queue == "" || r.WorkerID == "" {
		return nil, ErrInvalid
	}
	if r.AvailableCapacity <= 0 {
		return nil, nil
	}
	if r.LeaseDuration <= 0 {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now, err := s.currentTime(ctx, tx)
	if err != nil {
		return nil, err
	}
	until := now.Add(r.LeaseDuration)
	if s.dialect == postgres {
		// Capacity is per worker, so serialize the count-and-claim critical
		// section for one app/queue/worker without blocking unrelated workers.
		lockKey := fmt.Sprintf(
			"%d:%s%d:%s%d:%s",
			len(r.ApplicationID), r.ApplicationID,
			len(r.Queue), r.Queue,
			len(r.WorkerID), r.WorkerID,
		)
		if _, err := tx.ExecContext(
			ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			lockKey,
		); err != nil {
			return nil, fmt.Errorf("workqueue claim capacity lock: %w", err)
		}
	}
	if err := s.reconcileExpired(ctx, tx, r.ApplicationID, r.Queue, now); err != nil {
		return nil, err
	}
	var leased int
	err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM jobs WHERE application_id=? AND queue_name=? AND state='leased' AND lease_owner=? AND lease_expires_at>?`), r.ApplicationID, r.Queue, r.WorkerID, now.UnixMilli()).Scan(&leased)
	if err != nil {
		return nil, err
	}
	if leased >= r.AvailableCapacity {
		return nil, tx.Commit()
	}
	capJSON, _ := json.Marshal(normalized(r.Capabilities))
	query := `SELECT id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,produces_code,state,priority,attempts,max_attempts,available_at,lease_owner,lease_expires_at,fence,receipt,created_at,updated_at FROM jobs WHERE application_id=? AND queue_name=? AND state='queued' AND available_at<=?`
	if s.dialect == postgres {
		query += ` AND capabilities <@ ?::jsonb ORDER BY priority DESC,available_at,created_at LIMIT 1 FOR UPDATE SKIP LOCKED`
	} else {
		query += ` AND NOT EXISTS (SELECT 1 FROM json_each(capabilities) need WHERE NOT EXISTS (SELECT 1 FROM json_each(?) have WHERE have.value=need.value)) ORDER BY priority DESC,available_at,created_at LIMIT 1`
	}
	rows, err := tx.QueryContext(ctx, s.q(query), r.ApplicationID, r.Queue, now.UnixMilli(), string(capJSON))
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
		res, err := tx.ExecContext(ctx, s.q(`UPDATE jobs SET state=?,attempts=?,lease_owner=?,lease_expires_at=?,fence=?,updated_at=? WHERE id=? AND fence=? AND state='queued' AND available_at<=?`), j.State, j.Attempts, j.LeaseOwner, until.UnixMilli(), j.Fence, now.UnixMilli(), j.ID, j.Fence-1, now.UnixMilli())
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
	now, err := s.currentTime(ctx, s.db)
	if err != nil {
		return Job{}, err
	}
	res, err := s.db.ExecContext(
		ctx,
		s.q(`UPDATE jobs SET lease_expires_at=?,updated_at=? WHERE id=? AND state='leased' AND lease_owner=? AND fence=? AND lease_expires_at>?`),
		now.Add(d).UnixMilli(), now.UnixMilli(), id, owner, fence, now.UnixMilli(),
	)
	if err != nil {
		return Job{}, err
	}
	return s.afterCAS(ctx, res, id)
}
func (s *SQLStore) Complete(ctx context.Context, id, owner string, fence int64, r Receipt) (Job, error) {
	j, err := s.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if j.State != StateLeased || j.LeaseOwner != owner || j.Fence != fence {
		return Job{}, ErrStaleLease
	}
	if r.Schema != ReceiptSchema || r.Outcome == "" || r.JobID != id ||
		r.Fence != fence || r.Attempt != j.Attempts {
		return Job{}, ErrInvalid
	}
	if j.ProducesCode &&
		(strings.TrimSpace(r.BundleRef) == "" ||
			!validSHA256Digest(r.BundleDigest)) {
		return Job{}, ErrInvalid
	}
	if j.ProducesCode {
		if s.bundles == nil {
			return Job{}, ErrInvalid
		}
		canonicalRef, validateErr := s.bundles.ValidateBundle(
			ctx, r.BundleRef, r.BundleDigest,
		)
		if validateErr != nil || strings.TrimSpace(canonicalRef) == "" {
			return Job{}, ErrInvalid
		}
		r.BundleRef = canonicalRef
	}
	now, err := s.currentTime(ctx, s.db)
	if err != nil {
		return Job{}, err
	}
	r.ApplicationID = j.ApplicationID
	r.Queue = j.Queue
	r.WorkerID = owner
	r.FinishedAt = now
	r.RequiredCapabilities = append([]string(nil), j.RequiredCapabilities...)
	// Countability is deployment policy, never a worker-selected outcome.
	r.Countable = j.ProducesCode
	raw, _ := json.Marshal(r)
	res, err := s.db.ExecContext(
		ctx,
		s.q(`UPDATE jobs SET state='succeeded',receipt=?,lease_owner='',lease_expires_at=NULL,updated_at=? WHERE id=? AND state='leased' AND lease_owner=? AND fence=? AND attempts=? AND lease_expires_at>?`),
		string(raw), now.UnixMilli(), id, owner, fence, j.Attempts, now.UnixMilli(),
	)
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
	now, err := s.currentTime(ctx, s.db)
	if err != nil {
		return Job{}, err
	}
	if j.LeaseExpiresAt == nil || !j.LeaseExpiresAt.After(now) {
		return Job{}, ErrStaleLease
	}
	state := StateFailed
	available := now
	if f.Retryable && j.Attempts < j.MaxAttempts {
		state = StateQueued
		available = now.Add(backoff(j.Attempts))
	} else if f.Receipt.Schema != ReceiptSchema || f.Receipt.Outcome == "" ||
		f.Receipt.JobID != id || f.Receipt.Fence != fence ||
		f.Receipt.Attempt != j.Attempts {
		return Job{}, ErrInvalid
	}
	var receipt any
	if state == StateFailed {
		f.Receipt.ApplicationID = j.ApplicationID
		f.Receipt.Queue = j.Queue
		f.Receipt.WorkerID = owner
		f.Receipt.Reason = strings.TrimSpace(f.Message)
		f.Receipt.FinishedAt = now
		f.Receipt.RequiredCapabilities = append(
			[]string(nil), j.RequiredCapabilities...,
		)
		f.Receipt.Countable = false
		raw, _ := json.Marshal(f.Receipt)
		receipt = string(raw)
	}
	res, err := s.db.ExecContext(
		ctx,
		s.q(`UPDATE jobs SET state=?,available_at=?,receipt=?,lease_owner='',lease_expires_at=NULL,updated_at=? WHERE id=? AND state='leased' AND lease_owner=? AND fence=? AND attempts=? AND lease_expires_at>?`),
		state, available.UnixMilli(), receipt, now.UnixMilli(), id, owner, fence,
		j.Attempts, now.UnixMilli(),
	)
	if err != nil {
		return Job{}, err
	}
	return s.afterCAS(ctx, res, id)
}
func (s *SQLStore) Cancel(ctx context.Context, id string, receipt Receipt) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, s.q(`SELECT id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,produces_code,state,priority,attempts,max_attempts,available_at,lease_owner,lease_expires_at,fence,receipt,created_at,updated_at FROM jobs WHERE id=?`), id)
	j, err := scan(row)
	if err == sql.ErrNoRows {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if receipt.Schema != ReceiptSchema || receipt.Outcome == "" || receipt.JobID != id || receipt.Attempt != j.Attempts || receipt.Fence != j.Fence {
		return Job{}, ErrInvalid
	}
	receipt.ApplicationID = j.ApplicationID
	receipt.Queue = j.Queue
	now, err := s.currentTime(ctx, tx)
	if err != nil {
		return Job{}, err
	}
	receipt.FinishedAt = now
	receipt.RequiredCapabilities = append([]string(nil), j.RequiredCapabilities...)
	receipt.Countable = false
	raw, _ := json.Marshal(receipt)
	res, err := tx.ExecContext(ctx, s.q(`UPDATE jobs SET state='cancelled',receipt=?,lease_owner='',lease_expires_at=NULL,updated_at=? WHERE id=? AND state IN ('queued','leased') AND attempts=? AND fence=?`), string(raw), now.UnixMilli(), id, j.Attempts, j.Fence)
	if err != nil {
		return Job{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Job{}, err
	}
	if n != 1 {
		return Job{}, ErrStaleLease
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, id)
}
func (s *SQLStore) afterCAS(ctx context.Context, res sql.Result, id string) (Job, error) {
	n, _ := res.RowsAffected()
	if n != 1 {
		return Job{}, ErrStaleLease
	}
	return s.Get(ctx, id)
}
func (s *SQLStore) Get(ctx context.Context, id string) (Job, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,produces_code,state,priority,attempts,max_attempts,available_at,lease_owner,lease_expires_at,fence,receipt,created_at,updated_at FROM jobs WHERE id=?`), id)
	j, err := scan(row)
	if err == sql.ErrNoRows {
		return Job{}, ErrNotFound
	}
	return j, err
}
func (s *SQLStore) getByKey(ctx context.Context, a, q, k string) (Job, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,produces_code,state,priority,attempts,max_attempts,available_at,lease_owner,lease_expires_at,fence,receipt,created_at,updated_at FROM jobs WHERE application_id=? AND queue_name=? AND idempotency_key=?`), a, q, k)
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
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,produces_code,state,priority,attempts,max_attempts,available_at,lease_owner,lease_expires_at,fence,receipt,created_at,updated_at FROM jobs WHERE application_id=? AND queue_name=? ORDER BY created_at LIMIT ?`), f.ApplicationID, f.Queue, limit)
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
	var producesCode int
	var avail, created, updated int64
	err := r.Scan(&j.ID, &j.ApplicationID, &j.Queue, &j.IdempotencyKey, &j.Payload, &j.PayloadDigest, &caps, &producesCode, &j.State, &j.Priority, &j.Attempts, &j.MaxAttempts, &avail, &j.LeaseOwner, &lease, &j.Fence, &receipt, &created, &updated)
	if err != nil {
		return j, err
	}
	json.Unmarshal([]byte(caps.String), &j.RequiredCapabilities)
	j.ProducesCode = producesCode != 0
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
		q = strings.ReplaceAll(q, "capsule_dispatches", "workqueue_capsule_dispatches")
		return strings.ReplaceAll(q, "jobs", "workqueue_jobs")
	}
	q = strings.ReplaceAll(q, "capsule_dispatches", "workqueue.capsule_dispatches")
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

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validSHA256Digest(value string) bool {
	value = strings.TrimSpace(value)
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(raw) == sha256.Size
}

func (s *SQLStore) reconcileExpired(
	ctx context.Context,
	tx *sql.Tx,
	applicationID, queue string,
	now time.Time,
) error {
	query := `SELECT id,application_id,queue_name,idempotency_key,payload,payload_digest,capabilities,produces_code,state,priority,attempts,max_attempts,available_at,lease_owner,lease_expires_at,fence,receipt,created_at,updated_at FROM jobs WHERE application_id=? AND queue_name=? AND state='leased' AND lease_expires_at<=? ORDER BY lease_expires_at,id LIMIT 100`
	if s.dialect == postgres {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	rows, err := tx.QueryContext(
		ctx, s.q(query), applicationID, queue, now.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("workqueue reconcile expired leases: %w", err)
	}
	var expired []Job
	for rows.Next() {
		job, scanErr := scan(rows)
		if scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("workqueue reconcile expired lease: %w", scanErr)
		}
		expired = append(expired, job)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, job := range expired {
		if job.Attempts >= job.MaxAttempts {
			receipt := Receipt{
				Schema: ReceiptSchema, Outcome: "failed",
				Reason: "lease_expired_max_attempts",
				JobID:  job.ID, ApplicationID: job.ApplicationID,
				Queue: job.Queue, WorkerID: job.LeaseOwner,
				Attempt: job.Attempts, Fence: job.Fence, FinishedAt: now,
				RequiredCapabilities: append(
					[]string(nil), job.RequiredCapabilities...,
				),
			}
			raw, _ := json.Marshal(receipt)
			if _, err := tx.ExecContext(
				ctx,
				s.q(`UPDATE jobs SET state='failed',receipt=?,lease_owner='',lease_expires_at=NULL,updated_at=? WHERE id=? AND state='leased' AND attempts=? AND fence=? AND lease_expires_at<=?`),
				string(raw), now.UnixMilli(), job.ID, job.Attempts, job.Fence,
				now.UnixMilli(),
			); err != nil {
				return fmt.Errorf("workqueue exhaust expired lease: %w", err)
			}
			continue
		}
		availableAt := now.Add(backoff(job.Attempts))
		if _, err := tx.ExecContext(
			ctx,
			s.q(`UPDATE jobs SET state='queued',available_at=?,lease_owner='',lease_expires_at=NULL,updated_at=? WHERE id=? AND state='leased' AND attempts=? AND fence=? AND lease_expires_at<=?`),
			availableAt.UnixMilli(), now.UnixMilli(), job.ID, job.Attempts,
			job.Fence, now.UnixMilli(),
		); err != nil {
			return fmt.Errorf("workqueue retry expired lease: %w", err)
		}
	}
	return nil
}

type timeQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *SQLStore) currentTime(ctx context.Context, queryer timeQueryer) (time.Time, error) {
	if s.dialect != postgres || s.clockInjected {
		return s.now().UTC(), nil
	}
	var millis int64
	if err := queryer.QueryRowContext(
		ctx,
		`SELECT FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT`,
	).Scan(&millis); err != nil {
		return time.Time{}, fmt.Errorf("workqueue database clock: %w", err)
	}
	return time.UnixMilli(millis).UTC(), nil
}
