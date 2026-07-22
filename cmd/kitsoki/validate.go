// validate.go — `kitsoki validate <app.yaml>`: load a story through the full
// loader pipeline and report load-time validation errors WITHOUT starting a
// session or running a turn. The fast "is this story well-formed?" check.
package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/app"
)

func validateCmd() *cobra.Command {
	var reportUnhandledErrors bool
	cmd := &cobra.Command{
		Use:   "validate <app.yaml>",
		Short: "Statically validate a story without running it",
		Long: `Load a story app.yaml through the full loader pipeline — imports, phase
expansion, interface resolution, and every load-time validator — and report any
errors. No session is started and no turn is run, so it is fast and free.

It surfaces the same errors a run/flow would hit at load, including:
  - unknown intents / transition targets, world-key typos, agent & host refs
  - host.starlark.run wiring: a missing script/sidecar, and an inputs: value
    that is a bare expression ("world.x" instead of "{{ world.x }}") or a
    literal that can never satisfy its declared sidecar type

Exit status is 0 when the story loads cleanly, 1 otherwise.

  kitsoki validate stories/slidey-edit/app.yaml

--report-unhandled-errors additionally lists every invoke: effect (after
on_error_default: resolution — see docs/stories/state-machine.md) that still
has no effective on_error:. This is a REPORT only: it never changes the exit
status and is not part of the pass/fail validation above. A failing host call
with no on_error: silently continues to the next effect at runtime today —
see the troubleshooting design doc — so this flag is how an author finds
those gaps without waiting on a hard load-time gate the corpus doesn't
support yet.

Passing more than one <app.yaml> validates each in turn (per-root output
unchanged) and, when --report-unhandled-errors is also set, additionally
prints a de-duplicated total across every root passed: a fragment imported
by several stories authors ONE unhandled invoke:, not one per importer, and
the per-root counts alone over-count it once per importing root.

  kitsoki validate --report-unhandled-errors stories/*/app.yaml`,
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var anyErr bool
			var allUnhandled []app.UnhandledInvoke
			for _, path := range args {
				def, err := app.LoadWithResolver(path, nil, buildImportResolver())
				if err != nil {
					// errors.Join renders one validation error per line; present
					// them as a bulleted list on stderr so stdout stays clean
					// for piping.
					fmt.Fprintf(cmd.ErrOrStderr(), "✗ %s — invalid:\n", path)
					for _, line := range strings.Split(strings.TrimRight(err.Error(), "\n"), "\n") {
						if strings.TrimSpace(line) == "" {
							continue
						}
						fmt.Fprintf(cmd.ErrOrStderr(), "  • %s\n", line)
					}
					anyErr = true
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "✓ %s — valid\n", path)
				if reportUnhandledErrors {
					unhandled := app.UnhandledInvokes(def, path)
					fmt.Fprintf(cmd.OutOrStdout(), "\n%d invoke: site(s) with no effective on_error:\n", len(unhandled))
					for _, u := range unhandled {
						fmt.Fprintf(cmd.OutOrStdout(), "  • %s\n", u.String())
					}
					allUnhandled = append(allUnhandled, unhandled...)
				}
			}
			if reportUnhandledErrors && len(args) > 1 {
				deduped := app.DedupeUnhandledInvokes(allUnhandled)
				fmt.Fprintf(cmd.OutOrStdout(), "\n%d root(s): %d raw invoke: finding(s), %d de-duplicated by source location\n",
					len(args), len(allUnhandled), len(deduped))
			}
			if anyErr {
				return fmt.Errorf("validation failed")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&reportUnhandledErrors, "report-unhandled-errors", false, "also report invoke: effects with no effective on_error: (see Long help); does not affect exit status")
	return cmd
}
