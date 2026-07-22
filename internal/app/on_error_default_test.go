package app

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOnErrorDefault_FillsEmptyOnError asserts that a story-level
// on_error_default: populates an invoke: effect's on_error: when the author
// left it empty, and that the runtime-facing Effect.OnError is byte-identical
// to what a hand-authored on_error: would have produced.
func TestOnErrorDefault_FillsEmptyOnError(t *testing.T) {
	yaml := []byte(`app:
  id: on-error-default-fill
  version: 0.1.0
hosts: [host.run]
on_error_default: recover
root: start
states:
  start:
    on_enter:
      - invoke: host.run
  recover:
    terminal: true
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	require.Equal(t, "recover", def.States["start"].OnEnter[0].OnError)
}

// TestOnErrorDefault_PerInvokeWins asserts a per-invoke on_error: is never
// overwritten by the story-level default.
func TestOnErrorDefault_PerInvokeWins(t *testing.T) {
	yaml := []byte(`app:
  id: on-error-default-override
  version: 0.1.0
hosts: [host.run]
on_error_default: recover
root: start
states:
  start:
    on_enter:
      - invoke: host.run
        on_error: specific_handler
  recover:
    terminal: true
  specific_handler:
    terminal: true
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	require.Equal(t, "specific_handler", def.States["start"].OnEnter[0].OnError)
}

// TestOnErrorDefault_UnresolvableTargetIsLoadError asserts that a declared
// on_error_default: which does not resolve to a real state fails the load,
// mirroring how a bad transition target is reported.
func TestOnErrorDefault_UnresolvableTargetIsLoadError(t *testing.T) {
	yaml := []byte(`app:
  id: on-error-default-bad-target
  version: 0.1.0
hosts: [host.run]
on_error_default: nowhere
root: start
states:
  start:
    on_enter:
      - invoke: host.run
`)
	_, err := LoadBytes(yaml)
	require.Error(t, err)
	require.Contains(t, err.Error(), `on_error_default "nowhere"`)
	require.Contains(t, err.Error(), "does not exist")
}

// TestOnErrorDefault_AppliesAcrossOnCompleteAndTransitionEffects asserts the
// default reaches invoke: effects inside transition effect chains and
// on_complete: continuations, not just on_enter:.
func TestOnErrorDefault_AppliesAcrossOnCompleteAndTransitionEffects(t *testing.T) {
	yaml := []byte(`app:
  id: on-error-default-reach
  version: 0.1.0
hosts: [host.run, host.followup]
on_error_default: recover
intents:
  go: {}
root: start
states:
  start:
    on_enter:
      - invoke: host.run
        background: true
        bind: { job_id: job_id }
        on_complete:
          - invoke: host.followup
    on:
      go:
        - target: done
          effects:
            - invoke: host.run
  done:
    terminal: true
  recover:
    terminal: true
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	require.Equal(t, "recover", def.States["start"].OnEnter[0].OnComplete[0].OnError)
	require.Equal(t, "recover", def.States["start"].On["go"][0].Effects[0].OnError)
}

// TestOnErrorDefault_ImportBoundary_ParentDefaultDoesNotLeakIntoChild
// asserts that an importer's on_error_default: is NOT applied to an
// imported child's own unhandled invoke: sites — the child was already
// fully resolved (with no default of its own here) before it was folded in.
func TestOnErrorDefault_ImportBoundary_ParentDefaultDoesNotLeakIntoChild(t *testing.T) {
	root := t.TempDir()
	childDir := mkdirT(t, root, "child")
	mustWrite(t, childDir, "app.yaml", `app: { id: child, version: 0.1.0 }
hosts: [host.run]
root: idle
states:
  idle:
    on_enter:
      - invoke: host.run
`)
	parentDir := mkdirT(t, root, "parent")
	mustWrite(t, parentDir, "app.yaml", `app: { id: parent, version: 0.1.0 }
on_error_default: recover
intents:
  go: {}
imports:
  bf:
    source: ../child
    entry: idle
root: start
states:
  start:
    on:
      go:
        - target: bf
  recover:
    terminal: true
`)
	def, err := Load(filepath.Join(parentDir, "app.yaml"))
	require.NoError(t, err)
	// The folded child's invoke keeps its OWN (empty) on_error — the
	// parent's default did not reach across the import boundary.
	require.Empty(t, def.States["bf"].States["idle"].OnEnter[0].OnError)
}

// TestOnErrorDefault_ImportBoundary_ChildDefaultDoesNotLeakToParent asserts
// the reverse: a child's own on_error_default: resolves the child's own
// invoke sites (visible after fold, alias-rewritten) but never touches the
// importer's own invoke sites.
func TestOnErrorDefault_ImportBoundary_ChildDefaultDoesNotLeakToParent(t *testing.T) {
	root := t.TempDir()
	childDir := mkdirT(t, root, "child")
	mustWrite(t, childDir, "app.yaml", `app: { id: child, version: 0.1.0 }
hosts: [host.run]
on_error_default: recover
root: idle
states:
  idle:
    on_enter:
      - invoke: host.run
  recover:
    terminal: true
`)
	parentDir := mkdirT(t, root, "parent")
	// on_error_default: none here is deliberate and orthogonal to
	// builtinErrorRoomDefaultOn (builtin_error_room.go): this test's whole
	// point is import-boundary isolation (the child's default must not leak
	// into the parent), not whether the parent itself falls back to the
	// builtin room.
	mustWrite(t, parentDir, "app.yaml", `app: { id: parent, version: 0.1.0 }
hosts: [host.run]
on_error_default: none
intents:
  go: {}
imports:
  bf:
    source: ../child
    entry: idle
root: start
states:
  start:
    on_enter:
      - invoke: host.run
    on:
      go:
        - target: bf
`)
	def, err := Load(filepath.Join(parentDir, "app.yaml"))
	require.NoError(t, err)
	// Child's own invoke got the child's own default, rewritten by fold into
	// the relative-sibling form the import rewriter uses for bare targets
	// (see rewriteChildStateTransitionsAtDepth's rwTarget).
	require.Equal(t, "../recover", def.States["bf"].States["idle"].OnEnter[0].OnError)
	// Parent's own invoke, with no on_error_default of its own, stays empty.
	require.Empty(t, def.States["start"].OnEnter[0].OnError)
}

// TestUnhandledInvokes_FindsUnhandledAndSkipsHandled exercises the
// reportable lint: a known-unhandled invoke: is found; a handled one
// (either by a direct on_error: or by on_error_default: resolution) is not.
func TestUnhandledInvokes_FindsUnhandledAndSkipsHandled(t *testing.T) {
	yaml := []byte(`app:
  id: unhandled-invoke-lint
  version: 0.1.0
hosts: [host.run, host.other]
on_error_default: recover
intents:
  go: {}
root: start
states:
  start:
    on_enter:
      - invoke: host.run
        on_error: recover
    on:
      go:
        - target: done
          effects:
            - invoke: host.other
  done:
    terminal: true
  recover:
    terminal: true
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)

	unhandled := UnhandledInvokes(def, "test.yaml")
	require.Empty(t, unhandled, "on_error_default resolution should have filled every gap before the lint runs")

	// Now the negative case: explicit on_error_default: none, so this stays
	// genuinely unhandled regardless of builtinErrorRoomDefaultOn (see
	// builtin_error_room.go) — one invoke handled by hand, one left bare.
	yaml2 := []byte(`app:
  id: unhandled-invoke-lint-bare
  version: 0.1.0
hosts: [host.run, host.other]
on_error_default: none
intents:
  go: {}
root: start
states:
  start:
    on_enter:
      - invoke: host.run
        on_error: recover
    on:
      go:
        - target: done
          effects:
            - invoke: host.other
  done:
    terminal: true
  recover:
    terminal: true
`)
	def2, err := LoadBytes(yaml2)
	require.NoError(t, err)

	unhandled2 := UnhandledInvokes(def2, "test.yaml")
	require.Len(t, unhandled2, 1)
	require.Equal(t, "host.other", unhandled2[0].Invoke)
	require.Equal(t, "start", unhandled2[0].StatePath)
}

// TestOnErrorDefault_FillsProposalExecuteOnError asserts on_error_default:
// reaches a proposal kind's execute.invoke (and its on_complete: chain) the
// same way it reaches a state-tree invoke:.
func TestOnErrorDefault_FillsProposalExecuteOnError(t *testing.T) {
	yaml := []byte(`app:
  id: proposal-on-error-default-fill
  version: 0.1.0
hosts: [host.run, host.followup]
on_error_default: recover
root: start
states:
  start:
    terminal: true
  recover:
    terminal: true
proposals:
  buy_supplies:
    execute:
      invoke: host.run
      on_complete:
        - invoke: host.followup
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	require.Equal(t, "recover", def.Proposals["buy_supplies"].Execute.OnError)
	require.Equal(t, "recover", def.Proposals["buy_supplies"].Execute.OnComplete[0].OnError)
}

// TestOnErrorDefault_ExplicitProposalOnErrorWins mirrors
// TestOnErrorDefault_PerInvokeWins for the proposal-execute surface: an
// explicit on_error: is never overwritten by the story-level default.
func TestOnErrorDefault_ExplicitProposalOnErrorWins(t *testing.T) {
	yaml := []byte(`app:
  id: proposal-on-error-default-override
  version: 0.1.0
hosts: [host.run]
on_error_default: recover
root: start
states:
  start:
    terminal: true
  recover:
    terminal: true
  specific_handler:
    terminal: true
proposals:
  buy_supplies:
    execute:
      invoke: host.run
      on_error: specific_handler
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	require.Equal(t, "specific_handler", def.Proposals["buy_supplies"].Execute.OnError)
}

// TestProposalExecute_UnresolvableOnErrorIsLoadError asserts a proposal
// kind's execute.on_error: that does not resolve to a real state (and isn't
// "stay"/"back") fails the load, mirroring transition-target validation.
func TestProposalExecute_UnresolvableOnErrorIsLoadError(t *testing.T) {
	yaml := []byte(`app:
  id: proposal-on-error-bad-target
  version: 0.1.0
hosts: [host.run]
root: start
states:
  start:
    terminal: true
proposals:
  buy_supplies:
    execute:
      invoke: host.run
      on_error: nowhere
`)
	_, err := LoadBytes(yaml)
	require.Error(t, err)
	require.Contains(t, err.Error(), `on_error "nowhere"`)
	require.Contains(t, err.Error(), "does not exist")
}

// TestProposalExecute_OnErrorAcceptsStayAndBack asserts the "stay"/"back"
// keywords (same accepted set as on_success) pass load-time validation
// without needing to resolve to a state.
func TestProposalExecute_OnErrorAcceptsStayAndBack(t *testing.T) {
	for _, kw := range []string{"stay", "back"} {
		yaml := []byte(`app:
  id: proposal-on-error-keyword
  version: 0.1.0
hosts: [host.run]
root: start
states:
  start:
    terminal: true
proposals:
  buy_supplies:
    execute:
      invoke: host.run
      on_error: ` + kw + `
`)
		_, err := LoadBytes(yaml)
		require.NoError(t, err, "keyword %q should be accepted", kw)
	}
}

// TestUnhandledInvokes_FlagsProposalExecuteWithoutOnError asserts the lint
// reports a proposal execute (and its on_complete: entries) with no
// effective on_error:, and skips one that has it.
func TestUnhandledInvokes_FlagsProposalExecuteWithoutOnError(t *testing.T) {
	yaml := []byte(`app:
  id: unhandled-invoke-proposal
  version: 0.1.0
hosts: [host.run, host.other, host.followup]
on_error_default: none
root: start
states:
  start:
    terminal: true
  recover:
    terminal: true
proposals:
  buy_supplies:
    execute:
      invoke: host.run
      on_error: recover
      on_complete:
        - invoke: host.followup
  river_strategy:
    execute:
      invoke: host.other
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)

	unhandled := UnhandledInvokes(def, "test.yaml")
	require.Len(t, unhandled, 2)

	byKind := map[string]UnhandledInvoke{}
	for _, u := range unhandled {
		byKind[u.ProposalKind+"/"+u.Location] = u
	}
	found, ok := byKind["buy_supplies/execute.on_complete[0]"]
	require.True(t, ok, "expected buy_supplies on_complete invoke to be flagged")
	require.Equal(t, "host.followup", found.Invoke)

	found2, ok := byKind["river_strategy/execute"]
	require.True(t, ok, "expected river_strategy execute invoke to be flagged")
	require.Equal(t, "host.other", found2.Invoke)
}

// TestProposalExecute_UnresolvableOnSuccessIsLoadError mirrors
// TestProposalExecute_UnresolvableOnErrorIsLoadError for on_success: — the
// field has never had load-time validation before this test; a typo'd
// target must now fail the load the same way a typo'd on_error: does.
func TestProposalExecute_UnresolvableOnSuccessIsLoadError(t *testing.T) {
	yaml := []byte(`app:
  id: proposal-on-success-bad-target
  version: 0.1.0
hosts: [host.run]
root: start
states:
  start:
    terminal: true
proposals:
  buy_supplies:
    execute:
      invoke: host.run
      on_success: nowhere
`)
	_, err := LoadBytes(yaml)
	require.Error(t, err)
	require.Contains(t, err.Error(), `on_success "nowhere"`)
	require.Contains(t, err.Error(), "does not exist")
}

// TestProposalExecute_OnSuccessAcceptsStayAndBack mirrors
// TestProposalExecute_OnErrorAcceptsStayAndBack for on_success:.
func TestProposalExecute_OnSuccessAcceptsStayAndBack(t *testing.T) {
	for _, kw := range []string{"stay", "back"} {
		yaml := []byte(`app:
  id: proposal-on-success-keyword
  version: 0.1.0
hosts: [host.run]
root: start
states:
  start:
    terminal: true
proposals:
  buy_supplies:
    execute:
      invoke: host.run
      on_success: ` + kw + `
`)
		_, err := LoadBytes(yaml)
		require.NoError(t, err, "keyword %q should be accepted", kw)
	}
}

// TestProposalExecute_OnSuccessResolvesToState asserts a plain named-state
// on_success: target passes load-time validation just like on_error:.
func TestProposalExecute_OnSuccessResolvesToState(t *testing.T) {
	yaml := []byte(`app:
  id: proposal-on-success-ok
  version: 0.1.0
hosts: [host.run]
root: start
states:
  start:
    terminal: true
  done:
    terminal: true
proposals:
  buy_supplies:
    execute:
      invoke: host.run
      on_success: done
`)
	_, err := LoadBytes(yaml)
	require.NoError(t, err)
}

// TestUnhandledInvokes_DedupeCollapsesSharedFragmentAcrossImportingRoots
// asserts the de-duplication contract at the heart of the unhandled-invoke
// lint fix: one fragment (a child story with a single unhandled invoke:)
// imported by two DIFFERENT parent roots is discovered once per root by
// UnhandledInvokes (call it once per root, like `kitsoki validate` does per
// story), but DedupeUnhandledInvokes collapses the combined findings down
// to the one real underlying gap — not two.
func TestUnhandledInvokes_DedupeCollapsesSharedFragmentAcrossImportingRoots(t *testing.T) {
	root := t.TempDir()
	childDir := mkdirT(t, root, "child")
	mustWrite(t, childDir, "app.yaml", `app: { id: child, version: 0.1.0 }
hosts: [host.run]
root: idle
states:
  idle:
    on_enter:
      - invoke: host.run
`)

	parentADir := mkdirT(t, root, "parent-a")
	mustWrite(t, parentADir, "app.yaml", `app: { id: parent-a, version: 0.1.0 }
imports:
  bf:
    source: ../child
    entry: idle
root: bf
`)

	parentBDir := mkdirT(t, root, "parent-b")
	mustWrite(t, parentBDir, "app.yaml", `app: { id: parent-b, version: 0.1.0 }
imports:
  fragment:
    source: ../child
    entry: idle
root: fragment
`)

	defA, err := Load(filepath.Join(parentADir, "app.yaml"))
	require.NoError(t, err)
	defB, err := Load(filepath.Join(parentBDir, "app.yaml"))
	require.NoError(t, err)

	findingsA := UnhandledInvokes(defA, filepath.Join(parentADir, "app.yaml"))
	findingsB := UnhandledInvokes(defB, filepath.Join(parentBDir, "app.yaml"))
	require.Len(t, findingsA, 1, "parent-a should see the child's one unhandled invoke, aliased under bf")
	require.Len(t, findingsB, 1, "parent-b should see the SAME underlying gap, aliased under a different alias (fragment)")

	// Different importers alias the shared child differently, so the raw
	// per-root display StatePath legitimately differs...
	require.Equal(t, "bf.idle", findingsA[0].StatePath)
	require.Equal(t, "fragment.idle", findingsB[0].StatePath)

	// ...but both trace back to the SAME child app.yaml on disk, so their
	// SourceKey (SourceFile + origin-relative state path + location) is
	// identical.
	require.Equal(t, findingsA[0].SourceFile, findingsB[0].SourceFile)
	require.Equal(t, "idle", findingsA[0].SourceStatePath)
	require.Equal(t, findingsA[0].SourceStatePath, findingsB[0].SourceStatePath)
	require.Equal(t, findingsA[0].SourceKey(), findingsB[0].SourceKey())

	combined := append(append([]UnhandledInvoke{}, findingsA...), findingsB...)
	require.Len(t, combined, 2, "raw combined count still double-counts the shared fragment, matching today's per-root behavior")

	deduped := DedupeUnhandledInvokes(combined)
	require.Len(t, deduped, 1, "de-duplication must collapse the one real gap discovered via two importing roots into a single finding")
}
