// Package bashprofile defines the BashProfileKind enum shared between
// internal/app (YAML loader) and internal/host (runtime enforcement).
//
// Keeping the definition here prevents the two packages from independently
// declaring identical iota sequences that could silently diverge on reorder.
package bashprofile

// Kind names the three Bash restriction profiles (oracle-split proposal §2.3).
type Kind int

const (
	ReadOnly     Kind = iota // built-in read-only allowlist; no writes
	Commands                 // explicit argv0 allowlist
	SandboxWrite             // writes confined to a per-call scratch dir; network denied
)
