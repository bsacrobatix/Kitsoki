package graph

// canonicalize_autoheal_test.go pins the contract that replaced the
// NEEDS_CANONICALIZATION write block: a non-canonical but
// semantically-loadable catalog must never hard-block a write on any
// surface, and nothing may be reformatted without the result saying so.
//
// The scenario under test is the one that wedged POG in production: a human
// hand-wrapped a long block scalar in the catalog, and from that moment
// every propose — including validate_only — was rejected until someone ran
// the CLI canonicalizer with a binary whose writer format matched the
// catalog's pin.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/clock"
)

// statementOf reads the req-block node's folded block scalar straight out of
// the file's YAML, which is what "the semantics survived the reformat" has
// to mean: the string value, not the bytes that encode it.
func statementOf(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Nodes []map[string]any `yaml:"nodes"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, n := range doc.Nodes {
		if id, _ := n["id"].(string); id == "req-block" {
			s, _ := n["statement"].(string)
			return s
		}
	}
	t.Fatalf("req-block not found in %s", path)
	return ""
}

// TestPropose_AutoHealsNonCanonicalCatalog is the headline regression: the
// hand-wrapped-block-scalar catalog that used to freeze every write now
// takes the write, heals itself in the same commit, and says so.
func TestPropose_AutoHealsNonCanonicalCatalog(t *testing.T) {
	root := writeBlockScalarFixture(t)
	statementBefore := statementOf(t, root)

	res, err := Propose(root, ProposeInput{
		Title: "Propose against a catalog a human left non-canonical",
		Operations: []map[string]any{
			{"kind": "added", "after": map[string]any{"schema": "graph/requirement/v0", "id": "req-heals", "title": "Lands despite the block scalar", "status": "draft", "visibility": "internal"}},
		},
	}, "", clock.Real())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(res.RejectReasons) > 0 {
		t.Fatalf("a non-canonical catalog must never block a propose, got: %v", res.RejectReasons)
	}
	if res.ChangesetID == "" {
		t.Fatal("expected a changeset id from a successful propose")
	}

	// The heal is reported, not silent.
	if !res.Canonicalized {
		t.Error("Canonicalized must be true when the write reformatted a file")
	}
	if len(res.CanonicalizedFiles) != 1 || res.CanonicalizedFiles[0] != "catalog.yaml" {
		t.Errorf("CanonicalizedFiles = %v, want [catalog.yaml]", res.CanonicalizedFiles)
	}

	// The catalog on disk is now canonical, and the proposal landed.
	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if files, problems := nonCanonicalCatalogFiles(cat); len(files) > 0 || len(problems) > 0 {
		t.Errorf("catalog should be canonical after the healing write: files=%v problems=%v", files, problems)
	}
	if _, ok := cat.Nodes[res.ChangesetID]; !ok {
		t.Error("changeset node missing after a healing propose")
	}
	if _, ok := cat.Nodes["req-block"]; !ok {
		t.Error("pre-existing req-block node lost during the heal")
	}

	// Semantics identical: the reformat changed encoding, not meaning.
	if got := statementOf(t, root); got != statementBefore {
		t.Errorf("block scalar value changed across the heal:\n before: %q\n  after: %q", statementBefore, got)
	}
}

// TestPropose_ValidateOnlyOnNonCanonicalCatalogSucceeds: validate_only must
// actually validate. Rejecting a dry-run check because of file formatting
// was the most useless form of the old block — it told an agent "you cannot
// even ask" — so it now reports the heal that a real write would perform,
// and writes nothing.
func TestPropose_ValidateOnlyOnNonCanonicalCatalogSucceeds(t *testing.T) {
	root := writeBlockScalarFixture(t)
	before, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Propose(root, ProposeInput{
		Title:        "Validate against a non-canonical catalog",
		ValidateOnly: true,
		Operations: []map[string]any{
			{"kind": "added", "after": map[string]any{"schema": "graph/requirement/v0", "id": "req-validated", "title": "Never written", "status": "draft", "visibility": "internal"}},
		},
	}, "", clock.Real())
	if err != nil {
		t.Fatalf("Propose(validate_only): %v", err)
	}
	if len(res.RejectReasons) > 0 {
		t.Fatalf("validate_only must never reject on formatting alone, got: %v", res.RejectReasons)
	}
	if !res.ValidatedOnly {
		t.Error("expected ValidatedOnly to be set")
	}
	if !res.Canonicalized || len(res.CanonicalizedFiles) != 1 {
		t.Errorf("validate_only should report the heal it WOULD perform, got Canonicalized=%v files=%v", res.Canonicalized, res.CanonicalizedFiles)
	}

	after, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("validate_only wrote to disk")
	}
}

// TestLifecycle_AutoHealsOnceThenReportsClean walks the full
// propose -> authorize -> apply lifecycle on a non-canonical catalog. Every
// verb must be unblocked, and only the first write should report a heal:
// after that the catalog is canonical and there is nothing left to tidy.
func TestLifecycle_AutoHealsOnceThenReportsClean(t *testing.T) {
	root := writeBlockScalarFixture(t)
	statementBefore := statementOf(t, root)

	pres, err := Propose(root, ProposeInput{
		Title: "Lifecycle over a non-canonical catalog",
		Operations: []map[string]any{
			{"kind": "added", "after": map[string]any{"schema": "graph/requirement/v0", "id": "req-lifecycle", "title": "Applied end to end", "status": "draft", "visibility": "internal"}},
		},
	}, "", clock.Real())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(pres.RejectReasons) > 0 {
		t.Fatalf("Propose rejected: %v", pres.RejectReasons)
	}
	if !pres.Canonicalized {
		t.Error("the first write should have reported the heal")
	}

	ares, err := Authorize(root, pres.ChangesetID, "", clock.Real())
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if ares.Rejected() {
		t.Fatalf("Authorize rejected: %v / %v", ares.RejectReasons, ares.LintIssues)
	}
	if ares.Canonicalized {
		t.Errorf("nothing left to canonicalize by authorize time, got: %v", ares.CanonicalizedFiles)
	}

	apres, err := Apply(root, pres.ChangesetID, false, "", clock.Real())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if apres.Rejected() {
		t.Fatalf("Apply rejected: %v / %v", apres.RejectReasons, apres.LintIssues)
	}
	if !apres.Applied {
		t.Error("expected Applied")
	}

	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := cat.Nodes["req-lifecycle"]; !ok {
		t.Error("req-lifecycle never landed")
	}
	if got := statementOf(t, root); got != statementBefore {
		t.Errorf("block scalar value changed across the lifecycle:\n before: %q\n  after: %q", statementBefore, got)
	}
}

// TestWithdrawAndRebase_UnblockedByAutoHeal covers the two remaining
// lifecycle verbs, so "never blocks on any verb" isn't an inference from
// three of five.
func TestWithdrawAndRebase_UnblockedByAutoHeal(t *testing.T) {
	for _, tc := range []struct {
		name string
		// prepare puts the catalog into the state the verb needs, on top of
		// the seeded changeset, before the file is re-wrapped by hand.
		prepare func(t *testing.T, root string)
		run     func(t *testing.T, root string, id NodeID) *ApplyResult
	}{
		{
			name:    "withdraw",
			prepare: func(*testing.T, string) {},
			run: func(t *testing.T, root string, id NodeID) *ApplyResult {
				res, err := Withdraw(root, id, "", clock.Real())
				if err != nil {
					t.Fatalf("Withdraw: %v", err)
				}
				return res
			},
		},
		{
			name: "rebase",
			// Rebase only acts on a changeset with a STALE Before guard, so
			// move the live node out from under the seeded proposal first.
			prepare: func(t *testing.T, root string) {
				rewriteFile(t, root,
					"title: Requirement with a block scalar",
					"title: Requirement retitled out of band")
			},
			run: func(t *testing.T, root string, id NodeID) *ApplyResult {
				res, err := Rebase(root, id, "", clock.Real())
				if err != nil {
					t.Fatalf("Rebase: %v", err)
				}
				return res
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeBlockScalarFixture(t)
			// Seed a changeset, then restore the file's non-canonical form so
			// the verb under test is the one facing the un-healed catalog.
			pres, err := Propose(root, ProposeInput{
				Title: "Seed for " + tc.name,
				Operations: []map[string]any{
					{"kind": "modified", "node": "req-block", "changes": []any{
						map[string]any{"path": []any{"title"}, "after": "Retitled"},
					}},
				},
			}, "", clock.Real())
			if err != nil {
				t.Fatalf("seed Propose: %v", err)
			}
			if len(pres.RejectReasons) > 0 {
				t.Fatalf("seed Propose rejected: %v", pres.RejectReasons)
			}
			tc.prepare(t, root)
			unCanonicalize(t, root)

			res := tc.run(t, root, pres.ChangesetID)
			for _, r := range res.RejectReasons {
				if strings.Contains(r, "NEEDS_CANONICALIZATION") {
					t.Fatalf("%s blocked on canonicality: %v", tc.name, res.RejectReasons)
				}
			}
			if !res.Canonicalized {
				t.Errorf("%s should have reported healing the catalog, got %+v", tc.name, res)
			}
		})
	}
}

// rewriteFile performs one literal substitution on a catalog file, failing
// the test if the fixture drifted and the substitution matched nothing.
func rewriteFile(t *testing.T, path, old, new string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Replace(string(raw), old, new, 1)
	if out == string(raw) {
		t.Fatalf("fixture drifted — %q not found in %s", old, path)
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

// unCanonicalize re-wraps the catalog's folded block scalar by hand, putting
// the file back into exactly the state a human editor leaves it in.
func unCanonicalize(t *testing.T, root string) {
	t.Helper()
	rewriteFile(t, root,
		"yaml.v3's re-marshal collapses this onto one line, the exact reflow hazard this guard exists to catch.",
		"yaml.v3's re-marshal collapses this onto one line,\n      the exact reflow hazard this guard exists to catch.")
	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload after un-canonicalizing: %v", err)
	}
	if files, _ := nonCanonicalCatalogFiles(cat); len(files) == 0 {
		t.Fatal("expected the re-wrapped file to be non-canonical again")
	}
}

// TestAutoHeal_RefusesWhenCanonicalFormWouldChangeMeaning is the one case
// that still fails closed. It is only reachable through the tamper hook:
// yaml.v3's emitter falls back to a quoted style for any string block style
// cannot round-trip, so a well-formed file cannot actually produce a
// semantics-changing re-marshal. The refusal is a proof obligation — if
// that invariant ever breaks, a write must stop rather than corrupt the
// catalog — and the message has to name the field so the corruption is
// findable.
func TestAutoHeal_RefusesWhenCanonicalFormWouldChangeMeaning(t *testing.T) {
	root := writeBlockScalarFixture(t)
	before, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}

	canonicalizeTamperHook = func(_ string, canonical []byte) []byte {
		return []byte(strings.Replace(string(canonical),
			"status: draft", "status: published", 1))
	}
	t.Cleanup(func() { canonicalizeTamperHook = nil })

	res, err := Propose(root, ProposeInput{
		Title: "Must refuse rather than corrupt",
		Operations: []map[string]any{
			{"kind": "added", "after": map[string]any{"schema": "graph/requirement/v0", "id": "req-corrupt", "title": "Never lands", "status": "draft", "visibility": "internal"}},
		},
	}, "", clock.Real())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(res.RejectReasons) != 1 {
		t.Fatalf("expected exactly one refusal reason, got: %v", res.RejectReasons)
	}
	reason := res.RejectReasons[0]
	if !strings.Contains(reason, "would change this file's meaning") {
		t.Errorf("refusal must explain itself, got: %s", reason)
	}
	// The message names the exact node and field that diverged.
	if !strings.Contains(reason, "$.nodes[req-block].status") {
		t.Errorf("refusal must name the diverging node/field, got: %s", reason)
	}
	if d := ClassifyRejectReason(reason); d.Code != "needs_canonicalization" {
		t.Errorf("refusal should classify as needs_canonicalization, got %q", d.Code)
	}

	after, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a refused write must leave the catalog byte-for-byte untouched")
	}
}

// TestAutoHeal_NeverLandsOutsideTheCASTransaction: the heal is committed by
// the operation's own CAS-guarded copy-back and by nothing else. Under a
// concurrent writer that never yields, the whole operation — heal included
// — must roll back, leaving the catalog exactly as non-canonical as it was.
// A heal that wrote itself out separately would leave the file reformatted
// even though the operation failed.
func TestAutoHeal_NeverLandsOutsideTheCASTransaction(t *testing.T) {
	root := writeBlockScalarFixture(t)

	casTestHook = func() {
		raw, err := os.ReadFile(root)
		if err != nil {
			t.Fatalf("read during hook: %v", err)
		}
		if err := os.WriteFile(root, append(raw, []byte("\n# concurrent writer, every attempt\n")...), 0o644); err != nil {
			t.Fatalf("write during hook: %v", err)
		}
	}
	t.Cleanup(func() { casTestHook = nil })

	res, err := Propose(root, ProposeInput{
		Title: "Loses every CAS race",
		Operations: []map[string]any{
			{"kind": "added", "after": map[string]any{"schema": "graph/requirement/v0", "id": "req-never", "title": "Never lands", "status": "draft", "visibility": "internal"}},
		},
	}, "", clock.Real())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(res.RejectReasons) != 1 || !strings.HasPrefix(res.RejectReasons[0], "CONFLICT:") {
		t.Fatalf("expected a CONFLICT reject, got: %v", res.RejectReasons)
	}

	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if files, _ := nonCanonicalCatalogFiles(cat); len(files) == 0 {
		t.Error("the heal landed even though the operation rolled back — it must ride the CAS-guarded copy-back, nothing else")
	}
	if _, ok := cat.Nodes["req-never"]; ok {
		t.Error("req-never must not have been written")
	}
}

// TestCanonicalOutputFormatIsStable guards the writer-format compatibility
// contract downstream repos pin binaries against: canonicalizing an
// already-canonical file must be a byte-for-byte no-op, on both the explicit
// and the auto-heal paths. If a yaml.v3 upgrade or an indent change ever
// shifts the canonical form, this fails instead of silently reformatting
// every pinned catalog on its next write.
func TestCanonicalOutputFormatIsStable(t *testing.T) {
	root := writeBlockScalarFixture(t)
	if _, err := Canonicalize(root, false); err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	canonical, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}

	// The detector agrees there is nothing left to do...
	if out, needs, err := canonicalRewrite(canonical); err != nil || needs {
		t.Fatalf("canonicalRewrite on canonical bytes: needs=%v err=%v (would rewrite to %d bytes)", needs, err, len(out))
	}

	// ...the explicit path is a no-op...
	res, err := Canonicalize(root, false)
	if err != nil {
		t.Fatalf("second Canonicalize: %v", err)
	}
	if len(res.ChangedFiles) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("re-canonicalizing a canonical file must change nothing, got: %+v", res)
	}

	// ...and so is the auto-heal inside a real write. (The propose still
	// rewrites the file to add its changeset node; what must not happen is
	// the heal claiming a reformat.)
	pres, err := Propose(root, ProposeInput{
		Title: "Write against an already-canonical catalog",
		Operations: []map[string]any{
			{"kind": "added", "after": map[string]any{"schema": "graph/requirement/v0", "id": "req-stable", "title": "No reformat expected", "status": "draft", "visibility": "internal"}},
		},
	}, "", clock.Real())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(pres.RejectReasons) > 0 {
		t.Fatalf("Propose rejected: %v", pres.RejectReasons)
	}
	if pres.Canonicalized || len(pres.CanonicalizedFiles) != 0 {
		t.Errorf("an already-canonical catalog must not report a heal, got %+v", pres.CanonicalizedFiles)
	}

	// The pre-existing region of the file is byte-identical; only the
	// appended changeset node is new.
	afterWrite, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(afterWrite), string(canonical)) {
		t.Error("writing to a canonical catalog reflowed pre-existing content instead of appending")
	}
}

// TestPropose_AutoHealsOneFileOfABundleCatalog: a bundle catalog spreads
// nodes across several files, and only the ones a human actually mangled
// may be rewritten. This also exercises the multi-file scratch mapping —
// heal and edits address the same staged bytes, and both reach copy-back.
func TestPropose_AutoHealsOneFileOfABundleCatalog(t *testing.T) {
	root := copyBundleFixture(t)
	mangled := filepath.Join(root, "nodes", "requirements.yaml")
	untouched := filepath.Join(root, "nodes", "features.yaml")

	// Give req-one a hand-wrapped folded block scalar.
	rewriteFile(t, mangled,
		"  statement: A generic requirement statement.",
		"  statement: >-\n    A generic requirement statement that a human\n    hand-wrapped across two lines.")
	featuresBefore, err := os.ReadFile(untouched)
	if err != nil {
		t.Fatal(err)
	}

	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if files, _ := nonCanonicalCatalogFiles(cat); len(files) != 1 || files[0] != mangled {
		t.Fatalf("expected only requirements.yaml to be non-canonical, got %v", files)
	}

	res, err := Propose(root, ProposeInput{
		Title: "Propose into a bundle with one mangled file",
		Operations: []map[string]any{
			{"kind": "modified", "node": "feature-one", "changes": []any{
				map[string]any{"path": []any{"title"}, "after": "Feature one, renamed"},
			}},
		},
	}, "", clock.Real())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(res.RejectReasons) > 0 {
		t.Fatalf("bundle propose rejected: %v", res.RejectReasons)
	}
	if !res.Canonicalized {
		t.Fatal("expected the heal to be reported")
	}
	want := filepath.Join("nodes", "requirements.yaml")
	if len(res.CanonicalizedFiles) != 1 || res.CanonicalizedFiles[0] != want {
		t.Errorf("CanonicalizedFiles = %v, want [%s]", res.CanonicalizedFiles, want)
	}

	// The mangled file was healed on disk...
	reloaded, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if files, _ := nonCanonicalCatalogFiles(reloaded); len(files) != 0 {
		t.Errorf("expected a canonical bundle after the write, got %v", files)
	}
	// ...and its sibling, which the changeset never touched and which had
	// nothing wrong with it, is byte-for-byte untouched.
	featuresAfter, err := os.ReadFile(untouched)
	if err != nil {
		t.Fatal(err)
	}
	if string(featuresBefore) != string(featuresAfter) {
		t.Error("an unrelated, already-canonical bundle file was rewritten")
	}
}

// TestSemanticDivergence covers the proof itself: identical documents pass,
// and a changed value is reported at a human-addressable path.
func TestSemanticDivergence(t *testing.T) {
	base := []byte("nodes:\n  - id: a\n    title: one\n  - id: b\n    title: two\n")
	same := []byte("nodes:\n- id: a\n  title: one\n- id: b\n  title: two\n")
	changed := []byte("nodes:\n  - id: a\n    title: one\n  - id: b\n    title: TWO\n")
	missing := []byte("nodes:\n  - id: a\n    title: one\n")

	if diff, err := semanticDivergence(base, same); err != nil || diff != "" {
		t.Errorf("pure reformatting must not diverge, got %q (err %v)", diff, err)
	}
	diff, err := semanticDivergence(base, changed)
	if err != nil {
		t.Fatal(err)
	}
	if diff != "$.nodes[b].title" {
		t.Errorf("diff path = %q, want $.nodes[b].title", diff)
	}
	diff, err = semanticDivergence(base, missing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(diff, "$.nodes (") {
		t.Errorf("a dropped entry should report the sequence length change, got %q", diff)
	}
}
