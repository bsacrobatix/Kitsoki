package webauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// Registers the pure-Go "sqlite" driver Open uses, same as internal/store.
	_ "modernc.org/sqlite"

	"kitsoki/internal/ulid"
)

// sqliteSchema is the idempotent DDL for the webauth tables. They live in the
// same SQLite file as the session store (sessions.db), so every name carries
// the webauth_ prefix. Times are unix milliseconds UTC, matching artifactjob.
const sqliteSchema = `
CREATE TABLE IF NOT EXISTS webauth_users (
  id            TEXT PRIMARY KEY,
  github_id     INTEGER NOT NULL UNIQUE,
  github_login  TEXT NOT NULL,
  display_name  TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user')),
  created_at    INTEGER NOT NULL,
  last_login_at INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE IF NOT EXISTS webauth_invites (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  role        TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user')),
  code_hash   TEXT NOT NULL UNIQUE,
  created_at  INTEGER NOT NULL,
  redeemed_at INTEGER,
  redeemed_by TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE IF NOT EXISTS webauth_sessions (
  id           TEXT PRIMARY KEY,
  token_hash   TEXT NOT NULL UNIQUE,
  user_id      TEXT NOT NULL REFERENCES webauth_users(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS webauth_sessions_user ON webauth_sessions(user_id);
CREATE INDEX IF NOT EXISTS webauth_sessions_expires ON webauth_sessions(expires_at);
`

// Roles a user or invite may carry. Only recorded and reported for now — no
// route is role-gated yet (see the package non-goals).
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// ErrInviteNotFound is returned by LookupInvite / RedeemInvite when the code
// matches no live (unredeemed) invite. Deliberately one error for "never
// existed" and "already redeemed": the caller must not tell an attacker which.
var ErrInviteNotFound = errors.New("webauth: no live invite for that code")

// User is an authorized person: someone who redeemed an invite (or appears in
// the auth.admins list) and signed in with GitHub.
type User struct {
	ID          string
	GitHubID    int64
	GitHubLogin string
	DisplayName string
	Role        string
	CreatedAt   time.Time
	LastLoginAt time.Time
}

// Invite is a pending or redeemed authorization for one person, identified by
// the sha256 of its one-time code.
type Invite struct {
	ID         string
	Name       string
	Role       string
	CreatedAt  time.Time
	RedeemedAt *time.Time
	RedeemedBy string
}

// Store persists users, invites, and browser sessions. Safe for concurrent
// use; all writes ride the single-connection *sql.DB it was built over.
// dialect defaults to SQLite; NewPostgresStore (pg.go) sets Postgres and the
// queries below are rebound through q at call time.
type Store struct {
	db      *sql.DB
	now     func() time.Time
	dialect dialect
}

// NewStore runs the idempotent schema DDL over db and returns a Store sharing
// that handle. The caller keeps ownership of db.
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("webauth.NewStore: nil db")
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		return nil, fmt.Errorf("webauth.NewStore: schema migration: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

// Open opens (or creates) the SQLite file at path with the same posture as the
// session store — WAL, foreign keys, busy_timeout, single connection — and
// runs the webauth DDL. Both the serving process and the `kitsoki web invite`
// CLI use this; WAL makes the cross-process access safe. The returned close
// func owns the handle.
func Open(path string) (*Store, func() error, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, nil, fmt.Errorf("webauth.Open: sql.Open: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("webauth.Open: pragma %q: %w", p, err)
		}
	}
	s, err := NewStore(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return s, db.Close, nil
}

// SetClock overrides the store's time source for tests.
func (s *Store) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// CreateInvite mints a one-time invite for name with the given role and
// returns it together with the plaintext code — the only time the plaintext
// exists outside the URL the operator shares.
func (s *Store) CreateInvite(ctx context.Context, name, role string) (Invite, string, error) {
	if role != RoleAdmin && role != RoleUser {
		return Invite{}, "", fmt.Errorf("webauth.CreateInvite: role %q is not %q or %q", role, RoleAdmin, RoleUser)
	}
	plain, hash, err := NewToken()
	if err != nil {
		return Invite{}, "", err
	}
	inv := Invite{ID: ulid.New(), Name: name, Role: role, CreatedAt: s.now().UTC()}
	_, err = s.db.ExecContext(ctx, s.q(`
		INSERT INTO webauth_invites (id, name, role, code_hash, created_at)
		VALUES (?, ?, ?, ?, ?)`),
		inv.ID, inv.Name, inv.Role, hash, unixMillis(inv.CreatedAt))
	if err != nil {
		return Invite{}, "", fmt.Errorf("webauth.CreateInvite: %w", err)
	}
	return inv, plain, nil
}

// LookupInvite resolves a plaintext code to its live (unredeemed) invite, or
// ErrInviteNotFound.
func (s *Store) LookupInvite(ctx context.Context, plainCode string) (Invite, error) {
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT id, name, role, created_at
		FROM webauth_invites
		WHERE code_hash = ? AND redeemed_at IS NULL`),
		HashToken(plainCode))
	var inv Invite
	var created int64
	if err := row.Scan(&inv.ID, &inv.Name, &inv.Role, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Invite{}, ErrInviteNotFound
		}
		return Invite{}, fmt.Errorf("webauth.LookupInvite: %w", err)
	}
	inv.CreatedAt = time.UnixMilli(created).UTC()
	return inv, nil
}

// ListInvites returns every invite, newest first, for `kitsoki web invite
// --list`. Codes are not recoverable (only hashes are stored).
func (s *Store) ListInvites(ctx context.Context) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT id, name, role, created_at, redeemed_at, redeemed_by
		FROM webauth_invites ORDER BY created_at DESC`))
	if err != nil {
		return nil, fmt.Errorf("webauth.ListInvites: %w", err)
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var inv Invite
		var created int64
		var redeemed sql.NullInt64
		if err := rows.Scan(&inv.ID, &inv.Name, &inv.Role, &created, &redeemed, &inv.RedeemedBy); err != nil {
			return nil, fmt.Errorf("webauth.ListInvites: %w", err)
		}
		inv.CreatedAt = time.UnixMilli(created).UTC()
		if redeemed.Valid {
			t := time.UnixMilli(redeemed.Int64).UTC()
			inv.RedeemedAt = &t
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RedeemInvite consumes a live invite for the GitHub identity gh in one
// transaction: the invite is re-checked and marked redeemed, and the user row
// is created (or, if the GitHub account already exists, updated) with the
// invite's role. A second redeem of the same code — or a redeem raced by
// another — returns ErrInviteNotFound.
func (s *Store) RedeemInvite(ctx context.Context, plainCode string, gh GitHubUser) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, fmt.Errorf("webauth.RedeemInvite: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC()
	row := tx.QueryRowContext(ctx, s.q(`
		SELECT id, name, role FROM webauth_invites
		WHERE code_hash = ? AND redeemed_at IS NULL`),
		HashToken(plainCode))
	var inv Invite
	if err := row.Scan(&inv.ID, &inv.Name, &inv.Role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrInviteNotFound
		}
		return User{}, fmt.Errorf("webauth.RedeemInvite: %w", err)
	}

	u, err := s.upsertUser(ctx, tx, gh, inv.Role, inv.Name, now)
	if err != nil {
		return User{}, fmt.Errorf("webauth.RedeemInvite: %w", err)
	}

	if _, err := tx.ExecContext(ctx, s.q(`
		UPDATE webauth_invites SET redeemed_at = ?, redeemed_by = ? WHERE id = ?`),
		unixMillis(now), u.ID, inv.ID); err != nil {
		return User{}, fmt.Errorf("webauth.RedeemInvite: mark redeemed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("webauth.RedeemInvite: commit: %w", err)
	}
	return u, nil
}

// EnsureAdmin provisions (or promotes) the GitHub identity gh as an admin —
// the auth.admins config path, which needs no invite. Login/display name are
// refreshed from GitHub on every call.
func (s *Store) EnsureAdmin(ctx context.Context, gh GitHubUser) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, fmt.Errorf("webauth.EnsureAdmin: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	u, err := s.upsertUser(ctx, tx, gh, RoleAdmin, gh.Name, s.now().UTC())
	if err != nil {
		return User{}, fmt.Errorf("webauth.EnsureAdmin: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("webauth.EnsureAdmin: commit: %w", err)
	}
	return u, nil
}

// upsertUser creates or refreshes the user row for a GitHub identity. An
// existing user keeps its stronger role: redeeming a user-role invite never
// demotes an admin, while an admin-role source (admin invite, auth.admins)
// promotes. displayName falls back to the GitHub profile name.
func (s *Store) upsertUser(ctx context.Context, tx *sql.Tx, gh GitHubUser, role, displayName string, now time.Time) (User, error) {
	if displayName == "" {
		displayName = gh.Name
	}
	row := tx.QueryRowContext(ctx, s.q(`SELECT id, role FROM webauth_users WHERE github_id = ?`), gh.ID)
	var u User
	err := row.Scan(&u.ID, &u.Role)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		u = User{
			ID:          ulid.New(),
			GitHubID:    gh.ID,
			GitHubLogin: gh.Login,
			DisplayName: displayName,
			Role:        role,
			CreatedAt:   now,
			LastLoginAt: now,
		}
		if _, err := tx.ExecContext(ctx, s.q(`
			INSERT INTO webauth_users (id, github_id, github_login, display_name, role, created_at, last_login_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`),
			u.ID, u.GitHubID, u.GitHubLogin, u.DisplayName, u.Role, unixMillis(u.CreatedAt), unixMillis(u.LastLoginAt)); err != nil {
			return User{}, err
		}
		return u, nil
	case err != nil:
		return User{}, err
	}
	if u.Role != RoleAdmin && role == RoleAdmin {
		u.Role = RoleAdmin
	}
	u.GitHubID = gh.ID
	u.GitHubLogin = gh.Login
	u.DisplayName = displayName
	u.LastLoginAt = now
	if _, err := tx.ExecContext(ctx, s.q(`
		UPDATE webauth_users SET github_login = ?, display_name = ?, role = ?, last_login_at = ? WHERE id = ?`),
		u.GitHubLogin, u.DisplayName, u.Role, unixMillis(u.LastLoginAt), u.ID); err != nil {
		return User{}, err
	}
	return u, nil
}

// UserByGitHubID resolves a returning GitHub identity to its user row. ok is
// false when the account has never been invited/provisioned.
func (s *Store) UserByGitHubID(ctx context.Context, id int64) (User, bool, error) {
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT id, github_id, github_login, display_name, role, created_at, last_login_at
		FROM webauth_users WHERE github_id = ?`), id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("webauth.UserByGitHubID: %w", err)
	}
	return u, true, nil
}

// TouchLogin refreshes a returning user's GitHub login/display name and
// last-login time (profile fields can change on GitHub between visits).
func (s *Store) TouchLogin(ctx context.Context, userID string, gh GitHubUser) error {
	_, err := s.db.ExecContext(ctx, s.q(`
		UPDATE webauth_users SET github_login = ?, display_name = CASE WHEN ? <> '' THEN ? ELSE display_name END, last_login_at = ?
		WHERE id = ?`),
		gh.Login, gh.Name, gh.Name, unixMillis(s.now().UTC()), userID)
	if err != nil {
		return fmt.Errorf("webauth.TouchLogin: %w", err)
	}
	return nil
}

// CreateSession mints a browser session for userID valid for ttl and returns
// the plaintext cookie token.
func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	plain, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	_, err = s.db.ExecContext(ctx, s.q(`
		INSERT INTO webauth_sessions (id, token_hash, user_id, created_at, expires_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)`),
		ulid.New(), hash, userID, unixMillis(now), unixMillis(now.Add(ttl)), unixMillis(now))
	if err != nil {
		return "", fmt.Errorf("webauth.CreateSession: %w", err)
	}
	return plain, nil
}

// SessionUser resolves a cookie token to its user. ok is false for an unknown
// or expired token. Expired rows are deleted opportunistically; live lookups
// bump last_seen_at.
func (s *Store) SessionUser(ctx context.Context, plainToken string) (User, bool, error) {
	now := s.now().UTC()
	hash := HashToken(plainToken)
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT u.id, u.github_id, u.github_login, u.display_name, u.role, u.created_at, u.last_login_at, sess.expires_at
		FROM webauth_sessions sess JOIN webauth_users u ON u.id = sess.user_id
		WHERE sess.token_hash = ?`), hash)
	var u User
	var created, lastLogin, expires int64
	err := row.Scan(&u.ID, &u.GitHubID, &u.GitHubLogin, &u.DisplayName, &u.Role, &created, &lastLogin, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("webauth.SessionUser: %w", err)
	}
	if !now.Before(time.UnixMilli(expires).UTC()) {
		_, _ = s.db.ExecContext(ctx, s.q(`DELETE FROM webauth_sessions WHERE token_hash = ?`), hash)
		return User{}, false, nil
	}
	u.CreatedAt = time.UnixMilli(created).UTC()
	u.LastLoginAt = time.UnixMilli(lastLogin).UTC()
	_, _ = s.db.ExecContext(ctx, s.q(`UPDATE webauth_sessions SET last_seen_at = ? WHERE token_hash = ?`), unixMillis(now), hash)
	return u, true, nil
}

// DeleteSession revokes a cookie token (logout). Unknown tokens are a no-op.
func (s *Store) DeleteSession(ctx context.Context, plainToken string) error {
	if _, err := s.db.ExecContext(ctx, s.q(`DELETE FROM webauth_sessions WHERE token_hash = ?`), HashToken(plainToken)); err != nil {
		return fmt.Errorf("webauth.DeleteSession: %w", err)
	}
	return nil
}

func scanUser(row *sql.Row) (User, error) {
	var u User
	var created, lastLogin int64
	if err := row.Scan(&u.ID, &u.GitHubID, &u.GitHubLogin, &u.DisplayName, &u.Role, &created, &lastLogin); err != nil {
		return User{}, err
	}
	u.CreatedAt = time.UnixMilli(created).UTC()
	u.LastLoginAt = time.UnixMilli(lastLogin).UTC()
	return u, nil
}

func unixMillis(t time.Time) int64 { return t.UTC().UnixMilli() }
