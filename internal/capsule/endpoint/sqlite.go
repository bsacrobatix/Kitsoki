package endpoint

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore is the daemon's durable, machine-wide broker state. Callers
// choose its path from daemon configuration, never a repository .capsules dir.
type SQLiteStore struct{ db *sql.DB }

func OpenSQLite(path string) (*SQLiteStore, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: sqlite path is required", ErrInvalid)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &SQLiteStore{db: db}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE IF NOT EXISTS endpoint_reservations (protocol TEXT NOT NULL,address TEXT NOT NULL,port INTEGER NOT NULL,description TEXT NOT NULL, PRIMARY KEY(protocol,address,port)); CREATE TABLE IF NOT EXISTS endpoint_leases (id TEXT PRIMARY KEY,runtime_id TEXT NOT NULL,service TEXT NOT NULL,role TEXT NOT NULL,owner TEXT NOT NULL,generation INTEGER NOT NULL,source_digest TEXT NOT NULL,protocol TEXT NOT NULL,address TEXT NOT NULL,port INTEGER NOT NULL,exposure TEXT NOT NULL,acquired_at INTEGER NOT NULL,expires_at INTEGER NOT NULL, UNIQUE(runtime_id,service,role), UNIQUE(protocol,address,port));`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("capsule endpoint: initialize store: %w", err)
	}
	return store, nil
}
func (s *SQLiteStore) Close() error { return s.db.Close() }
func (s *SQLiteStore) Reserve(ctx context.Context, r Reservation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ignored int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM endpoint_leases WHERE protocol=? AND address=? AND port=?`, r.Endpoint.Protocol, r.Endpoint.Address, r.Endpoint.Port).Scan(&ignored)
	if err == nil {
		return fmt.Errorf("%w: %s is already leased", ErrConflict, r.Endpoint)
	}
	if err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO endpoint_reservations(protocol,address,port,description) VALUES(?,?,?,?)`, r.Endpoint.Protocol, r.Endpoint.Address, r.Endpoint.Port, r.Description); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrReserved, r.Endpoint, err)
	}
	return tx.Commit()
}
func (s *SQLiteStore) Allocate(ctx context.Context, now time.Time, requests []Request) ([]Lease, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, r := range requests {
		var ignored int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM endpoint_reservations WHERE protocol=? AND address=? AND port=?`, r.Endpoint.Protocol, r.Endpoint.Address, r.Endpoint.Port).Scan(&ignored)
		if err == nil {
			return nil, fmt.Errorf("%w: %s", ErrReserved, r.Endpoint)
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	leases := make([]Lease, 0, len(requests))
	for _, r := range requests {
		l, found, err := sqliteRole(ctx, tx, r)
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO endpoint_leases(id,runtime_id,service,role,owner,generation,source_digest,protocol,address,port,exposure,acquired_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, l.ID, l.RuntimeID, l.Service, l.Role, l.Owner, l.Generation, l.SourceDigest, l.Endpoint.Protocol, l.Endpoint.Address, l.Endpoint.Port, l.Endpoint.Exposure, l.AcquiredAt.UnixNano(), l.ExpiresAt.UnixNano()); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrConflict, r.Endpoint, err)
		}
		leases = append(leases, l)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return leases, nil
}
func (s *SQLiteStore) Renew(ctx context.Context, now time.Time, h Handle, ttl time.Duration) (Lease, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE endpoint_leases SET expires_at=? WHERE id=? AND owner=? AND generation=?`, now.Add(ttl).UnixNano(), h.ID, h.Owner, h.Generation)
	if err != nil {
		return Lease{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return Lease{}, fmt.Errorf("%w: %s", ErrLease, h.ID)
	}
	return s.get(ctx, h.ID)
}
func (s *SQLiteStore) Release(ctx context.Context, h Handle) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM endpoint_leases WHERE id=? AND owner=? AND generation=?`, h.ID, h.Owner, h.Generation)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("%w: %s", ErrLease, h.ID)
	}
	return nil
}
func (s *SQLiteStore) List(ctx context.Context) ([]Lease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,runtime_id,service,role,owner,generation,source_digest,protocol,address,port,exposure,acquired_at,expires_at FROM endpoint_leases ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLeases(rows)
}
func (s *SQLiteStore) Reap(ctx context.Context, now time.Time, live Liveness) ([]Lease, error) {
	leases, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Lease
	for _, l := range leases {
		if !l.ExpiresAt.After(now) && !live.Live(ctx, l.RuntimeID, l.Generation) {
			// Include the observed expiry in the delete CAS. A concurrent keepalive
			// must win over a reaper which observed the old expired record.
			result, err := s.db.ExecContext(ctx, `DELETE FROM endpoint_leases WHERE id=? AND owner=? AND generation=? AND expires_at=?`, l.ID, l.Owner, l.Generation, l.ExpiresAt.UnixNano())
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
func (s *SQLiteStore) get(ctx context.Context, id string) (Lease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,runtime_id,service,role,owner,generation,source_digest,protocol,address,port,exposure,acquired_at,expires_at FROM endpoint_leases WHERE id=?`, id)
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
func sqliteRole(ctx context.Context, tx *sql.Tx, r Request) (Lease, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,runtime_id,service,role,owner,generation,source_digest,protocol,address,port,exposure,acquired_at,expires_at FROM endpoint_leases WHERE runtime_id=? AND service=? AND role=?`, r.RuntimeID, r.Service, r.Role)
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
func scanLeases(rows *sql.Rows) ([]Lease, error) {
	var out []Lease
	for rows.Next() {
		var l Lease
		var port uint16
		var acquired, expires int64
		if err := rows.Scan(&l.ID, &l.RuntimeID, &l.Service, &l.Role, &l.Owner, &l.Generation, &l.SourceDigest, &l.Endpoint.Protocol, &l.Endpoint.Address, &port, &l.Endpoint.Exposure, &acquired, &expires); err != nil {
			return nil, err
		}
		l.Schema = Schema
		l.Endpoint.Port = port
		l.AcquiredAt = time.Unix(0, acquired).UTC()
		l.ExpiresAt = time.Unix(0, expires).UTC()
		out = append(out, l)
	}
	return out, rows.Err()
}
