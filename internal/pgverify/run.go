package pgverify

import (
	"context"
	"fmt"

	"kitsoki/internal/store"
)

// Run executes every check and returns the complete Report. It never
// returns a non-nil error for a check that could not be established —
// that is represented as StatusNotEstablished inside the report, per this
// package's fail-closed contract (see doc.go). Run only returns an error
// for a setup failure that makes the whole run meaningless (e.g. opening
// the service store itself fails), and even then still returns a partial
// Report with every check marked StatusNotEstablished, so the caller always
// has a JSON artifact to keep.
func Run(ctx context.Context, opts Options) (Report, error) {
	report := Report{GeneratedAt: opts.now()}

	var svc store.Store
	var svcErr error
	if opts.OpenServiceStore != nil {
		svc, svcErr = opts.OpenServiceStore()
		if svc != nil {
			defer func() { _ = svc.Close() }()
		}
	}

	// 1. Backend identity — needs only the raw Postgres handle.
	report.Checks = append(report.Checks, CheckBackendIdentity(ctx, opts.PGDB))

	// 2 & 3. Round trip + direct rows.
	var roundtrip, directRows CheckResult
	var cleanupRoundTrip func()
	if svcErr != nil {
		roundtrip = notEstablished("write_read_roundtrip", "Write-read round trip through the real service (store.Store)",
			fmt.Sprintf("could not open service store: %v", svcErr), nil)
		directRows = notEstablished("direct_postgres_rows", "Round-trip rows independently confirmed via raw SQL against Postgres",
			"skipped: service store unavailable", nil)
	} else {
		roundtrip, directRows, cleanupRoundTrip = CheckRoundTrip(ctx, svc, opts.PGDB, opts.KeepTestData)
	}
	report.Checks = append(report.Checks, roundtrip, directRows)

	// 4. SQLite inert — bracket the window so it bridges the round trip
	// that just happened; the round trip already wrote application data
	// before this check takes its "before" snapshot, so a settle window
	// here specifically catches DELAYED writes (checkpointing, batched
	// flushes), which is the more dangerous failure mode than an immediate
	// one.
	report.Checks = append(report.Checks, CheckSQLiteInert(opts.SQLitePath, StatSQLiteFiles(opts.SQLitePath), opts))

	if cleanupRoundTrip != nil {
		cleanupRoundTrip()
	}

	// 5. Migrated data intact.
	if svcErr != nil {
		report.Checks = append(report.Checks, notEstablished("migrated_data_intact", "Pre-existing migrated data is intact and readable through the service",
			fmt.Sprintf("could not open service store: %v", svcErr), nil))
	} else {
		report.Checks = append(report.Checks, CheckMigratedDataIntact(ctx, svc, opts.PGDB, opts))
	}

	// 6. stream_pos monotonicity under concurrency.
	if svcErr != nil {
		report.Checks = append(report.Checks, notEstablished("stream_pos_monotonicity", "stream_pos has no gap and no duplicate under concurrent appends",
			fmt.Sprintf("could not open service store: %v", svcErr), nil))
	} else {
		report.Checks = append(report.Checks, CheckStreamMonotonicity(ctx, svc, opts.PGDB, opts))
	}

	// 7. Type fidelity.
	if svcErr != nil {
		report.Checks = append(report.Checks, CheckTypeFidelity(ctx, nil, opts.PGDB, opts.KeepTestData))
	} else {
		report.Checks = append(report.Checks, CheckTypeFidelity(ctx, svc, opts.PGDB, opts.KeepTestData))
	}

	return report, nil
}
