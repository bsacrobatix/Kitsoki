package turncache

import (
	"context"
	"time"
)

// Cache is the abstraction the orchestrator (Phase 5) and Phase 6's SQLite
// backend both satisfy. All methods are context-aware; implementations
// should honour cancellation at cheap loop boundaries but are not required
// to interrupt single-row mutations mid-flight.
type Cache interface {
	// Get returns the cached verdict for k. If no row exists the boolean
	// return is false and the CachedVerdict is the zero value.
	Get(ctx context.Context, k Key) (CachedVerdict, bool, error)

	// Put inserts or replaces the row at k with v. Put is the canonical
	// place to seed CreatedAt — implementations may overwrite a
	// zero-valued v.CreatedAt with the current time.
	Put(ctx context.Context, k Key, v CachedVerdict) error

	// RecordHit notes that the row at k was used and successfully
	// re-validated at the given timestamp. Increments HitCount, advances
	// LastHitAt and LastVerifiedAt, and resets RevalidateFails to 0. A
	// no-op if the row does not exist.
	RecordHit(ctx context.Context, k Key, at time.Time) error

	// RecordRevalidateFail notes that a row at k was retrieved but its
	// re-validation against the live Machine state failed. Increments
	// RevalidateFails; once it reaches the configured RevalidateStrikes
	// the row is deleted and evicted is true. A no-op (evicted=false)
	// if the row does not exist.
	RecordRevalidateFail(ctx context.Context, k Key, at time.Time) (evicted bool, err error)

	// InvalidateOtherHashes deletes every row whose App matches app but
	// whose AppHash differs from keepHash. Implements §7.1 (app-hash
	// invalidation) and is typically called once at process start.
	InvalidateOtherHashes(ctx context.Context, app string, keepHash string) (deleted int, err error)

	// SweepCold deletes every row for app whose LastHitAt is strictly
	// older than olderThan. Implements §7.4 (time-based expiry).
	SweepCold(ctx context.Context, app string, olderThan time.Time) (deleted int, err error)

	// TrimLRU brings the row count for app below cap by deleting the
	// bottom trimFraction of rows sorted by LastHitAt ascending.
	// Implements §7.3 (LRU + size cap). A no-op when current row count
	// is already ≤ cap.
	TrimLRU(ctx context.Context, app string, cap int, trimFraction float64) (deleted int, err error)

	// RecordSynonymHit records a hit on the declared synonym described
	// by sk. Implements §7.6 (per-synonym hit tracking).
	RecordSynonymHit(ctx context.Context, sk SynonymKey, at time.Time) error

	// SynonymStats returns every synonym-hit row for appHash, sorted by
	// HitCount descending (ties broken by LastHitAt descending).
	SynonymStats(ctx context.Context, appHash string) ([]SynonymStat, error)

	// Close releases backend resources. For the in-memory cache this is
	// a no-op; Phase 6's SQLite cache will close the underlying DB.
	Close() error
}

// Config controls policy thresholds. Defaults track the proposal §7
// recommendations; see [DefaultConfig].
type Config struct {
	// MaxAge is the §7.4 time-based expiry cutoff. A row whose LastHitAt
	// is older than this is swept on the next [Cache.SweepCold] call.
	// Zero disables time-based expiry entirely.
	MaxAge time.Duration

	// Cap is the §7.3 LRU row cap per app. Zero disables the cap.
	Cap int

	// TrimFraction is the fraction of rows discarded when [Cache.TrimLRU]
	// runs against an over-cap app. 0.10 means "drop the coldest 10%".
	TrimFraction float64

	// RevalidateStrikes is the §7.2 strike count: after this many
	// consecutive [Cache.RecordRevalidateFail] calls the row is evicted.
	// Must be ≥ 1; values < 1 are treated as 1.
	RevalidateStrikes int

	// ConfidenceDecay enables §7.5: rows whose originating
	// CachedVerdict.Confidence is below 0.7 have their effective MaxAge
	// halved during [Cache.SweepCold]. Opt-in; default off.
	ConfidenceDecay bool
}

// DefaultConfig returns the proposal's recommended defaults: 30-day expiry,
// 10k-row cap, 10% trim fraction, 3 strikes, no confidence decay.
func DefaultConfig() Config {
	return Config{
		MaxAge:            30 * 24 * time.Hour,
		Cap:               10_000,
		TrimFraction:      0.10,
		RevalidateStrikes: 3,
		ConfidenceDecay:   false,
	}
}
