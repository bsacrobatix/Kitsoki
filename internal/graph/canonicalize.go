package graph

// canonicalize.go owns everything about "canonical re-marshal form": the
// detector, the semantic-equality proof, the in-scratch auto-heal every
// lifecycle verb runs, and the explicit out-of-band Canonicalize entry point
// (CLI `kitsoki graph canonicalize`, the graph.canonicalize RPC/MCP tool).
//
// The hazard is real and unchanged: applyOperations re-marshals every file
// it touches whole, so a file whose bytes differ from marshalYAMLNode's
// output would get reformatted as a side effect of an unrelated changeset —
// a surprise whole-file diff smuggled into someone else's review.
//
// What changed is the remedy. Refusing the write (the old
// NEEDS_CANONICALIZATION fail-closed guard) protected reviewers from a
// silent reformat by freezing the entire write path — agent proposals,
// portal writes, even validate_only checks — until a human ran the CLI with
// a binary whose writer format matched the catalog's pin. One human touch to
// a catalog file wedged everything. So the guard now heals instead of
// refusing: canonicalizeScratch rewrites the offending files inside the
// same scratch tree the operation is already building, proves the rewrite
// is semantics-preserving, and lets them ride the operation's existing
// atomic copy-back. The reformat still happens exactly once, still shows up
// in git diff, and is now also named in the operation's result
// (Canonicalized/CanonicalizedFiles) — visible, but never blocking.
//
// Fail-closed survives where it actually matters: if the canonical
// re-marshal would change what the file MEANS (semanticDivergence finds a
// value mismatch), the operation refuses and names the diverging path.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// CanonicalizeResult reports what Canonicalize did: the files it rewrote
// into canonical re-marshal form, and the files it could not write (e.g. a
// chmod-locked catalog) — surfaced as skips rather than a hard error so one
// read-only file doesn't abort canonicalizing its writable siblings.
type CanonicalizeResult struct {
	ChangedFiles []string
	Skipped      []string // "path: reason" entries for files that needed canonicalization but couldn't be written
}

// Canonicalize rewrites every file backing the catalog at rootPath that
// canonicalRewrite flags — a block-scalar-bearing file whose bytes differ
// from marshalYAMLNode's output — into that canonical form, via an atomic
// same-directory temp+rename preserving the original file mode. Files with
// no block scalars, or already canonical, are left byte-for-byte untouched.
// Idempotent: a second run changes nothing. With dryRun, ChangedFiles
// reports what WOULD be rewritten and nothing touches disk.
//
// This is now the *explicit* form of what every lifecycle verb already does
// on its own behalf (canonicalizeScratch): it exists so an operator or agent
// can land the reflow as its own reviewable commit rather than having it
// ride along with the next propose. A file whose canonical form would not
// be semantics-preserving is reported as a skip, never rewritten.
func Canonicalize(rootPath string, dryRun bool) (*CanonicalizeResult, error) {
	cat, err := LoadCatalog(rootPath)
	if err != nil {
		return nil, fmt.Errorf("canonicalize: %w", err)
	}
	files, err := catalogFiles(cat)
	if err != nil {
		return nil, fmt.Errorf("canonicalize: enumerate catalog files: %w", err)
	}
	res := &CanonicalizeResult{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		out, needs, err := canonicalRewrite(raw)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: re-marshal failed: %v", f, err))
			continue
		}
		if !needs {
			continue
		}
		if diverged, err := semanticDivergence(raw, out); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", f, err))
			continue
		} else if diverged != "" {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %s", f, divergenceMessage(diverged)))
			continue
		}
		if !dryRun {
			if err := writeFileAtomic(f, out); err != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", f, err))
				continue
			}
		}
		res.ChangedFiles = append(res.ChangedFiles, f)
	}
	return res, nil
}

// canonicalRewrite reports raw's canonical re-marshal form and whether the
// file actually needs rewriting. needs is false — and out nil — for the
// three exempt cases:
//
//   - the bytes don't parse as YAML at all. LoadCatalog already succeeded by
//     the time anything calls this, so a re-parse quirk here is not this
//     layer's problem to adjudicate.
//   - the file contains no literal (`|`) or folded (`>`) block scalar. That
//     is the only class of formatting whose re-marshal reflows meaningfully;
//     everything else (flow-mapping padding, quote style, ...) differs
//     cosmetically in virtually every hand-authored catalog, and rewriting it
//     would churn diffs for nothing.
//   - the bytes are already byte-identical to the re-marshal.
//
// The exemption set is deliberately identical to what the old fail-closed
// guard flagged, so switching from "refuse" to "heal" changed which files
// get acted on by exactly zero — and, per the writer-format compatibility
// contract downstream repos pin against, the canonical bytes themselves are
// still whatever marshalYAMLNode produces. Nothing about the output format
// moved.
func canonicalRewrite(raw []byte) (out []byte, needs bool, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, false, nil
	}
	if !hasBlockScalar(&doc) {
		return nil, false, nil
	}
	out, err = marshalYAMLNode(&doc)
	if err != nil {
		return nil, false, err
	}
	if bytes.Equal(raw, out) {
		return nil, false, nil
	}
	return out, true, nil
}

// hasBlockScalar reports whether n (or any descendant) is a literal (`|`)
// or folded (`>`) style scalar node — the class of YAML formatting whose
// re-marshal can change meaning-bearing line-wrap width, as opposed to the
// purely cosmetic flow-style/quoting differences elsewhere in a
// hand-authored file.
func hasBlockScalar(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.ScalarNode && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return true
	}
	for _, c := range n.Content {
		if hasBlockScalar(c) {
			return true
		}
	}
	return false
}

// canonicalizeTamperHook, when non-nil, rewrites the canonical bytes a heal
// is about to prove semantics-preserving — the test-only seam (same
// convention as guards.go's casTestHook) that makes the semantic-divergence
// refusal path reachable from a test. In practice yaml.v3's emitter refuses
// to emit block style for any string it cannot round-trip (it falls back to
// a quoted style), so a genuine divergence out of a well-formed file is not
// constructible through the real encoder; the refusal is a proof
// obligation, not an expected outcome. Nil in production.
var canonicalizeTamperHook func(file string, canonical []byte) []byte

// canonicalizeScratch is the auto-heal: for every file backing cat, it
// rewrites that file's copy INSIDE scratchRoot into canonical form, first
// proving the rewrite preserves the file's parsed YAML value. It returns
// the healed files in the same "relative to the catalog root" shape
// applyOperations returns, so the two lists merge directly into one
// changed-files set for copy-back, plus any refusal reasons.
//
// Nothing here touches the real catalog. The heal lands on disk only when
// the caller's commitWithCAS copies the scratch tree back, which means it
// is covered by the same content-digest guard and the same commit window as
// the operation's own edits: a concurrent writer either loses the CAS race
// (and the whole operation, heal included, is retried against a fresh load)
// or wins it — never observing a half-healed catalog.
//
// Reasons keep the historical "NEEDS_CANONICALIZATION:" prefix so every
// existing classifier — graphsrv's classifyWriteReject, the RPC carrier's
// ClassifyRejectReason, the portal's fix-it affordance — keeps working.
// What changed is that the prefix now only ever appears for a genuinely
// dangerous condition (unreadable file, un-marshalable document, or a
// semantics-changing re-marshal), never for "this file merely needs
// reformatting".
func canonicalizeScratch(cat *Catalog, scratchRoot string) (healed []string, reasons []string) {
	files, err := catalogFiles(cat)
	if err != nil {
		return nil, []string{fmt.Sprintf("NEEDS_CANONICALIZATION: failed to enumerate catalog files: %v", err)}
	}
	for _, f := range files {
		scratchFile, err := scratchPathFor(cat, scratchRoot, f)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("NEEDS_CANONICALIZATION: %s: %v", f, err))
			continue
		}
		raw, err := os.ReadFile(scratchFile)
		if err != nil {
			if os.IsNotExist(err) {
				// The scratch tree stages exactly what catalogFiles
				// enumerates, so this means the real file vanished between
				// load and copy — commitWithCAS's digest re-check owns that
				// race, not this heal.
				continue
			}
			reasons = append(reasons, fmt.Sprintf("NEEDS_CANONICALIZATION: %s: %v", f, err))
			continue
		}
		out, needs, err := canonicalRewrite(raw)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("NEEDS_CANONICALIZATION: %s: re-marshal failed: %v", f, err))
			continue
		}
		if !needs {
			continue
		}
		if canonicalizeTamperHook != nil {
			out = canonicalizeTamperHook(f, out)
		}
		diverged, err := semanticDivergence(raw, out)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("NEEDS_CANONICALIZATION: %s: %v", f, err))
			continue
		}
		if diverged != "" {
			reasons = append(reasons, fmt.Sprintf("NEEDS_CANONICALIZATION: %s: %s", f, divergenceMessage(diverged)))
			continue
		}
		if err := os.WriteFile(scratchFile, out, 0o644); err != nil {
			reasons = append(reasons, fmt.Sprintf("NEEDS_CANONICALIZATION: %s: %v", f, err))
			continue
		}
		rel, err := scratchRelPath(scratchRoot, scratchFile)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("NEEDS_CANONICALIZATION: %s: %v", f, err))
			continue
		}
		healed = append(healed, rel)
	}
	sort.Strings(healed)
	return healed, reasons
}

// divergenceMessage renders the one refusal a canonicalizing write may
// still make. It names the exact path so a reader can go look at that
// node/field instead of eyeballing a 1700-line diff.
func divergenceMessage(path string) string {
	return fmt.Sprintf(
		"refusing to canonicalize: the canonical re-marshal would change this file's meaning at %s — "+
			"the parsed value differs before and after re-serialization. That is a YAML round-trip "+
			"defect, not a formatting condition: rewrite (or simplify) that field by hand rather than "+
			"forcing the write.", path)
}

// nonCanonicalCatalogFiles reports which of cat's files are not in
// canonical re-marshal form (the set canonicalizeScratch would heal) and
// any files it could not assess. Diagnostic only: nothing in the write path
// consults it, because the write path heals rather than asks.
func nonCanonicalCatalogFiles(cat *Catalog) (files []string, problems []string) {
	all, err := catalogFiles(cat)
	if err != nil {
		return nil, []string{fmt.Sprintf("failed to enumerate catalog files: %v", err)}
	}
	for _, f := range all {
		raw, err := os.ReadFile(f)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		_, needs, err := canonicalRewrite(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: re-marshal failed: %v", f, err))
			continue
		}
		if needs {
			files = append(files, f)
		}
	}
	return files, problems
}

// semanticDivergence is the proof obligation behind every auto-heal: raw
// and canonical must parse to the same YAML value. It returns "" when they
// do, or a path naming the first divergence.
//
// Comparing the two files' parsed YAML documents is strictly stronger than
// comparing load(original) with load(canonicalized): the loader projects a
// catalog file down to nodes/edges/fields and drops whatever it doesn't
// model, so two files could load identically while differing in bytes the
// loader ignores. This compares the whole document, including everything
// the loader would have thrown away.
func semanticDivergence(raw, canonical []byte) (string, error) {
	var before, after any
	if err := yaml.Unmarshal(raw, &before); err != nil {
		return "", fmt.Errorf("re-parse original for semantic comparison: %w", err)
	}
	if err := yaml.Unmarshal(canonical, &after); err != nil {
		return "", fmt.Errorf("re-parse canonical form for semantic comparison: %w", err)
	}
	if path, differs := yamlValueDiff(before, after, "$"); differs {
		return path, nil
	}
	return "", nil
}

// yamlValueDiff walks two decoded YAML values in lockstep and returns the
// path of the first difference. Sequence elements are addressed by their
// `id` key when they have one ("$.nodes[req-block].statement") rather than
// by index, because a catalog's sequences are node lists and the id is what
// a human can actually search for.
func yamlValueDiff(a, b any, path string) (string, bool) {
	am, aIsMap := asStringKeyedMap(a)
	bm, bIsMap := asStringKeyedMap(b)
	if aIsMap || bIsMap {
		if !aIsMap || !bIsMap {
			return path, true
		}
		for _, k := range unionKeys(am, bm) {
			av, aok := am[k]
			bv, bok := bm[k]
			child := path + "." + k
			if aok != bok {
				return child, true
			}
			if p, differs := yamlValueDiff(av, bv, child); differs {
				return p, true
			}
		}
		return "", false
	}

	as, aIsSeq := a.([]any)
	bs, bIsSeq := b.([]any)
	if aIsSeq || bIsSeq {
		if !aIsSeq || !bIsSeq {
			return path, true
		}
		if len(as) != len(bs) {
			return fmt.Sprintf("%s (%d entries before, %d after)", path, len(as), len(bs)), true
		}
		for i := range as {
			child := fmt.Sprintf("%s[%d]", path, i)
			if id := sequenceElementID(as[i]); id != "" {
				child = fmt.Sprintf("%s[%s]", path, id)
			}
			if p, differs := yamlValueDiff(as[i], bs[i], child); differs {
				return p, true
			}
		}
		return "", false
	}

	if !reflect.DeepEqual(a, b) {
		return path, true
	}
	return "", false
}

// asStringKeyedMap normalizes both mapping shapes yaml.v3 can decode into
// (map[string]any for all-string keys, map[any]any otherwise) to one
// string-keyed map, so the walk doesn't branch per shape.
func asStringKeyedMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out, true
	default:
		return nil, false
	}
}

func unionKeys(a, b map[string]any) []string {
	seen := make(map[string]bool, len(a)+len(b))
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// sequenceElementID pulls a sequence element's "id" scalar, when it has
// one, for human-addressable diff paths.
func sequenceElementID(v any) string {
	m, ok := asStringKeyedMap(v)
	if !ok {
		return ""
	}
	id, ok := m["id"].(string)
	if !ok || id == "" || strings.ContainsAny(id, "[]") {
		return ""
	}
	return id
}

// writeFileAtomic replaces path's content via a same-directory temp file +
// rename, preserving the original mode. A read-only target is refused up
// front the same way a direct write would fail, without ever leaving a
// half-written catalog file behind.
func writeFileAtomic(path string, content []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if mode&0o200 == 0 {
		return fmt.Errorf("file is read-only (mode %v)", info.Mode())
	}
	return replaceFileAtomic(path, content, mode)
}

// replaceFileAtomic writes content to path via a same-directory temp file +
// rename at the given mode. Splitting it out from writeFileAtomic lets the
// commit path (which must also create files that don't exist yet) reuse the
// same "a reader sees either the old file or the new one, never a partial
// write" guarantee.
func replaceFileAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".canon-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// RejectDetail is the structured form of a lifecycle-verb reject reason —
// the classification the MCP surface has always had (graphsrv's
// classifyWriteReject) but the bare graph.* RPC carrier lacked, leaving
// browser clients to regex raw strings. Code is one of
// "needs_canonicalization" (a catalog file could not be read, parsed, or
// safely re-serialized — no longer merely "needs reformatting", which now
// heals itself), "conflict" (a stale Before guard or concurrent write), or
// "validation" (everything else).
type RejectDetail struct {
	Code    string `json:"code"`
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}

// ClassifyRejectReason maps one raw reject-reason string to its structured
// form. NEEDS_CANONICALIZATION reasons carry the offending file path as the
// second colon-delimited segment (see canonicalizeScratch's fmt.Sprintf
// shapes).
func ClassifyRejectReason(reason string) RejectDetail {
	switch {
	case strings.HasPrefix(reason, "NEEDS_CANONICALIZATION:"):
		d := RejectDetail{Code: "needs_canonicalization", Message: reason}
		// "<file>: <detail>" — the file segment ends at the first ": ".
		// The enumerate-failure variant has no file path; leaving File
		// empty is correct there.
		rest := strings.TrimLeft(strings.TrimPrefix(reason, "NEEDS_CANONICALIZATION:"), " ")
		if i := strings.Index(rest, ": "); i > 0 {
			candidate := rest[:i]
			if ext := filepath.Ext(candidate); ext == ".yaml" || ext == ".yml" {
				d.File = candidate
			}
		}
		return d
	case strings.HasPrefix(reason, "CONFLICT:"):
		return RejectDetail{Code: "conflict", Message: reason}
	default:
		return RejectDetail{Code: "validation", Message: reason}
	}
}
