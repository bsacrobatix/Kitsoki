package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// loadopts.go defines LoadOptions and LoadWithOptions — the options-struct
// form of the file-backed load entrypoints. LoadWithResolver (and therefore
// Load / LoadWithOverrides, which delegate to it) is a thin shim over
// LoadWithOptions so there is exactly one place that parses the root
// manifest and drives runLoadPipeline.
//
// EnvLookup is the reason this file exists: it lets a caller materialising an
// AppDef from an in-memory file set (app.LoadFromFiles, used to reconstruct a
// revision from a trace or a freshly-patched story) point `${KITSOKI_APP_DIR}`
// -style cwd: expansion at its own temp tree WITHOUT mutating the
// process-global environment via os.Setenv — a process-global that would race
// two concurrently-loading revisions against each other. A nil EnvLookup
// (the zero value, what every existing caller gets) is identical to today:
// cwd: expansion reads os.LookupEnv, exactly as if EnvLookup never existed.
type LoadOptions struct {
	// IfaceOverrides is the per-iface binding-override map applied between
	// the import-fold pass and resolveAllInterfaces. See LoadWithOverrides.
	IfaceOverrides map[string]string
	// Resolver is the injected @kitsoki/<name> import resolver. See
	// LoadWithResolver.
	Resolver ImportResolver
	// EnvLookup overrides os.LookupEnv for `cwd:` expansion (agents: and
	// meta_modes:) during this load only. Nil means "use the process
	// environment", identical to pre-LoadOptions behaviour. See
	// AppDef.envLookup.
	EnvLookup func(name string) (string, bool)
}

// LoadWithOptions is LoadWithResolver expressed as an options struct instead
// of positional parameters, so a new knob (like EnvLookup) doesn't require
// touching every existing call site's argument list. See [LoadOptions].
//
// Structurally identical to the body LoadWithResolver used to have: read the
// root manifest, parseAndMerge it (threading opts.EnvLookup through so it's
// visible to that pass's own resolveAgentDecls call — see parseAndMerge), then
// hand off to runLoadPipeline.
func LoadWithOptions(path string, opts LoadOptions) (*AppDef, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}

	baseDir := filepath.Dir(path)
	if abs, absErr := filepath.Abs(baseDir); absErr == nil {
		baseDir = abs
	}
	merged, mergeErrs := parseAndMerge(b, path, baseDir, opts.EnvLookup)
	if len(mergeErrs) > 0 {
		return nil, errors.Join(mergeErrs...)
	}
	return runLoadPipeline(merged, path, baseDir, opts)
}
