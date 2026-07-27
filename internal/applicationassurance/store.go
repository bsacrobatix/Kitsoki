// Package applicationassurance provides the configured Story Application
// wrappers and durable stores for deterministic assurance operations.
package applicationassurance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"kitsoki/internal/host"
)

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS story_application_assurance_evidence (
  kind        TEXT NOT NULL,
  digest      TEXT NOT NULL,
  ref         TEXT NOT NULL,
  payload     TEXT NOT NULL,
  PRIMARY KEY (kind, digest)
) STRICT;
`

const postgresSchema = `
CREATE SCHEMA IF NOT EXISTS applicationassurance;

CREATE TABLE IF NOT EXISTS applicationassurance.story_application_assurance_evidence (
  kind        TEXT NOT NULL,
  digest      TEXT NOT NULL,
  ref         TEXT NOT NULL,
  payload     TEXT NOT NULL,
  PRIMARY KEY (kind, digest)
);
`

type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

// SQLStore retains immutable compliance and flow-evidence records in the
// active session database.
type SQLStore struct {
	db      *sql.DB
	dialect dialect
}

func NewSQLiteStore(db *sql.DB) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("applicationassurance.NewSQLiteStore: nil db")
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		return nil, fmt.Errorf("applicationassurance.NewSQLiteStore: schema migration: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func NewPostgresStore(db *sql.DB) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("applicationassurance.NewPostgresStore: nil db")
	}
	if _, err := db.Exec(postgresSchema); err != nil {
		return nil, fmt.Errorf("applicationassurance.NewPostgresStore: schema migration: %w", err)
	}
	return &SQLStore{db: db, dialect: dialectPostgres}, nil
}

// ComplianceStore adapts SQLStore to compliance.EvidenceStore without
// exposing database details to that typed service.
type ComplianceStore struct {
	Store *SQLStore
}

func (s ComplianceStore) Get(ctx context.Context, digest string) (string, bool, error) {
	if s.Store == nil {
		return "", false, errors.New("application assurance compliance store is unavailable")
	}
	ref, _, ok, err := s.Store.get(ctx, "compliance", digest)
	return ref, ok, err
}

func (s ComplianceStore) Put(ctx context.Context, digest string, raw []byte) (string, error) {
	if s.Store == nil {
		return "", errors.New("application assurance compliance store is unavailable")
	}
	ref := "kitsoki://compliance/sha256/" + digest
	storedRef, _, err := s.Store.putIfAbsent(ctx, "compliance", digest, ref, raw)
	return storedRef, err
}

// FlowEvidenceStore adapts SQLStore to host.FlowEvidenceStore.
type FlowEvidenceStore struct {
	Store *SQLStore
}

func (s FlowEvidenceStore) LookupFlowEvidence(
	ctx context.Context,
	key string,
) (host.FlowEvidenceRecord, bool, error) {
	if s.Store == nil {
		return host.FlowEvidenceRecord{}, false, errors.New("application assurance flow store is unavailable")
	}
	_, raw, ok, err := s.Store.get(ctx, "flow_evidence", key)
	if err != nil || !ok {
		return host.FlowEvidenceRecord{}, ok, err
	}
	var record host.FlowEvidenceRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return host.FlowEvidenceRecord{}, false, fmt.Errorf("decode flow evidence: %w", err)
	}
	return record, true, nil
}

func (s FlowEvidenceStore) PutFlowEvidenceIfAbsent(
	ctx context.Context,
	key string,
	record host.FlowEvidenceRecord,
) (host.FlowEvidenceRecord, error) {
	if s.Store == nil {
		return host.FlowEvidenceRecord{}, errors.New("application assurance flow store is unavailable")
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return host.FlowEvidenceRecord{}, fmt.Errorf("encode flow evidence: %w", err)
	}
	_, stored, err := s.Store.putIfAbsent(
		ctx, "flow_evidence", key, record.EvidenceRef, raw,
	)
	if err != nil {
		return host.FlowEvidenceRecord{}, err
	}
	var canonical host.FlowEvidenceRecord
	if err := json.Unmarshal(stored, &canonical); err != nil {
		return host.FlowEvidenceRecord{}, fmt.Errorf("decode canonical flow evidence: %w", err)
	}
	return canonical, nil
}

func (s *SQLStore) get(
	ctx context.Context,
	kind, digest string,
) (string, []byte, bool, error) {
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT ref, payload
		FROM story_application_assurance_evidence
		WHERE kind=? AND digest=?`), kind, digest)
	var ref, payload string
	if err := row.Scan(&ref, &payload); errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	} else if err != nil {
		return "", nil, false, fmt.Errorf("read %s evidence: %w", kind, err)
	}
	return ref, []byte(payload), true, nil
}

func (s *SQLStore) putIfAbsent(
	ctx context.Context,
	kind, digest, ref string,
	raw []byte,
) (string, []byte, error) {
	_, err := s.db.ExecContext(ctx, s.q(`
		INSERT INTO story_application_assurance_evidence (kind, digest, ref, payload)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (kind, digest) DO NOTHING`),
		kind, digest, ref, string(raw),
	)
	if err != nil {
		return "", nil, fmt.Errorf("persist %s evidence: %w", kind, err)
	}
	storedRef, stored, ok, err := s.get(ctx, kind, digest)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return "", nil, fmt.Errorf("persist %s evidence: canonical record is unavailable", kind)
	}
	return storedRef, stored, nil
}

func (s *SQLStore) q(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	query = strings.ReplaceAll(
		query,
		"story_application_assurance_evidence",
		"applicationassurance.story_application_assurance_evidence",
	)
	var b strings.Builder
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}
