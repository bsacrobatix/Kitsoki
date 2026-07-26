package gitbackup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"kitsoki/internal/objectstore"
)

// ManifestSchema versions the JSON manifest object that records the bundle
// chain for one repository.
const ManifestSchema = "git-backup-manifest/v1"

// Entry kinds. A "full" entry is a self-contained compaction point, an "incr"
// entry carries only the objects new since the previous entry's refs snapshot,
// and a "refs" entry carries no bundle at all: it records a snapshot change
// (ref deletions, or rewinds to already-backed-up commits) whose objects are
// fully covered by the earlier chain.
const (
	KindFull = "full"
	KindIncr = "incr"
	KindRefs = "refs"
)

// ErrConcurrentUpdate reports that the manifest generation advanced between
// the read that planned a backup and the write that would record it.
var ErrConcurrentUpdate = errors.New("gitbackup: manifest changed concurrently; retry the backup")

// Entry describes one link of the bundle chain.
type Entry struct {
	Seq  int    `json:"seq"`
	Kind string `json:"kind"`
	// Key is the object key of the bundle; empty for KindRefs entries.
	Key string `json:"key,omitempty"`
	// Refs is the complete refs snapshot (name -> object id) after this
	// entry. Restore sets the target's refs to exactly the tip snapshot.
	Refs map[string]string `json:"refs"`
	// BasisRefs is the snapshot the increment was computed against
	// (the previous entry's Refs); empty for full backups.
	BasisRefs map[string]string `json:"basis_refs,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	Size      int64             `json:"size,omitempty"`
	SHA256    string            `json:"sha256,omitempty"`
}

// Manifest is the chain record stored at <prefix>/manifest.json. It is
// written last (write-after-data): a crash between bundle upload and manifest
// write leaves an orphan bundle object, never a manifest pointing at missing
// or unverified data.
//
// Concurrency story: the package assumes a single writer per prefix (one
// Backer driving backups for a repo). The objectstore seam has no conditional
// put, so true CAS is impossible; as a best-effort guard every write re-reads
// the manifest and refuses (ErrConcurrentUpdate) when Generation no longer
// matches the value loaded when the backup was planned, then writes
// Generation+1. This detects interleaved writers within one backup's window
// but cannot exclude a lost update from two writers racing the final put —
// hence the stated single-writer assumption.
type Manifest struct {
	Schema     string    `json:"schema"`
	Generation int64     `json:"generation"`
	UpdatedAt  time.Time `json:"updated_at,omitzero"`
	Entries    []Entry   `json:"entries"`
}

// Tip returns the newest entry, or false when the chain is empty.
func (m Manifest) Tip() (Entry, bool) {
	if len(m.Entries) == 0 {
		return Entry{}, false
	}
	return m.Entries[len(m.Entries)-1], true
}

func manifestKey(prefix string) string { return prefix + "/manifest.json" }

// loadManifest reads and validates the manifest, returning a zero-generation
// empty manifest when none exists yet.
func loadManifest(ctx context.Context, store objectstore.Store, prefix string) (Manifest, error) {
	body, _, err := store.Get(ctx, manifestKey(prefix))
	if errors.Is(err, objectstore.ErrNotFound) {
		return Manifest{Schema: ManifestSchema}, nil
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: read manifest: %w", err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: decode manifest: %w", err)
	}
	if err := validateManifest(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func validateManifest(m Manifest) error {
	if m.Schema != ManifestSchema {
		return fmt.Errorf("gitbackup: unsupported manifest schema %q", m.Schema)
	}
	for i, e := range m.Entries {
		if e.Seq != i+1 {
			return fmt.Errorf("gitbackup: manifest chain broken: entry %d has seq %d", i, e.Seq)
		}
		switch e.Kind {
		case KindFull, KindIncr:
			if e.Key == "" || e.Size <= 0 || e.SHA256 == "" {
				return fmt.Errorf("gitbackup: manifest entry seq %d (%s) missing bundle metadata", e.Seq, e.Kind)
			}
		case KindRefs:
			if e.Key != "" || e.Size != 0 || e.SHA256 != "" {
				return fmt.Errorf("gitbackup: manifest entry seq %d (refs) must not carry a bundle", e.Seq)
			}
		default:
			return fmt.Errorf("gitbackup: manifest entry seq %d has unknown kind %q", e.Seq, e.Kind)
		}
	}
	if len(m.Entries) > 0 && m.Entries[0].Kind != KindFull {
		return fmt.Errorf("gitbackup: manifest chain must start with a full backup, got %q", m.Entries[0].Kind)
	}
	return nil
}

// putManifest appends entry to the manifest last seen at basisGeneration and
// writes the next generation, guarding against concurrent writers.
func putManifest(ctx context.Context, store objectstore.Store, prefix string, basisGeneration int64, entry Entry, now time.Time) (Manifest, error) {
	current, err := loadManifest(ctx, store, prefix)
	if err != nil {
		return Manifest{}, err
	}
	if current.Generation != basisGeneration {
		return Manifest{}, fmt.Errorf("%w (generation %d, expected %d)", ErrConcurrentUpdate, current.Generation, basisGeneration)
	}
	next := current
	next.Generation = basisGeneration + 1
	next.UpdatedAt = now.UTC()
	next.Entries = append(append([]Entry(nil), current.Entries...), entry)
	if err := validateManifest(next); err != nil {
		return Manifest{}, err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: encode manifest: %w", err)
	}
	if _, err := store.Put(ctx, manifestKey(prefix), bytes.NewReader(data), int64(len(data)), objectstore.PutOptions{ContentType: "application/json"}); err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: write manifest: %w", err)
	}
	return next, nil
}

// refsDigest returns a short stable digest of a refs snapshot, used in
// incremental bundle object keys to name the old and new tips.
func refsDigest(refs map[string]string) string {
	if len(refs) == 0 {
		return "empty"
	}
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	for _, name := range names {
		fmt.Fprintf(&buf, "%s %s\n", refs[name], name)
	}
	return shortSHA256(buf.Bytes())
}
