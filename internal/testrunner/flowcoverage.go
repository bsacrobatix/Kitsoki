package testrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"kitsoki/internal/app"
)

// FlowCoverageOptions configures static flow fixture coverage analysis.
type FlowCoverageOptions struct {
	// FlowsGlob is the glob for flow fixture files. Required.
	FlowsGlob string
	// JSONOut is the optional path to write a machine-readable report.
	JSONOut string
	// MinBranchCoverage fails the gate when branch coverage is below this
	// percentage. Zero disables the threshold.
	MinBranchCoverage float64
	// RequireAllBranches fails the gate when any authored transition branch is
	// uncovered.
	RequireAllBranches bool
	// MaxCombinations caps enum slot product expansion per state+intent. Larger
	// products are reported as skipped so authors can opt into a narrower check.
	MaxCombinations int
	// ImportResolver is the injected app import resolver used by kitsoki test.
	ImportResolver app.ImportResolver
}

// FlowCoverageReport is the static coverage ledger for one app's flow fixtures.
type FlowCoverageReport struct {
	App             string                  `json:"app"`
	AppPath         string                  `json:"app_path"`
	FlowsGlob       string                  `json:"flows_glob"`
	BranchCoverage  CoverageSummary         `json:"branch_coverage"`
	Branches        []FlowBranchCoverage    `json:"branches"`
	ParameterChecks []FlowParameterCoverage `json:"parameter_checks,omitempty"`
	FlowFiles       []string                `json:"flow_files"`
	Problems        []string                `json:"problems,omitempty"`
	Passed          bool                    `json:"passed"`
}

// CoverageSummary is a compact pass/fail metric for one coverage dimension.
type CoverageSummary struct {
	Covered int     `json:"covered"`
	Total   int     `json:"total"`
	Percent float64 `json:"percent"`
}

// FlowBranchCoverage describes one authored transition branch.
type FlowBranchCoverage struct {
	ID        string   `json:"id"`
	State     string   `json:"state"`
	Intent    string   `json:"intent"`
	Index     int      `json:"index"`
	Target    string   `json:"target"`
	When      string   `json:"when,omitempty"`
	Default   bool     `json:"default,omitempty"`
	Covered   bool     `json:"covered"`
	FlowFiles []string `json:"flow_files,omitempty"`
}

// FlowParameterCoverage describes bounded enum-slot coverage for one
// state+intent pair.
type FlowParameterCoverage struct {
	State                string              `json:"state"`
	Intent               string              `json:"intent"`
	EnumSlots            map[string][]string `json:"enum_slots,omitempty"`
	ObservedValues       map[string][]string `json:"observed_values,omitempty"`
	MissingValues        map[string][]string `json:"missing_values,omitempty"`
	RequiredCombinations int                 `json:"required_combinations,omitempty"`
	ObservedCombinations int                 `json:"observed_combinations,omitempty"`
	MissingCombinations  []map[string]string `json:"missing_combinations,omitempty"`
	SkippedReason        string              `json:"skipped_reason,omitempty"`
	Passed               bool                `json:"passed"`
}

// RunFlowCoverage statically compares authored story transitions with the
// flow fixtures that claim to drive them. It does not execute flows; pair this
// with RunFlows for behavioral correctness.
func RunFlowCoverage(ctx context.Context, appPath string, opts FlowCoverageOptions) (*FlowCoverageReport, error) {
	_ = ctx
	if opts.FlowsGlob == "" {
		return nil, fmt.Errorf("flow coverage: FlowsGlob is required")
	}
	if opts.MaxCombinations <= 0 {
		opts.MaxCombinations = 64
	}

	publishAppDirForTestrunner(appPath)
	def, err := app.LoadWithResolver(appPath, nil, opts.ImportResolver)
	if err != nil {
		return nil, fmt.Errorf("load app %q: %w", appPath, err)
	}
	files, err := filepath.Glob(opts.FlowsGlob)
	if err != nil {
		return nil, fmt.Errorf("glob %q: %w", opts.FlowsGlob, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no flow fixtures matched %q", opts.FlowsGlob)
	}
	sort.Strings(files)

	branches := collectFlowBranches(def)
	byKey := make(map[string]*FlowBranchCoverage, len(branches))
	for i := range branches {
		byKey[branches[i].ID] = &branches[i]
	}

	param := newParameterLedger(def, opts.MaxCombinations)
	var problems []string
	for _, file := range files {
		fixtures, ferr := loadFlowCoverageFixtures(file)
		if ferr != nil {
			return nil, ferr
		}
		for _, fixture := range fixtures {
			state := fixture.InitialState
			if state == "" {
				problems = append(problems, fmt.Sprintf("%s: flow fixture missing initial_state", file))
				continue
			}
			for i, turn := range fixture.Turns {
				if turn.Intent == nil {
					if turn.ExpectState != "" {
						state = turn.ExpectState
					}
					continue
				}
				intentName := turn.Intent.Name
				param.observe(state, intentName, turn.Intent.Slots)

				candidates := branchesForStateIntent(branches, state, intentName)
				if len(candidates) == 0 {
					problems = append(problems, fmt.Sprintf("%s turn %d: no authored branch for %s + %s", file, i+1, state, intentName))
					if turn.ExpectState != "" {
						state = turn.ExpectState
					}
					continue
				}
				covered := inferCoveredBranches(candidates, state, turn)
				for _, branch := range covered {
					if got := byKey[branch.ID]; got != nil {
						got.Covered = true
						got.FlowFiles = appendUnique(got.FlowFiles, file)
					}
				}

				nextState := inferNextState(candidates, state, turn)
				if nextState != "" {
					state = nextState
				}
			}
		}
	}

	sort.Slice(branches, func(i, j int) bool { return branches[i].ID < branches[j].ID })
	covered := 0
	for i := range branches {
		sort.Strings(branches[i].FlowFiles)
		if branches[i].Covered {
			covered++
		}
	}
	summary := CoverageSummary{Covered: covered, Total: len(branches)}
	if summary.Total > 0 {
		summary.Percent = float64(summary.Covered) * 100 / float64(summary.Total)
	}

	paramChecks := param.results()
	passed := true
	if opts.RequireAllBranches && summary.Covered != summary.Total {
		passed = false
	}
	if opts.MinBranchCoverage > 0 && summary.Percent+1e-9 < opts.MinBranchCoverage {
		passed = false
	}

	report := &FlowCoverageReport{
		App:             def.App.ID,
		AppPath:         appPath,
		FlowsGlob:       opts.FlowsGlob,
		BranchCoverage:  summary,
		Branches:        branches,
		ParameterChecks: paramChecks,
		FlowFiles:       files,
		Problems:        problems,
		Passed:          passed,
	}
	if len(problems) > 0 && opts.RequireAllBranches {
		report.Passed = false
	}
	if opts.JSONOut != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		if dir := filepath.Dir(opts.JSONOut); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create JSON report dir: %w", err)
			}
		}
		if err := os.WriteFile(opts.JSONOut, append(b, '\n'), 0o644); err != nil {
			return nil, fmt.Errorf("write JSON report: %w", err)
		}
	}
	return report, nil
}

func loadFlowCoverageFixtures(file string) ([]FlowFixture, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", file, err)
	}
	var fixtures []FlowFixture
	for _, doc := range splitYAMLDocs(data) {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}
		var fixture FlowFixture
		if err := yaml.Unmarshal([]byte(doc), &fixture); err != nil {
			return nil, fmt.Errorf("parse fixture in %q: %w", file, err)
		}
		if fixture.TestKind == "flow" {
			fixtures = append(fixtures, fixture)
		}
	}
	return fixtures, nil
}

func collectFlowBranches(def *app.AppDef) []FlowBranchCoverage {
	var out []FlowBranchCoverage
	walkStates("", def.States, func(path string, st *app.State) {
		for intentName, transitions := range st.On {
			for idx, tr := range transitions {
				target := normalizeTarget(path, tr.Target)
				id := branchID(path, intentName, idx)
				out = append(out, FlowBranchCoverage{
					ID:      id,
					State:   path,
					Intent:  intentName,
					Index:   idx,
					Target:  target,
					When:    tr.When,
					Default: tr.Default,
				})
			}
		}
	})
	return out
}

func walkStates(prefix string, states map[string]*app.State, visit func(string, *app.State)) {
	keys := make([]string, 0, len(states))
	for k := range states {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, name := range keys {
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		st := states[name]
		visit(path, st)
		if len(st.States) > 0 {
			walkStates(path, st.States, visit)
		}
	}
}

func branchID(state, intent string, idx int) string {
	return fmt.Sprintf("%s#%s[%d]", state, intent, idx)
}

func branchesForStateIntent(branches []FlowBranchCoverage, state, intent string) []FlowBranchCoverage {
	var out []FlowBranchCoverage
	for _, branch := range branches {
		if branch.State == state && branch.Intent == intent {
			out = append(out, branch)
		}
	}
	return out
}

func inferCoveredBranches(candidates []FlowBranchCoverage, state string, turn FlowTurn) []FlowBranchCoverage {
	if turn.ExpectError != nil {
		return nil
	}
	if turn.ExpectState != "" {
		var out []FlowBranchCoverage
		for _, branch := range candidates {
			if branch.Target == turn.ExpectState {
				out = append(out, branch)
			}
		}
		return out
	}
	if len(turn.ExpectStateIn) > 0 {
		allowed := make(map[string]bool, len(turn.ExpectStateIn))
		for _, s := range turn.ExpectStateIn {
			allowed[s] = true
		}
		var out []FlowBranchCoverage
		for _, branch := range candidates {
			if allowed[branch.Target] {
				out = append(out, branch)
			}
		}
		return out
	}
	if len(candidates) == 1 {
		return candidates
	}
	if len(candidates) > 0 && candidates[0].Target == state && allSameTarget(candidates) {
		return candidates
	}
	return nil
}

func inferNextState(candidates []FlowBranchCoverage, state string, turn FlowTurn) string {
	if turn.ExpectState != "" {
		return turn.ExpectState
	}
	covered := inferCoveredBranches(candidates, state, turn)
	if len(covered) == 1 {
		return covered[0].Target
	}
	if len(covered) > 1 && allSameTarget(covered) {
		return covered[0].Target
	}
	return ""
}

func allSameTarget(branches []FlowBranchCoverage) bool {
	if len(branches) == 0 {
		return false
	}
	target := branches[0].Target
	for _, branch := range branches[1:] {
		if branch.Target != target {
			return false
		}
	}
	return true
}

func normalizeTarget(from, target string) string {
	if target == "" || target == "." {
		return from
	}
	if strings.HasPrefix(target, "@") || strings.Contains(target, ".") {
		return target
	}
	if i := strings.LastIndex(from, "."); i >= 0 {
		return from[:i+1] + target
	}
	return target
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

type parameterLedger struct {
	def             *app.AppDef
	maxCombinations int
	observed        map[string][]map[string]any
}

func newParameterLedger(def *app.AppDef, maxCombinations int) *parameterLedger {
	return &parameterLedger{
		def:             def,
		maxCombinations: maxCombinations,
		observed:        map[string][]map[string]any{},
	}
}

func (l *parameterLedger) observe(state, intent string, slots map[string]any) {
	key := state + "#" + intent
	copied := make(map[string]any, len(slots))
	for k, v := range slots {
		copied[k] = v
	}
	l.observed[key] = append(l.observed[key], copied)
}

func (l *parameterLedger) results() []FlowParameterCoverage {
	var out []FlowParameterCoverage
	walkStates("", l.def.States, func(path string, st *app.State) {
		for intentName := range st.On {
			intentDef, ok := l.intentForState(st, intentName)
			if !ok {
				continue
			}
			enumSlots := enumSlotValues(intentDef)
			if len(enumSlots) == 0 {
				continue
			}
			key := path + "#" + intentName
			check := FlowParameterCoverage{
				State:          path,
				Intent:         intentName,
				EnumSlots:      enumSlots,
				ObservedValues: map[string][]string{},
				MissingValues:  map[string][]string{},
				Passed:         true,
			}
			observedCombos := map[string]bool{}
			for _, slots := range l.observed[key] {
				combo := map[string]string{}
				for slot := range enumSlots {
					if raw, ok := slots[slot]; ok {
						value := fmt.Sprint(raw)
						check.ObservedValues[slot] = appendUnique(check.ObservedValues[slot], value)
						combo[slot] = value
					}
				}
				if len(combo) == len(enumSlots) {
					observedCombos[comboKey(combo)] = true
				}
			}
			for slot, values := range enumSlots {
				sort.Strings(check.ObservedValues[slot])
				seen := make(map[string]bool, len(check.ObservedValues[slot]))
				for _, v := range check.ObservedValues[slot] {
					seen[v] = true
				}
				for _, want := range values {
					if !seen[want] {
						check.MissingValues[slot] = append(check.MissingValues[slot], want)
						check.Passed = false
					}
				}
			}
			combos, skipped := expandEnumCombinations(enumSlots, l.maxCombinations)
			check.RequiredCombinations = len(combos)
			check.ObservedCombinations = len(observedCombos)
			if skipped != "" {
				check.SkippedReason = skipped
			} else {
				for _, combo := range combos {
					if !observedCombos[comboKey(combo)] {
						check.MissingCombinations = append(check.MissingCombinations, combo)
						check.Passed = false
					}
				}
			}
			out = append(out, check)
		}
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].State == out[j].State {
			return out[i].Intent < out[j].Intent
		}
		return out[i].State < out[j].State
	})
	return out
}

func (l *parameterLedger) intentForState(st *app.State, intentName string) (app.Intent, bool) {
	if st.Intents != nil {
		if intentDef, ok := st.Intents[intentName]; ok {
			return intentDef, true
		}
	}
	if l.def.Intents != nil {
		if intentDef, ok := l.def.Intents[intentName]; ok {
			return intentDef, true
		}
	}
	return app.Intent{}, false
}

func enumSlotValues(intentDef app.Intent) map[string][]string {
	out := map[string][]string{}
	for name, slot := range intentDef.Slots {
		if len(slot.Values) == 0 {
			continue
		}
		values := append([]string(nil), slot.Values...)
		sort.Strings(values)
		out[name] = values
	}
	return out
}

func expandEnumCombinations(slots map[string][]string, max int) ([]map[string]string, string) {
	keys := make([]string, 0, len(slots))
	total := 1
	for k, values := range slots {
		keys = append(keys, k)
		total *= len(values)
		if total > max {
			return nil, fmt.Sprintf("combination product %d exceeds max %d", total, max)
		}
	}
	sort.Strings(keys)
	var out []map[string]string
	var rec func(int, map[string]string)
	rec = func(idx int, cur map[string]string) {
		if idx == len(keys) {
			combo := make(map[string]string, len(cur))
			for k, v := range cur {
				combo[k] = v
			}
			out = append(out, combo)
			return
		}
		key := keys[idx]
		for _, value := range slots[key] {
			cur[key] = value
			rec(idx+1, cur)
		}
		delete(cur, key)
	}
	rec(0, map[string]string{})
	return out, ""
}

func comboKey(combo map[string]string) string {
	keys := make([]string, 0, len(combo))
	for k := range combo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+combo[k])
	}
	return strings.Join(parts, "\x00")
}
