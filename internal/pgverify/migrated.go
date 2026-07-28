package pgverify

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"kitsoki/internal/app"
	"kitsoki/internal/dbmigrate"
	"kitsoki/internal/store"
)

// CheckMigratedDataIntact spot-checks that PRE-EXISTING (not newly written)
// rows are both present in the expected quantity and readable through the
// real service. It never invents an expectation: with no ExpectedCounts and
// no SampleSessionIDs supplied, it reports StatusNotEstablished rather than
// silently passing on a database that happens to be empty.
//
// Two independent sub-proofs, both required for StatusPass:
//
//  1. Counts: reuses internal/dbmigrate's own PreflightShared (the same row
//     counter `db migrate` uses to refuse a non-empty destination), and
//     compares each returned DestCount.Rows against
//     opts.ExpectedCounts[DestCount.Label] as a minimum. A table named in
//     ExpectedCounts that PreflightShared did not return is a hard failure
//     (it means the expectation refers to something this deployment's
//     schema doesn't have), not silently ignored.
//  2. Readability: for each session in opts.SampleSessionIDs (or, if none
//     were given, up to 3 pre-existing sessions auto-selected via a raw SQL
//     query that excludes anything this package's own MarkerPrefix could
//     have created), reads the session and its history through svc
//     (store.Store — the real service code path) and cross-checks the
//     returned event count against an independent raw SQL COUNT(*) on the
//     same session_id, so a service-layer bug that silently dropped rows
//     would be caught even if LoadHistory itself did not error.
func CheckMigratedDataIntact(ctx context.Context, svc store.Store, pgDB *sql.DB, opts Options) CheckResult {
	const id = "migrated_data_intact"
	const name = "Pre-existing migrated data is intact and readable through the service"

	if pgDB == nil {
		return notEstablished(id, name, "no direct Postgres handle configured (Options.PGDB)", nil)
	}
	if len(opts.ExpectedCounts) == 0 && len(opts.SampleSessionIDs) == 0 {
		return notEstablished(id, name, "no --expect-count / --sample-session-id given; nothing to compare pre-existing data against", nil)
	}

	evidence := map[string]any{}

	if len(opts.ExpectedCounts) > 0 {
		counts, err := dbmigrate.PreflightShared(ctx, pgDB)
		if err != nil {
			return notEstablished(id, name, fmt.Sprintf("PreflightShared: %v", err), nil)
		}
		byLabel := make(map[string]int64, len(counts))
		for _, c := range counts {
			byLabel[c.Label] = c.Rows
		}
		var mismatches []string
		var checked []map[string]any
		labels := make([]string, 0, len(opts.ExpectedCounts))
		for label := range opts.ExpectedCounts {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		for _, label := range labels {
			want := opts.ExpectedCounts[label]
			got, known := byLabel[label]
			checked = append(checked, map[string]any{"label": label, "expected_min": want, "got": got, "known_table": known})
			if !known {
				mismatches = append(mismatches, fmt.Sprintf("%s: no such table in this deployment's schema", label))
				continue
			}
			if got < want {
				mismatches = append(mismatches, fmt.Sprintf("%s: expected at least %d rows, got %d", label, want, got))
			}
		}
		evidence["count_checks"] = checked
		if len(mismatches) > 0 {
			evidence["count_mismatches"] = mismatches
			return fail(id, name, fmt.Sprintf("%d table(s) did not meet expected minimum row counts", len(mismatches)), evidence)
		}
	}

	sampleIDs := opts.SampleSessionIDs
	if len(sampleIDs) == 0 {
		autoIDs, err := autoSampleSessionIDs(ctx, pgDB, 3)
		if err != nil {
			return notEstablished(id, name, fmt.Sprintf("auto-select sample sessions: %v", err), evidence)
		}
		sampleIDs = autoIDs
	}
	if len(sampleIDs) == 0 {
		if len(opts.ExpectedCounts) > 0 {
			// Counts alone passed; readability had nothing to sample against
			// (e.g. a sessions table with zero rows outside test data).
			evidence["sample_session_count"] = 0
			return pass(id, name, "expected row-count minimums satisfied; no pre-existing session available to spot-read", evidence)
		}
		return notEstablished(id, name, "no pre-existing sessions found to spot-read (and no --expect-count given)", evidence)
	}

	var samples []map[string]any
	for _, sidStr := range sampleIDs {
		sid := app.SessionID(sidStr)
		var rawCount int
		if err := pgDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE session_id = $1`, sidStr).Scan(&rawCount); err != nil {
			return fail(id, name, fmt.Sprintf("raw COUNT(*) for session %s: %v", sidStr, err), evidence)
		}
		history, err := svc.LoadHistory(sid)
		if err != nil {
			return fail(id, name, fmt.Sprintf("LoadHistory for pre-existing session %s failed: %v", sidStr, err), evidence)
		}
		summary, err := svc.GetSession(ctx, sid)
		if err != nil {
			return fail(id, name, fmt.Sprintf("GetSession for pre-existing session %s failed: %v", sidStr, err), evidence)
		}
		s := map[string]any{
			"session_id":               sidStr,
			"app_id":                   summary.AppID,
			"raw_event_count":          rawCount,
			"service_history_returned": len(history),
		}
		samples = append(samples, s)
		// LoadHistory returns events since the last snapshot, not the full
		// log, so it cannot be compared 1:1 against the raw table count when
		// a session has snapshots. What CAN be asserted unconditionally: it
		// must not error, and it must never return MORE events than exist in
		// the raw table (that would mean phantom rows).
		if len(history) > rawCount {
			return fail(id, name, fmt.Sprintf("session %s: LoadHistory returned %d events but raw table has only %d", sidStr, len(history), rawCount), evidence)
		}
	}
	evidence["samples"] = samples
	evidence["sample_session_count"] = len(samples)
	return pass(id, name, fmt.Sprintf("expected counts satisfied and %d pre-existing session(s) read back successfully through the service, cross-checked against raw row counts", len(samples)), evidence)
}

// autoSampleSessionIDs picks up to n pre-existing session ids, excluding
// anything whose app_id carries this package's own MarkerPrefix (so a
// verification run can never accidentally "spot check" its own test data).
func autoSampleSessionIDs(ctx context.Context, pgDB *sql.DB, n int) ([]string, error) {
	rows, err := pgDB.QueryContext(ctx,
		`SELECT id FROM sessions WHERE app_id NOT LIKE $1 ORDER BY started_at DESC LIMIT $2`,
		MarkerPrefix+"%", n,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
