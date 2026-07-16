package study

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// SQLiteStore persists the whole coordination aggregate atomically. Keeping
// the immutable event log in the same record prevents restart recovery from
// reconstructing an inferred sequence from scheduler state.
type SQLiteStore struct {
	db     *sql.DB
	memory *MemoryStore
}

func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("study: nil database")
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS governed_studies (id TEXT PRIMARY KEY, idempotency_key TEXT NOT NULL UNIQUE, payload BLOB NOT NULL) STRICT`); err != nil {
		return nil, fmt.Errorf("study schema: %w", err)
	}
	s := &SQLiteStore{db: db, memory: NewMemoryStore()}
	if err := s.load(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *SQLiteStore) load(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM governed_studies`)
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
func (s *SQLiteStore) persist(ctx context.Context, id string) error {
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO governed_studies(id,idempotency_key,payload) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, id, st.IdempotencyKey, b)
	return err
}
func (s *SQLiteStore) Submit(ctx context.Context, r SubmitRequest) (Study, bool, error) {
	x, created, err := s.memory.Submit(ctx, r)
	if err != nil {
		return x, created, err
	}
	if created {
		err = s.persist(ctx, x.ID)
	}
	return x, created, err
}
func (s *SQLiteStore) Get(c context.Context, id string) (Snapshot, error) { return s.memory.Get(c, id) }
func (s *SQLiteStore) List(c context.Context) ([]Study, error)            { return s.memory.List(c) }
func (s *SQLiteStore) Events(c context.Context, id string, seq int64) ([]Event, error) {
	return s.memory.Events(c, id, seq)
}
func (s *SQLiteStore) Retry(c context.Context, id, cell string) (Attempt, error) {
	x, e := s.memory.Retry(c, id, cell)
	if e == nil {
		e = s.persist(c, id)
	}
	return x, e
}
func (s *SQLiteStore) Cancel(c context.Context, id, cell string) error {
	e := s.memory.Cancel(c, id, cell)
	if e == nil {
		e = s.persist(c, id)
	}
	return e
}
func (s *SQLiteStore) Record(c context.Context, id, cell, attempt string, r Result) error {
	e := s.memory.Record(c, id, cell, attempt, r)
	if e == nil {
		e = s.persist(c, id)
	}
	return e
}
