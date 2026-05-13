// picker.go — plain-text numbered session picker for `kitsoki run --continue`.
//
// The proposal §5.4 calls for "a small Bubble Tea overlay", but phase A ships
// a simpler plain numbered list to stderr + readline from stdin. A full Bubble
// Tea overlay (built from the same data as `kitsoki session list`) is a
// follow-up task.
package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/store"
)

// pickSession presents a numbered list of sessions to stderr and reads the
// user's choice from in. Returns the chosen SessionID or an error.
// Returns errPickerAborted if the user quits.
func pickSession(sessions []store.SessionSummary, keys [][]store.ExternalKey, out io.Writer, in io.Reader) (app.SessionID, error) {
	if len(sessions) == 0 {
		return "", fmt.Errorf("no sessions found for this app")
	}

	fmt.Fprintln(out, "Choose a session to resume:")
	fmt.Fprintln(out)

	for i, s := range sessions {
		age := time.Since(s.StartedAt).Truncate(time.Second)
		keyStr := ""
		if i < len(keys) && len(keys[i]) > 0 {
			parts := make([]string, 0, len(keys[i]))
			for _, k := range keys[i] {
				parts = append(parts, k.Transport+":"+k.Thread)
			}
			keyStr = "  keys: " + strings.Join(parts, ", ")
		}
		fmt.Fprintf(out, "  [%d] %s  turn %d  status %-8s  started %s ago%s\n",
			i+1,
			s.ID,
			s.LastTurn,
			s.Status,
			humanizeAge(age),
			keyStr,
		)
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Enter number (1-%d), or q to quit: ", len(sessions))

	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return "", errPickerAborted
	}
	line := strings.TrimSpace(scanner.Text())

	if line == "q" || line == "Q" {
		return "", errPickerAborted
	}

	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(sessions) {
		return "", fmt.Errorf("invalid choice %q: enter a number between 1 and %d", line, len(sessions))
	}

	return sessions[n-1].ID, nil
}

// humanizeAge formats a duration into a human-readable short string.
func humanizeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// errPickerAborted is returned when the user types q/Q or EOF in the picker.
var errPickerAborted = fmt.Errorf("session picker aborted")
