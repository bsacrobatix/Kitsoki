// Package appdef is the definition control plane: an append-only,
// content-addressed store of immutable application-definition revisions, a
// typed patch operation that produces new revisions, and the per-session
// binding that moves a live session from one revision to another without a
// restart.
//
// Immutability is the whole point. A revision's digest is computed over its
// file set (internal/appclosure) and a revision is stored only after it
// COMPILES AND VALIDATES through the ordinary loader. Nothing mutates a
// stored revision, ever; a fix produces a NEW revision whose ParentDigest
// names the one it came from.
//
// This package deliberately stops short of the ADCP epic's
// gate/authorize/promote lifecycle (milestone M2) and release pointers (M3).
// Patch returns a revision; it never moves a session onto one. Moving is a
// separate, explicit call (Binding.ReloadTo). That split is the seam those
// milestones grow into.
//
// appdef is a pure library: it imports internal/app and internal/appclosure
// and nothing from internal/orchestrator, internal/store, internal/runstatus,
// or cmd/kitsoki. Everything external — storage, compilation, the live
// session it binds to — is an injected interface.
package appdef

import (
	"sort"
	"time"
)

// RevisionSchema is the schema label bound into every revision digest
// (appclosure.DigestBytes(RevisionSchema, files)). Changing it changes every
// digest this package produces; it is a distinct schema from
// storydigest.Schema ("capsule-story-closure/v1") so a revision digest and a
// CI receipt's story-closure digest can never collide even over identical
// file sets computed at different points in the pipeline.
const RevisionSchema = "application-definition-revision/v1"

// Closure is a definition file set plus the entry manifest's path within it.
// Paths are closure-relative and forward-slashed. This is exactly the shape
// app.LoadFromFiles consumes and internal/store's EffectiveStory produces —
// carried here as a plain value so appdef never imports the trace store.
type Closure struct {
	Entry string
	Files map[string][]byte
}

// Revision is an immutable, digest-addressed definition closure. Once
// stored, none of its fields change; a fix produces a new Revision with
// ParentDigest set to this one's Digest.
type Revision struct {
	Schema string
	// Digest is "sha256:<hex>", from appclosure.DigestBytes(RevisionSchema, Files).
	Digest string
	// AppID is the app.id of the compiled definition; "" when unknown.
	AppID string
	Entry string
	// Files is the complete file set. Treat as read-only: copy defensively
	// before mutating anything obtained from a Revision.
	Files map[string][]byte
	// ParentDigest is "" for a captured (root) revision, or the digest of the
	// revision a patch was applied to.
	ParentDigest string
	// Source is SourceCapture or SourcePatch.
	Source    string
	CreatedAt time.Time
}

// Revision sources.
const (
	SourceCapture = "capture" // derived from a definition already being served
	SourcePatch   = "patch"   // produced by applying ops to a parent revision
)

// Header is Revision without the bytes — the wire/list projection a caller
// uses to show a revision (digest, parent, size, file names) without paying
// to move every byte of every stored definition over the wire just to list
// them.
type Header struct {
	Schema       string    `json:"schema"`
	Digest       string    `json:"digest"`
	AppID        string    `json:"app_id,omitempty"`
	Entry        string    `json:"entry"`
	ParentDigest string    `json:"parent_digest,omitempty"`
	Source       string    `json:"source"`
	CreatedAt    time.Time `json:"created_at"`
	// Files is the sorted list of closure-relative paths in the revision.
	Files      []string `json:"files"`
	TotalBytes int      `json:"total_bytes"`
}

// Header projects r into its wire/list form. The Files slice is freshly
// built (sorted, not aliasing r.Files), so a caller may retain or mutate it
// freely.
func (r Revision) Header() Header {
	files := make([]string, 0, len(r.Files))
	total := 0
	for path, b := range r.Files {
		files = append(files, path)
		total += len(b)
	}
	sort.Strings(files)
	return Header{
		Schema:       r.Schema,
		Digest:       r.Digest,
		AppID:        r.AppID,
		Entry:        r.Entry,
		ParentDigest: r.ParentDigest,
		Source:       r.Source,
		CreatedAt:    r.CreatedAt,
		Files:        files,
		TotalBytes:   total,
	}
}

// cloneFiles returns a fully independent deep copy of files: a fresh map
// whose values are fresh []byte slices over fresh backing arrays. This is
// deliberately a full deep copy, not just a fresh map over shared slices —
// Storage.Get hands a copy to every caller, and a caller mutating the bytes
// it got back (e.g. building a patch candidate in place, or a test poking a
// byte to probe immutability) must never be able to reach the stored
// revision's own backing array.
func cloneFiles(files map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for k, v := range files {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}
