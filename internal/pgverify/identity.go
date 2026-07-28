package pgverify

import (
	"context"
	"database/sql"
	"fmt"

	"kitsoki/internal/store"
)

// CheckBackendIdentity proves the running kitsoki service is genuinely
// holding live connections to pgDB, using Postgres's own bookkeeping rather
// than anything the kitsoki process declares about itself. Every connection
// internal/store.OpenPostgresDSN opens is stamped with
// application_name=store.ApplicationName (see that constant's doc comment);
// pg_stat_activity is Postgres's own record of currently-connected backends,
// so a row there naming that application_name is independent, live evidence
// — not a flag, not an env var, not a value this tool declared.
//
// Reports StatusFail (not StatusNotEstablished) when the query succeeds but
// finds zero matching connections: that is a real, actionable negative
// (nothing is currently talking to this database as kitsoki), not an
// inability to check.
func CheckBackendIdentity(ctx context.Context, pgDB *sql.DB) CheckResult {
	const id = "backend_identity"
	const name = "Deployed service holds live Postgres connections (pg_stat_activity)"

	if pgDB == nil {
		return notEstablished(id, name, "no Postgres handle configured", nil)
	}

	rows, err := pgDB.QueryContext(ctx,
		`SELECT pid, client_addr, backend_start, state
		   FROM pg_stat_activity
		  WHERE application_name = $1
		  ORDER BY backend_start DESC`,
		store.ApplicationName,
	)
	if err != nil {
		return notEstablished(id, name, fmt.Sprintf("query pg_stat_activity: %v", err), nil)
	}
	defer rows.Close()

	type conn struct {
		PID          int    `json:"pid"`
		ClientAddr   string `json:"client_addr"`
		BackendStart string `json:"backend_start"`
		State        string `json:"state"`
	}
	var conns []conn
	for rows.Next() {
		var (
			pid               int
			clientAddr, state sql.NullString
			backendStart      sql.NullTime
		)
		if err := rows.Scan(&pid, &clientAddr, &backendStart, &state); err != nil {
			return notEstablished(id, name, fmt.Sprintf("scan pg_stat_activity row: %v", err), nil)
		}
		c := conn{PID: pid, ClientAddr: clientAddr.String, State: state.String}
		if backendStart.Valid {
			c.BackendStart = backendStart.Time.Format("2006-01-02T15:04:05Z07:00")
		}
		conns = append(conns, c)
	}
	if err := rows.Err(); err != nil {
		return notEstablished(id, name, fmt.Sprintf("iterate pg_stat_activity: %v", err), nil)
	}

	evidence := map[string]any{
		"application_name": store.ApplicationName,
		"connections":      conns,
		"connection_count": len(conns),
	}
	if len(conns) == 0 {
		return fail(id, name,
			fmt.Sprintf("zero live Postgres connections report application_name=%q — nothing is currently proven to be using this database as kitsoki (a stopped service, a still-SQLite-backed process, or a DSN pointed elsewhere would all look like this)", store.ApplicationName),
			evidence)
	}
	return pass(id, name,
		fmt.Sprintf("%d live connection(s) with application_name=%q observed directly in pg_stat_activity", len(conns), store.ApplicationName),
		evidence)
}
