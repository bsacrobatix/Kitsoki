package workqueue

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const postgresBundleRefPrefix = "workqueue-pg-bundle:"

// PostgresBundleValidator imports a verified daemon-local intake file into
// PostgreSQL before a code receipt becomes terminal. The canonical receipt
// reference resolves from every daemon sharing that database.
type PostgresBundleValidator struct {
	db     *sql.DB
	intake *FileBundleValidator
}

func NewPostgresBundleValidator(
	db *sql.DB,
	intakeRoot string,
	maxBytes int64,
) (*PostgresBundleValidator, error) {
	if db == nil {
		return nil, fmt.Errorf("workqueue postgres bundle validator: nil database")
	}
	intake, err := NewFileBundleValidator(intakeRoot, maxBytes)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE SCHEMA IF NOT EXISTS workqueue;
		CREATE TABLE IF NOT EXISTS workqueue.bundles (
		  digest TEXT PRIMARY KEY,
		  data BYTEA NOT NULL,
		  size_bytes BIGINT NOT NULL,
		  created_at BIGINT NOT NULL
		)`); err != nil {
		return nil, fmt.Errorf("workqueue postgres bundle schema: %w", err)
	}
	return &PostgresBundleValidator{db: db, intake: intake}, nil
}

func (v *PostgresBundleValidator) ValidateBundle(
	ctx context.Context,
	ref, digest string,
) (string, error) {
	data, err := v.intake.readBundle(ctx, ref, digest)
	if err != nil {
		return "", err
	}
	digest = strings.TrimSpace(digest)
	if _, err := v.db.ExecContext(
		ctx,
		`INSERT INTO workqueue.bundles (digest,data,size_bytes,created_at)
		 VALUES ($1,$2,$3,
		   FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT)
		 ON CONFLICT (digest) DO NOTHING`,
		digest, data, len(data),
	); err != nil {
		return "", fmt.Errorf("retain workqueue postgres bundle: %w", err)
	}
	stored, err := v.Bundle(ctx, postgresBundleRefPrefix+digest)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(stored, data) {
		return "", ErrInvalid
	}
	return postgresBundleRefPrefix + digest, nil
}

// Bundle resolves canonical retained evidence from the shared database.
func (v *PostgresBundleValidator) Bundle(
	ctx context.Context,
	ref string,
) ([]byte, error) {
	digest := strings.TrimPrefix(strings.TrimSpace(ref), postgresBundleRefPrefix)
	if digest == ref || !validSHA256Digest(digest) {
		return nil, ErrInvalid
	}
	var data []byte
	var size int64
	if err := v.db.QueryRowContext(
		ctx,
		`SELECT data,size_bytes FROM workqueue.bundles WHERE digest=$1`,
		digest,
	).Scan(&data, &size); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, ErrInvalid
	}
	return data, nil
}
