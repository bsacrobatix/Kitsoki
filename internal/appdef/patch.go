package appdef

import (
	"path"
	"path/filepath"
	"strings"
)

// Op is one typed definition mutation. The vocabulary is deliberately tiny:
// set_file is the only op this slice needs, and a typed envelope with one op
// is still a typed envelope — the shape ADCP M2 grows the vocabulary into.
// There is no shell op, no whole-directory op, and no path that escapes the
// closure.
type Op struct {
	Op   string `json:"op"`   // OpSetFile
	Path string `json:"path"` // closure-relative, forward slashes
	// Content is the complete new file content for set_file — a patch always
	// replaces a file wholesale, never diffs it. That keeps the compile step
	// (step 7 of Patch) the only place partial/malformed content can be
	// discovered, instead of also having to reason about a diff applying
	// cleanly.
	Content string `json:"content"`
}

// OpSetFile replaces (or creates) one file in the closure with Content.
const OpSetFile = "set_file"

// Reject is one named reason a patch was refused. Code is a stable slug the
// client can branch on; Message is human text; File names the offending
// path when the reason is path- or compile-specific.
type Reject struct {
	Code    string `json:"code"`
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}

// Reject codes.
const (
	RejectUnknownBase   = "unknown_base"   // base_digest names no stored revision
	RejectStaleBase     = "stale_base"     // base_digest is not the session's current revision
	RejectNoOps         = "no_ops"         // empty ops list
	RejectUnknownOp     = "unknown_op"     // op field is not a supported op
	RejectBadPath       = "bad_path"       // empty, absolute, backslashed, or ..-escaping path
	RejectNoChange      = "no_change"      // ops produce a byte-identical closure
	RejectEntryRemoved  = "entry_removed"  // the resulting closure has no entry file
	RejectCompileFailed = "compile_failed" // the candidate does not load/validate
)

// PatchResult is the outcome of Patch. A REJECTED patch is a normal,
// successful call that produced no revision — mirroring internal/graph's
// propose/apply contract, where validation failure is a typed payload and
// only an operational fault (storage failure, context cancellation) is an
// error.
type PatchResult struct {
	Rejected bool
	Rejects  []Reject
	Revision Revision // zero when Rejected
}

// validatePath rejects, never sanitises, a bad closure-relative path. A
// silently-rewritten path (e.g. stripping a leading "../") is exactly the
// kind of surprise this control plane exists to prevent — every failure
// mode here is reported back to the caller as RejectBadPath instead of
// quietly coerced into something "safe".
func validatePath(p string) *Reject {
	reject := func(msg string) *Reject {
		return &Reject{Code: RejectBadPath, File: p, Message: msg}
	}

	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return reject("path is empty")
	}
	if strings.Contains(p, "\\") {
		return reject("path must not contain a backslash")
	}
	if filepath.IsAbs(p) {
		return reject("path must not be absolute")
	}
	if strings.HasPrefix(p, "/") {
		return reject("path must not have a leading slash")
	}
	if strings.Contains(p, "//") {
		return reject("path must not contain a doubled slash")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return reject("path must not contain a \".\" or \"..\" segment")
		}
	}
	if path.Clean(p) != p {
		return reject("path is not in canonical form")
	}
	return nil
}
