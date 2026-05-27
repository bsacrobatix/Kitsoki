package testrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	goyaml "github.com/goccy/go-yaml"

	"kitsoki/internal/app"
	"kitsoki/internal/clock"
	"kitsoki/internal/host"
)

// Cassette is one host-cassette file (kind: host_cassette).
type Cassette struct {
	Kind        string            `yaml:"kind"`
	AppID       string            `yaml:"app_id"`
	AppVersion  string            `yaml:"app_version,omitempty"`
	SourceRun   string            `yaml:"source_run,omitempty"`
	GeneratedAt string            `yaml:"generated_at,omitempty"`
	MatchOn     []string          `yaml:"match_on,omitempty"`
	RecordMode  string            `yaml:"record_mode,omitempty"`
	PhaseFrom   string            `yaml:"phase_from,omitempty"`
	Episodes    []CassetteEpisode `yaml:"episodes"`

	path       string
	phaseRegex *regexp.Regexp
	mu         sync.Mutex
}

// CassetteEpisode is one episode entry in a cassette.
type CassetteEpisode struct {
	ID       string            `yaml:"id"`
	Match    map[string]any    `yaml:"match"`
	Response CassetteResponse  `yaml:"response,omitempty"`
	Delay    string            `yaml:"delay,omitempty"`
	Replay   string            `yaml:"replay,omitempty"`

	played bool
}

// CassetteResponse is the canned response for an episode.
type CassetteResponse struct {
	Data       map[string]any `yaml:"data,omitempty"`
	Error      string         `yaml:"error,omitempty"`
	InfraError string         `yaml:"infra_error,omitempty"`
}

// UnmatchedEpisodes returns the IDs of every episode that was never played
// at least once. Episodes with replay: any that were matched at least once
// are NOT considered unmatched — only episodes with a zero play count are
// returned. The slice is ordered by episode position in the cassette.
//
// Callers use this to detect phantom / orphan episodes after a complete flow
// run: any unmatched episode indicates either a cassette mismatch or a flow
// fixture that did not exercise all expected paths.
func (c *Cassette) UnmatchedEpisodes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for _, ep := range c.Episodes {
		if !ep.played {
			ids = append(ids, ep.ID)
		}
	}
	return ids
}

// ErrCassetteMiss is returned when no episode matches a handler call.
type ErrCassetteMiss struct {
	Handler           string
	Args              map[string]any
	AvailableEpisodes []string
}

func (e *ErrCassetteMiss) Error() string {
	return fmt.Sprintf("cassette miss: no episode matched handler=%q args=%v; available episodes: %v",
		e.Handler, e.Args, e.AvailableEpisodes)
}

// LoadCassette reads and parses the YAML cassette at path. It resolves
// !include directives (paths relative to the cassette file) before
// unmarshaling. Returns an error if kind != "host_cassette".
func LoadCassette(path string) (*Cassette, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("cassette: abs path %q: %w", path, err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("cassette: read %q: %w", abs, err)
	}

	baseDir := filepath.Dir(abs)
	resolved, err := resolveIncludes(data, baseDir)
	if err != nil {
		return nil, fmt.Errorf("cassette: resolve !include in %q: %w", abs, err)
	}

	var cas Cassette
	if err := goyaml.Unmarshal(resolved, &cas); err != nil {
		return nil, fmt.Errorf("cassette: parse %q: %w", abs, err)
	}

	if cas.Kind != "host_cassette" {
		return nil, fmt.Errorf("cassette: %q: kind must be \"host_cassette\", got %q", abs, cas.Kind)
	}

	switch cas.RecordMode {
	case "", "none", "new_episodes":
		// ok
	default:
		return nil, fmt.Errorf("cassette: %q: record_mode %q is not supported; valid values are \"none\" or \"new_episodes\"", abs, cas.RecordMode)
	}

	if cas.PhaseFrom != "" {
		re, reErr := regexp.Compile(cas.PhaseFrom)
		if reErr != nil {
			return nil, fmt.Errorf("cassette: %q: phase_from regex: %w", abs, reErr)
		}
		cas.phaseRegex = re
	}

	cas.path = abs
	return &cas, nil
}

// resolveIncludes scans the YAML bytes for !include <path> tags and replaces
// them inline with the JSON content of the referenced file (resolved relative
// to baseDir). Uses a simple line-by-line pre-pass because goccy/go-yaml does
// not natively expand custom YAML tags before unmarshaling into typed structs.
//
// Recognised form (anywhere a YAML value appears):
//
//	someKey: !include relative/path.json
//
// The JSON file is parsed and re-serialised as YAML-safe JSON-literal inline.
// Only the value side of a mapping entry is replaced; block-scalars and
// anchors using !include are not supported.
func resolveIncludes(data []byte, baseDir string) ([]byte, error) {
	// Match: optional whitespace + !include + whitespace + path (rest of line).
	tagRe := regexp.MustCompile(`^(\s*)([^:]+:\s*)!include\s+(.+?)\s*$`)
	lines := strings.Split(string(data), "\n")
	var out []string
	for _, line := range lines {
		m := tagRe.FindStringSubmatch(line)
		if m == nil {
			out = append(out, line)
			continue
		}
		prefix := m[1] + m[2]
		incPath := filepath.Join(baseDir, strings.TrimSpace(m[3]))
		raw, readErr := os.ReadFile(incPath)
		if readErr != nil {
			return nil, fmt.Errorf("!include %q: %w", incPath, readErr)
		}
		// Compact the JSON so it fits on a single line and is valid YAML.
		var v any
		if jsonErr := json.Unmarshal(raw, &v); jsonErr != nil {
			return nil, fmt.Errorf("!include %q: parse JSON: %w", incPath, jsonErr)
		}
		compact, marshalErr := json.Marshal(v)
		if marshalErr != nil {
			return nil, fmt.Errorf("!include %q: re-marshal JSON: %w", incPath, marshalErr)
		}
		out = append(out, prefix+string(compact))
	}
	return []byte(strings.Join(out, "\n")), nil
}

// phaseFromStatePath extracts the "phase" synthetic field from the orchestrator
// state path. Uses the cassette's PhaseFrom regex (first capture group) when
// set; otherwise uses the first dot-separated segment of statePath.
func (c *Cassette) phaseFromStatePath(statePath string) string {
	if c.phaseRegex != nil {
		sub := c.phaseRegex.FindStringSubmatch(statePath)
		if len(sub) >= 2 {
			return sub[1]
		}
		return ""
	}
	if idx := strings.IndexByte(statePath, '.'); idx >= 0 {
		return statePath[:idx]
	}
	return statePath
}

// episodeIDs returns the IDs of the provided episode slice for error messages.
func episodeIDs(eps []*CassetteEpisode) []string {
	ids := make([]string, len(eps))
	for i, e := range eps {
		ids[i] = e.ID
	}
	return ids
}

// MatchEpisode finds the first unplayed episode that matches (handler, args,
// statePath). Returns ErrCassetteMiss when no episode matches.
func MatchEpisode(handler string, args map[string]any, statePath string, cas *Cassette) (*CassetteEpisode, error) {
	phase := cas.phaseFromStatePath(statePath)

	// Compute schema_name synthetic field.
	schemaName := ""
	if s, ok := args["schema"].(string); ok {
		schemaName = filepath.Base(s)
	}

	ptrs := make([]*CassetteEpisode, len(cas.Episodes))
	for i := range cas.Episodes {
		ptrs[i] = &cas.Episodes[i]
	}

	for _, ep := range ptrs {
		if ep.played && ep.Replay != "any" {
			continue
		}
		if !episodeMatches(ep, handler, args, phase, schemaName) {
			continue
		}
		return ep, nil
	}

	return nil, &ErrCassetteMiss{
		Handler:           handler,
		Args:              args,
		AvailableEpisodes: episodeIDs(ptrs),
	}
}

// episodeMatches checks whether a single episode's match map matches the call.
func episodeMatches(ep *CassetteEpisode, handler string, args map[string]any, phase, schemaName string) bool {
	for k, want := range ep.Match {
		var got any
		switch k {
		case "handler":
			got = handler
		case "phase":
			got = phase
		case "schema_name":
			got = schemaName
		default:
			got = args[k]
		}
		if !matchValue(got, want) {
			return false
		}
	}
	return true
}

// matchValue compares a field value against the expected match pattern.
// Uses deep equality after JSON-normalising both sides so that int/float
// comparisons from YAML work against string-typed args and vice-versa.
func matchValue(got, want any) bool {
	if got == want {
		return true
	}
	// JSON-normalise both sides so numeric types compare correctly.
	gj, err1 := json.Marshal(got)
	wj, err2 := json.Marshal(want)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(gj) == string(wj)
}

// BuildCassetteDispatcher returns a host.Handler closure that the testrunner
// installs under every handler name referenced by the cassette's episodes.
// stateOf is called per-invocation to read the orchestrator's current StatePath.
// fallback is dispatched on miss when non-nil; nil fallback on miss returns
// ErrCassetteMiss. recordSink is called with synthesised episodes when
// KITSOKI_CASSETTE_RECORD is active.
func BuildCassetteDispatcher(
	cas *Cassette,
	handlerName string,
	stateOf func() string,
	fallback host.Handler,
	recordSink func(ep *CassetteEpisode),
	clk clock.Clock,
) host.Handler {
	return func(ctx context.Context, args map[string]any) (host.Result, error) {
		statePath := stateOf()

		cas.mu.Lock()
		ep, err := MatchEpisode(handlerName, args, statePath, cas)
		if err == nil {
			// Mark played (always — even replay: any episodes so that
			// UnmatchedEpisodes can distinguish "never matched" from
			// "matched at least once". MatchEpisode's skip condition is
			// ep.played && ep.Replay != "any", so a replay: any episode
			// remains available for re-matching even after played=true).
			ep.played = true
			// Capture values before releasing lock.
			resp := ep.Response
			delay := ep.Delay
			cas.mu.Unlock()

			// Honor delay via the injected clock.
			if delay != "" {
				d, parseErr := app.ParseDuration(delay)
				if parseErr == nil && d > 0 {
					clk.Sleep(d)
				}
			}

			if resp.InfraError != "" {
				return host.Result{}, errors.New(resp.InfraError)
			}
			return host.Result{Data: resp.Data, Error: resp.Error}, nil
		}
		cas.mu.Unlock()

		// Miss path.
		var miss *ErrCassetteMiss
		if !errors.As(err, &miss) {
			return host.Result{}, err
		}

		mode := CassetteRecordMode(cas)

		if mode == "none" || mode == "" {
			if fallback != nil {
				return fallback(ctx, args)
			}
			return host.Result{}, miss
		}

		// Recording mode: delegate to fallback (must exist), capture, append.
		if fallback == nil {
			return host.Result{}, fmt.Errorf("cassette: record mode %q but no fallback handler for %q", mode, handlerName)
		}
		liveResult, liveErr := fallback(ctx, args)
		if liveErr != nil {
			return liveResult, liveErr
		}

		if recordSink != nil {
			synth := synthesiseEpisode(handlerName, args, statePath, cas, liveResult)
			recordSink(synth)
		}
		return liveResult, nil
	}
}

// synthesiseEpisode builds a new CassetteEpisode from a live handler result.
func synthesiseEpisode(handlerName string, args map[string]any, statePath string, cas *Cassette, result host.Result) *CassetteEpisode {
	matchMap := map[string]any{"handler": handlerName}
	if statePath != "" {
		matchMap["phase"] = cas.phaseFromStatePath(statePath)
	}
	for k, v := range args {
		if k != "handler" && k != "phase" && k != "schema_name" {
			matchMap[k] = v
		}
	}
	return &CassetteEpisode{
		ID:    fmt.Sprintf("recorded_%s_%s", handlerName, statePath),
		Match: matchMap,
		Response: CassetteResponse{
			Data:  result.Data,
			Error: result.Error,
		},
	}
}

// CassetteRecordMode returns the effective record mode. The environment variable
// KITSOKI_CASSETTE_RECORD wins over the file-level field. Returns "none" or
// "new_episodes". Any other value passed via the env var (e.g. "all") is
// returned as-is so the caller can surface a clear error — LoadCassette
// already rejects unsupported file-level values at parse time.
func CassetteRecordMode(cas *Cassette) string {
	if env := os.Getenv("KITSOKI_CASSETTE_RECORD"); env != "" {
		return env
	}
	if cas != nil && cas.RecordMode != "" {
		return cas.RecordMode
	}
	return "none"
}

// ValidateRecordMode reports whether mode is a supported effective record
// mode. Used by the testrunner to surface a clear error when an env-var
// override contains an unsupported value.
func ValidateRecordMode(mode string) error {
	switch mode {
	case "", "none", "new_episodes":
		return nil
	default:
		return fmt.Errorf("record_mode %q is not supported; valid values are \"none\" or \"new_episodes\"", mode)
	}
}

// CassetteStrictRecording returns true when KITSOKI_CASSETTE_STRICT=1 is set.
func CassetteStrictRecording() bool {
	v := os.Getenv("KITSOKI_CASSETTE_STRICT")
	return v == "1" || v == "true"
}

// AppendEpisodeToFile appends ep to the cassette file at cas.path using an
// atomic temp-file rename. The cassette is re-read, the new episode appended,
// and the full YAML written back. A trailing comment marks appended episodes.
func AppendEpisodeToFile(cas *Cassette, ep *CassetteEpisode) error {
	if cas.path == "" {
		return fmt.Errorf("cassette: AppendEpisodeToFile: cassette has no path")
	}

	cas.mu.Lock()
	defer cas.mu.Unlock()

	// Re-read the existing file so we preserve any edits made since load.
	existing, err := os.ReadFile(cas.path)
	if err != nil {
		return fmt.Errorf("cassette: AppendEpisodeToFile: read %q: %w", cas.path, err)
	}

	// Unmarshal the existing cassette without include resolution (the file was
	// already loaded; we just want to add an episode and re-marshal).
	var onDisk Cassette
	if parseErr := goyaml.Unmarshal(existing, &onDisk); parseErr != nil {
		return fmt.Errorf("cassette: AppendEpisodeToFile: parse existing: %w", parseErr)
	}

	onDisk.Episodes = append(onDisk.Episodes, *ep)

	out, marshalErr := goyaml.Marshal(&onDisk)
	if marshalErr != nil {
		return fmt.Errorf("cassette: AppendEpisodeToFile: marshal: %w", marshalErr)
	}

	// Append a comment so the origin of the episode is traceable.
	out = append(out, []byte("\n# appended by KITSOKI_CASSETTE_RECORD\n")...)

	// Atomic write via temp file + rename.
	dir := filepath.Dir(cas.path)
	tmp, tmpErr := os.CreateTemp(dir, ".cassette-append-*.yaml")
	if tmpErr != nil {
		return fmt.Errorf("cassette: AppendEpisodeToFile: create temp: %w", tmpErr)
	}
	tmpName := tmp.Name()
	if _, writeErr := tmp.Write(out); writeErr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("cassette: AppendEpisodeToFile: write temp: %w", writeErr)
	}
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cassette: AppendEpisodeToFile: close temp: %w", closeErr)
	}
	if renameErr := os.Rename(tmpName, cas.path); renameErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cassette: AppendEpisodeToFile: rename: %w", renameErr)
	}

	// Update the in-memory cassette to reflect the appended episode.
	cas.Episodes = append(cas.Episodes, *ep)
	return nil
}
