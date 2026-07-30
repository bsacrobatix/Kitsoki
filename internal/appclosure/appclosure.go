// Package appclosure holds the ONE content-addressed digest byte layout used
// to seal a set of logical (path -> bytes) files into a single
// "sha256:<hex>" digest. It exists so that a future third caller — the
// application-definition-revision digest the live app-edit work (ADCP) needs
// — does not grow a third divergent hashing loop next to the two that
// already exist in this repo:
//
//   - internal/capsule/storydigest.Compute seals a Capsule CI story-closure
//     receipt (capsule-story-closure/v1): it walks a loaded story's
//     manifests, filters by runtime-relevant extension, and enforces a
//     project/external-root escape guard before hashing.
//   - internal/store.CollectEffectiveStory/hashStoryFiles seals the story
//     files captured under a session's own capture root, keyed relative to
//     that capture root, and compares against hashes already persisted in
//     trace JSONL (store.LatestStoryHash / StoryChangedPayload).
//
// Those two callers diverge on confinement policy, file selection, and
// return shape, and MUST keep diverging: full closure-computation unification
// is a larger project (ADCP M0) and out of scope here. What both of them (and
// any future third caller) share is only the final hashing step — given a
// resolved map of logical path to file bytes, turn it into one deterministic
// digest. That one step is what this package extracts.
//
// Byte-identity with storydigest.Compute's existing loop is load-bearing:
// storydigest's digest is stamped into persisted capsule-ci-receipt/v1
// receipts by callers across the repo (see cmd/kitsoki/capsule_promote.go,
// capsule_ci.go, vmpool_roundtrip.go, internal/capsule/ci/doctor.go,
// internal/capsule/workerserver/server.go,
// internal/capsule/queue/executor_gate.go, internal/mcp/capsule.go). Digest
// below reproduces that loop's byte layout verbatim; changing it here would
// silently invalidate every historical receipt that pinned a story closure.
// See storydigest_golden_test.go in that package for the pinned regression
// proof.
package appclosure

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
)

// File is one entry in a logical closure: the raw bytes to hash and the
// filesystem permission bits to fold into the digest alongside them (so a
// mode-only change, e.g. a script losing its executable bit, changes the
// digest even though the content didn't).
type File struct {
	Mode  fs.FileMode
	Bytes []byte
}

// DefaultMode is the permission bits DigestBytes stamps on every entry, for
// callers that have file content but no meaningful mode of their own (e.g. an
// in-memory patch result rather than something read off a real filesystem).
// It matches the mode most runtime-bearing story files are actually written
// with (see storydigest's use of os.WriteFile in its own tests).
const DefaultMode fs.FileMode = 0o644

// Digest hashes files into a single "sha256:<hex>" digest, scoped by schema
// so digests computed under different schemas never collide even over an
// identical file set. The layout is:
//
//  1. schema + "\n"
//  2. for each logical path, visited in SORTED order (so map iteration order
//     never affects the result): "<path>\x00<mode:%04o>\x00<len(bytes)>\x00"
//     followed by the raw bytes, followed by one trailing 0x00 byte.
//
// This is byte-identical to storydigest.Compute's hashing loop — see the
// package doc for why that must never drift.
func Digest(schema string, files map[string]File) string {
	logical := make([]string, 0, len(files))
	for path := range files {
		logical = append(logical, path)
	}
	sort.Strings(logical)

	h := sha256.New()
	_, _ = h.Write([]byte(schema + "\n"))
	for _, path := range logical {
		f := files[path]
		fmt.Fprintf(h, "%s\x00%04o\x00%d\x00", path, f.Mode.Perm(), len(f.Bytes))
		_, _ = h.Write(f.Bytes)
		_, _ = h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// DigestBytes is Digest for callers that only have raw bytes and no
// meaningful per-file mode — every entry is stamped with DefaultMode.
func DigestBytes(schema string, files map[string][]byte) string {
	withMode := make(map[string]File, len(files))
	for path, raw := range files {
		withMode[path] = File{Mode: DefaultMode, Bytes: raw}
	}
	return Digest(schema, withMode)
}
