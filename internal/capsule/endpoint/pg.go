package endpoint

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	// Registers the "pgx" database/sql driver for callers opening the
	// Postgres handle (kitsoki/internal/dbruntime opens with it too).
	_ "github.com/jackc/pgx/v5/stdlib"
)

// pgDDL is the Postgres translation of the SQLite store's schema: same two
// tables and constraints, with BIGINT for the nanosecond timestamps and the
// generation counter, and INTEGER for the port, in a dedicated "endpoint"
// schema (schema-per-subsystem on a shared database). Idempotent via
// IF NOT EXISTS.
const pgDDL = `
CREATE SCHEMA IF NOT EXISTS endpoint;

CREATE TABLE IF NOT EXISTS endpoint.endpoint_reservations (
  protocol    TEXT    NOT NULL,
  address     TEXT    NOT NULL,
  port        INTEGER NOT NULL,
  description TEXT    NOT NULL,
  PRIMARY KEY (protocol, address, port)
);
CREATE TABLE IF NOT EXISTS endpoint.endpoint_leases (
  id            TEXT PRIMARY KEY,
  runtime_id    TEXT    NOT NULL,
  service       TEXT    NOT NULL,
  role          TEXT    NOT NULL,
  owner         TEXT    NOT NULL,
  generation    BIGINT  NOT NULL,
  source_digest TEXT    NOT NULL,
  protocol      TEXT    NOT NULL,
  address       TEXT    NOT NULL,
  port          INTEGER NOT NULL,
  exposure      TEXT    NOT NULL,
  acquired_at   BIGINT  NOT NULL,
  expires_at    BIGINT  NOT NULL,
  UNIQUE (runtime_id, service, role),
  UNIQUE (protocol, address, port)
);
`

// PostgresStore is the machine-wide broker state on a shared Postgres
// database — same authority contract as SQLiteStore, for deployments where
// several hosts (or containers) coordinate through one server. The caller
// keeps ownership of db and closes it; the store never does.
type PostgresStore struct{ db *sql.DB }

// NewPostgresStore applies the idempotent DDL over db (pgx stdlib driver)
// and returns the store sharing that handle.
func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: postgres db handle is required", ErrInvalid)
	}
	if _, err := db.Exec(pgDDL); err != nil {
		return nil, fmt.Errorf("capsule endpoint: initialize postgres store: %w", err)
	}
	return &PostgresStore{db: db}, nil
}
func (s *PostgresStore) Reserve(ctx context.Context, r Reservation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ignored int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM endpoint.endpoint_leases WHERE protocol=$1 AND address=$2 AND port=$3`, r.Endpoint.Protocol, r.Endpoint.Address, r.Endpoint.Port).Scan(&ignored)
	if err == nil {
		return fmt.Errorf("%w: %s is already leased", ErrConflict, r.Endpoint)
	}
	if err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO endpoint.endpoint_reservations(protocol,address,port,description) VALUES($1,$2,$3,$4)`, r.Endpoint.Protocol, r.Endpoint.Address, r.Endpoint.Port, r.Description); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrReserved, r.Endpoint, err)
	}
	return tx.Commit()
}
func (s *PostgresStore) Allocate(ctx context.Context, now time.Time, requests []Request) ([]Lease, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, r := range requests {
		var ignored int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM endpoint.endpoint_reservations WHERE protocol=$1 AND address=$2 AND port=$3`, r.Endpoint.Protocol, r.Endpoint.Address, r.Endpoint.Port).Scan(&ignored)
		if err == nil {
			return nil, fmt.Errorf("%w: %s", ErrReserved, r.Endpoint)
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	leases := make([]Lease, 0, len(requests))
	for _, r := range requests {
		l, found, err := pgRole(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		if found {
			if !sameRequest(l, r) {
				return nil, fmt.Errorf("%w: logical role %s/%s/%s", ErrConflict, r.RuntimeID, r.Service, r.Role)
			}
			leases = append(leases, l)
			continue
		}
		l = Lease{Schema: Schema, ID: leaseID(r), RuntimeID: r.RuntimeID, Service: r.Service, Role: r.Role, Owner: r.Owner, Generation: r.Generation, SourceDigest: r.SourceDigest, Endpoint: r.Endpoint, AcquiredAt: now, ExpiresAt: now.Add(r.TTL)}
		if _, err := tx.ExecContext(ctx, `INSERT INTO endpoint.endpoint_leases(id,runtime_id,service,role,owner,generation,source_digest,protocol,address,port,exposure,acquired_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, l.ID, l.RuntimeID, l.Service, l.Role, l.Owner, l.Generation, l.SourceDigest, l.Endpoint.Protocol, l.Endpoint.Address, l.Endpoint.Port, l.Endpoint.Exposure, l.AcquiredAt.UnixNano(), l.ExpiresAt.UnixNano()); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrConflict, r.Endpoint, err)
		}
		leases = append(leases, l)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return leases, nil
}
func (s *PostgresStore) Renew(ctx context.Context, now time.Time, h Handle, ttl time.Duration) (Lease, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE endpoint.endpoint_leases SET expires_at=$1 WHERE id=$2 AND owner=$3 AND generation=$4`, now.Add(ttl).UnixNano(), h.ID, h.Owner, h.Generation)
	if err != nil {
		return Lease{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return Lease{}, fmt.Errorf("%w: %s", ErrLease, h.ID)
	}
	return s.get(ctx, h.ID)
}
func (s *PostgresStore) Release(ctx context.Context, h Handle) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM endpoint.endpoint_leases WHERE id=$1 AND owner=$2 AND generation=$3`, h.ID, h.Owner, h.Generation)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("%w: %s", ErrLease, h.ID)
	}
	return nil
}
func (s *PostgresStore) List(ctx context.Context) ([]Lease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,runtime_id,service,role,owner,generation,source_digest,protocol,address,port,exposure,acquired_at,expires_at FROM endpoint.endpoint_leases ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLeases(rows)
}
func (s *PostgresStore) Reap(ctx context.Context, now time.Time, live Liveness) ([]Lease, error) {
	leases, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Lease
	for _, l := range leases {
		if !l.ExpiresAt.After(now) && !live.Live(ctx, l.RuntimeID, l.Generation) {
			// Include the observed expiry in the delete CAS. A concurrent keepalive
			// must win over a reaper which observed the old expired record.
			result, err := s.db.ExecContext(ctx, `DELETE FROM endpoint.endpoint_leases WHERE id=$1 AND owner=$2 AND generation=$3 AND expires_at=$4`, l.ID, l.Owner, l.Generation, l.ExpiresAt.UnixNano())
			if err != nil {
				return nil, err
			}
			deleted, err := result.RowsAffected()
			if err != nil {
				return nil, err
			}
			if deleted == 1 {
				out = append(out, l)
			}
		}
	}
	return out, nil
}
func (s *PostgresStore) get(ctx context.Context, id string) (Lease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,runtime_id,service,role,owner,generation,source_digest,protocol,address,port,exposure,acquired_at,expires_at FROM endpoint.endpoint_leases WHERE id=$1`, id)
	if err != nil {
		return Lease{}, err
	}
	defer rows.Close()
	ls, err := scanLeases(rows)
	if err != nil {
		return Lease{}, err
	}
	if len(ls) != 1 {
		return Lease{}, fmt.Errorf("%w: %s", ErrLease, id)
	}
	return ls[0], nil
}
func pgRole(ctx context.Context, tx *sql.Tx, r Request) (Lease, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,runtime_id,service,role,owner,generation,source_digest,protocol,address,port,exposure,acquired_at,expires_at FROM endpoint.endpoint_leases WHERE runtime_id=$1 AND service=$2 AND role=$3`, r.RuntimeID, r.Service, r.Role)
	if err != nil {
		return Lease{}, false, err
	}
	defer rows.Close()
	ls, err := scanLeases(rows)
	if err != nil {
		return Lease{}, false, err
	}
	if len(ls) == 0 {
		return Lease{}, false, nil
	}
	return ls[0], true, nil
}
