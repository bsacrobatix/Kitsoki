package study

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresStore is the PostgreSQL counterpart of SQLiteStore, selected
// explicitly by callers (SQLite remains the default). It persists the whole
// coordination aggregate atomically as one JSON payload per study; the
// immutable event log rides inside that payload, so restart recovery replays
// the exact recorded sequence — runstatus.study.events {since_sequence}
// depends on that ordering staying byte-for-byte identical across backends.
type PostgresStore struct {
	db     *sql.DB
	memory *MemoryStore
}

// NewPostgresStore applies the study schema idempotently (BYTEA payload, no
// SQLite STRICT, dedicated "study" schema per the schema-per-subsystem
// convention) and hydrates the in-memory aggregate from any persisted
// studies, mirroring NewSQLiteStore.
func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, fmt.Errorf("study: nil database")
	}
	if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS study; CREATE TABLE IF NOT EXISTS study.governed_studies (id TEXT PRIMARY KEY, idempotency_key TEXT NOT NULL UNIQUE, payload BYTEA NOT NULL)`); err != nil {
		return nil, fmt.Errorf("study schema: %w", err)
	}
	s := &PostgresStore{db: db, memory: NewMemoryStore()}
	if err := s.load(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *PostgresStore) load(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM study.governed_studies`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return err
		}
		var st state
		if err := json.Unmarshal(b, &st); err != nil {
			return err
		}
		s.memory.byID[st.Snapshot.Study.ID] = st
		s.memory.keys[st.IdempotencyKey] = st.Snapshot.Study.ID
	}
	return rows.Err()
}
func (s *PostgresStore) persist(ctx context.Context, id string) error {
	s.memory.mu.Lock()
	st, ok := s.memory.byID[id]
	s.memory.mu.Unlock()
	if !ok {
		return fmt.Errorf("study %q not found", id)
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO study.governed_studies(id,idempotency_key,payload) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET payload=EXCLUDED.payload`, id, st.IdempotencyKey, b)
	return err
}
func (s *PostgresStore) Submit(ctx context.Context, r SubmitRequest) (Study, bool, error) {
	x, created, err := s.memory.Submit(ctx, r)
	if err != nil {
		return x, created, err
	}
	if created {
		err = s.persist(ctx, x.ID)
	}
	return x, created, err
}
func (s *PostgresStore) Get(c context.Context, id string) (Snapshot, error) {
	return s.memory.Get(c, id)
}
func (s *PostgresStore) List(c context.Context) ([]Study, error) { return s.memory.List(c) }
func (s *PostgresStore) Events(c context.Context, id string, seq int64) ([]Event, error) {
	return s.memory.Events(c, id, seq)
}
func (s *PostgresStore) Retry(c context.Context, id, cell string) (Attempt, error) {
	x, e := s.memory.Retry(c, id, cell)
	if e == nil {
		e = s.persist(c, id)
	}
	return x, e
}
func (s *PostgresStore) Cancel(c context.Context, id, cell string) error {
	e := s.memory.Cancel(c, id, cell)
	if e == nil {
		e = s.persist(c, id)
	}
	return e
}
func (s *PostgresStore) Record(c context.Context, id, cell, attempt string, r Result) error {
	e := s.memory.Record(c, id, cell, attempt, r)
	if e == nil {
		e = s.persist(c, id)
	}
	return e
}
