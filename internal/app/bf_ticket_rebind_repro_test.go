package app_test

// Reproduction for bug 44:
//
//	bf's iface.ticket.comment/transition ignore kitsoki-dev's
//	host.gh.ticket rebind (falls back to host.local_files.ticket,
//	closing GitHub issues silently fails).
//
// The kitsoki-dev INSTANCE app rebinds the `ticket` capability onto the
// GitHub provider:
//
//	imports.core.host_bindings:
//	  ticket: host.gh.ticket
//
// dev-story (imported as `core`) declares its OWN `ticket` iface AND
// transitively imports the bugfix story (alias `bf`), which declares its
// own `ticket` iface too. When bf's iface is lifted through dev-story it
// becomes `bf__ticket`, and through kitsoki-dev `core__bf__ticket`. The
// instance's single `ticket:` binding key only matches dev-story's OWN
// `ticket` iface (→ core__ticket), so `core__bf__ticket` never gets
// rebound and keeps its default host.local_files.ticket.
//
// Concretely: the bugfix `done` room closes the fixed ticket via
//
//	invoke: iface.ticket.comment      (post the fix close-out)
//	invoke: iface.ticket.transition   (move the ticket to "resolved")
//
// On the kitsoki-dev instance these MUST reach the rebound GitHub
// provider so the GitHub issue is actually commented + closed. Today
// they resolve to host.local_files.ticket.{comment,transition}: the
// close-out is written to a local file and the GitHub issue is never
// touched — a silent no-op from the operator's perspective.
//
// This test loads the real kitsoki-dev instance app and asserts the
// OUTCOME the operator depends on: with `ticket` rebound to
// host.gh.ticket, NO ticket operation may still dispatch to
// host.local_files.ticket. It fails RED on the current tree (bf's
// comment/transition/get still route to local files) and passes for ANY
// fix that makes the rebind cascade to the imported child's ticket iface.

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"kitsoki/internal/app"
)

// collectTicketInvokes walks an effect list (including nested inline
// effects and on_complete chains) and records every host invoke target.
func collectResolvedInvokes(effs []app.Effect, out map[string]bool) {
	for i := range effs {
		if effs[i].Invoke != "" {
			out[effs[i].Invoke] = true
		}
		collectResolvedInvokes(effs[i].Effects, out)
		collectResolvedInvokes(effs[i].OnComplete, out)
	}
}

// walkResolvedInvokes recurses through the state tree collecting every
// resolved host invoke from on_enter and transition effects.
func walkResolvedInvokes(states map[string]*app.State, out map[string]bool) {
	for _, s := range states {
		if s == nil {
			continue
		}
		collectResolvedInvokes(s.OnEnter, out)
		for _, list := range s.On {
			for i := range list {
				collectResolvedInvokes(list[i].Effects, out)
			}
		}
		walkResolvedInvokes(s.States, out)
	}
}

func TestRepro_Bug44_BfTicketRebindReachesGitHub(t *testing.T) {
	appYAML := filepath.Clean(filepath.Join("..", "..", ".kitsoki", "stories", "kitsoki-dev", "app.yaml"))

	def, err := app.Load(appYAML)
	if err != nil {
		t.Fatalf("infra: load kitsoki-dev app: %v", err)
	}

	invokes := map[string]bool{}
	walkResolvedInvokes(def.States, invokes)

	// After the instance rebinds `ticket: host.gh.ticket`, every ticket
	// operation the composed app dispatches must reach the GitHub
	// provider. Any surviving host.local_files.ticket.* invoke is a
	// ticket op that ignored the rebind — the bug: it writes to a local
	// file instead of GitHub, so closing the issue silently fails.
	var strays []string
	for inv := range invokes {
		if strings.HasPrefix(inv, "host.local_files.ticket.") {
			strays = append(strays, inv)
		}
	}
	sort.Strings(strays)

	if len(strays) > 0 {
		t.Fatalf("bug 44: %d ticket op(s) ignored the kitsoki-dev `ticket: host.gh.ticket` rebind "+
			"and still dispatch to the local-files provider (GitHub issue close/comment silently no-ops): %v\n"+
			"  expected: every iface.ticket.* call — including bf's done-room comment + transition — "+
			"resolves to host.gh.ticket.*",
			len(strays), strays)
	}
}
