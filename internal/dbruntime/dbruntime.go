// Package dbruntime is the P1 Postgres runtime seam from
// .context/stateless-orchestrator-storage-plan.md: one code path for
// relational storage whether kitsoki talks to an external Postgres cluster
// (stateless container deployment) or a local embedded server (dev, tests,
// single-machine installs).
//
// Selection is explicit: when Config.DSN or the KITSOKI_PG_DSN environment
// variable is set the runtime is a pure passthrough to that external server
// and never starts anything. Otherwise it manages an embedded Postgres
// (fergusstrange/embedded-postgres) with a persistent data directory and a
// per-machine binary cache, so the one-time binary download happens once and
// data survives restarts.
//
// The runtime hands out *sql.DB via the pgx stdlib driver so every existing
// database/sql seam keeps working unchanged.
package dbruntime

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"

	"kitsoki/internal/statedir"
)

// EnvDSN is the environment variable that forces passthrough mode to an
// external Postgres server, overriding embedded startup.
const EnvDSN = "KITSOKI_PG_DSN"

// Config selects and parameterizes the Postgres runtime.
type Config struct {
	// DataDir is the persistent data directory for the embedded server.
	// Empty selects a default under os.UserCacheDir()/kitsoki/embedded-pg.
	DataDir string
	// Port is the TCP port for the embedded server. Zero picks a free port.
	Port uint32
	// DSN, when non-empty (or when KITSOKI_PG_DSN is set), makes the runtime
	// a passthrough to that external server; no embedded server is started.
	DSN string
}

// Runtime is a running (or externally provided) Postgres endpoint.
type Runtime interface {
	// DSN returns the connection string for the underlying server.
	DSN() string
	// DB returns a pinged *sql.DB (pgx stdlib driver) for the runtime's DSN.
	// The handle is cached; Close closes it.
	DB() (*sql.DB, error)
	// Close releases the DB handle and, in embedded mode, stops the server.
	Close() error
}

// Start returns a Runtime for cfg. External mode (cfg.DSN or KITSOKI_PG_DSN
// set) returns immediately without starting anything; embedded mode starts a
// Postgres server on the configured data directory.
func Start(cfg Config) (Runtime, error) {
	dsn := cfg.DSN
	if dsn == "" {
		dsn = os.Getenv(EnvDSN)
	}
	if dsn != "" {
		return &runtime{dsn: dsn}, nil
	}
	return startEmbedded(cfg)
}

func startEmbedded(cfg Config) (Runtime, error) {
	dataDir := cfg.DataDir
	if dataDir == "" {
		base, err := defaultBaseDir()
		if err != nil {
			return nil, err
		}
		dataDir = filepath.Join(base, "data")
	}
	binDir, err := binaryCacheDir()
	if err != nil {
		return nil, err
	}
	port := cfg.Port
	if port == 0 {
		port, err = freePort()
		if err != nil {
			return nil, fmt.Errorf("dbruntime: pick port: %w", err)
		}
	}
	// Runtime scratch (pid file, sockets, logs) lives beside the data dir so
	// concurrent runtimes with distinct data dirs never collide.
	runtimeDir := dataDir + ".runtime"
	epCfg := embeddedpostgres.DefaultConfig().
		Username("postgres").
		Password("postgres").
		Database("postgres").
		DataPath(dataDir).
		RuntimePath(runtimeDir).
		BinariesPath(binDir).
		CachePath(binDir).
		Port(port).
		StartTimeout(2 * time.Minute).
		Logger(io.Discard)
	server := embeddedpostgres.NewDatabase(epCfg)
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("dbruntime: start embedded postgres: %w", err)
	}
	dsn := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	return &runtime{dsn: dsn, server: server}, nil
}

// runtime implements Runtime for both modes; server is nil in passthrough.
type runtime struct {
	dsn    string
	server *embeddedpostgres.EmbeddedPostgres

	mu sync.Mutex
	db *sql.DB
}

func (r *runtime) DSN() string { return r.dsn }

func (r *runtime) DB() (*sql.DB, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		return r.db, nil
	}
	db, err := sql.Open("pgx", r.dsn)
	if err != nil {
		return nil, fmt.Errorf("dbruntime: open: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("dbruntime: ping: %w", err)
	}
	r.db = db
	return db, nil
}

func (r *runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	if r.db != nil {
		errs = append(errs, r.db.Close())
		r.db = nil
	}
	if r.server != nil {
		errs = append(errs, r.server.Stop())
		r.server = nil
	}
	return errors.Join(errs...)
}

// defaultBaseDir is the per-machine home for embedded Postgres state.
// KITSOKI_STATE_DIR, when set, re-roots it at <state>/cache/embedded-pg so
// the binary download/extract cache (and any defaulted data dir) stays inside
// the single writable mount; unset keeps os.UserCacheDir()/kitsoki/embedded-pg
// unchanged.
func defaultBaseDir() (string, error) {
	if state, ok := statedir.Root(); ok {
		return filepath.Join(state, "cache", "embedded-pg"), nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("dbruntime: user cache dir: %w", err)
	}
	return filepath.Join(cache, "kitsoki", "embedded-pg"), nil
}

// binaryCacheDir is where downloaded/extracted Postgres binaries live, shared
// by every runtime on the machine so the download happens once.
func binaryCacheDir() (string, error) {
	base, err := defaultBaseDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("dbruntime: binary cache dir: %w", err)
	}
	return dir, nil
}

func freePort() (uint32, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil
}
