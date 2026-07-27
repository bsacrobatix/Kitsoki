// Package compliance implements the app-scoped host.compliance provider.
package compliance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kitsoki/internal/clock"
	"kitsoki/internal/graph"
	"kitsoki/internal/host"
	starlarkhost "kitsoki/internal/host/starlark"
	"kitsoki/internal/materialize"
)

const (
	maxCatalogPathBytes = 4096
	maxNodeIDBytes      = 512
	maxChecks           = 64
	maxResolvedBytes    = 128 * 1024
	maxEvidenceBytes    = 256 * 1024
	evidenceSchema      = "kitsoki/compliance-evidence/v1"
)

// Scope is the fixed application authority presented to Authorizer.
type Scope struct {
	AppID       string
	Root        string
	CatalogPath string
	NodeID      string
}

// Authorizer can deny a compliance read/evaluation before catalog access.
type Authorizer interface {
	Authorize(context.Context, Scope) error
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(context.Context, Scope) error

func (f AuthorizerFunc) Authorize(ctx context.Context, scope Scope) error {
	return f(ctx, scope)
}

// CatalogResolver resolves only server-owned graph and materialize declarations.
type CatalogResolver interface {
	Resolve(context.Context, string, string, string) (ResolvedNode, error)
}

// ResolvedNode is the bounded materialize-check projection used by the runner.
type ResolvedNode struct {
	CatalogDigest string
	NodeID        string
	TypeID        string
	Checks        []materialize.ResolvedCheck
}

// CheckRunner evaluates one server-resolved check.
type CheckRunner interface {
	Run(context.Context, string, materialize.ResolvedCheck) materialize.CheckResult
}

// EvidenceStore persists immutable evidence by semantic digest.
type EvidenceStore interface {
	Get(context.Context, string) (string, bool, error)
	Put(context.Context, string, []byte) (string, error)
}

// Dependencies are fixed when a session runtime is constructed. None are
// accepted from story input.
type Dependencies struct {
	AppID      string
	Root       string
	Authorizer Authorizer
	Catalogs   CatalogResolver
	Runner     CheckRunner
	Evidence   EvidenceStore
	Clock      clock.Clock
	Limits     Limits
}

// Limits are server-owned ceilings. Zero values retain the legacy platform
// maxima for unconfigured callers.
type Limits struct {
	MaxChecks        int
	MaxResolvedBytes int
	MaxEvidenceBytes int
}

// NewHandler constructs the host.compliance prefix handler.
func NewHandler(deps Dependencies) host.Handler {
	return func(ctx context.Context, args map[string]any) (host.Result, error) {
		if err := validateDependencies(deps); err != nil {
			return host.Result{}, err
		}
		limits, err := resolvedLimits(deps.Limits)
		if err != nil {
			return host.Result{}, err
		}
		catalogPath, nodeID, err := parseArgs(args)
		if err != nil {
			return host.Result{}, err
		}
		scope := Scope{
			AppID: deps.AppID, Root: deps.Root,
			CatalogPath: catalogPath, NodeID: nodeID,
		}
		if err := deps.Authorizer.Authorize(ctx, scope); err != nil {
			return host.Result{}, fmt.Errorf("host.compliance.run: unauthorized: %w", err)
		}

		resolved, err := deps.Catalogs.Resolve(ctx, deps.Root, catalogPath, nodeID)
		if err != nil {
			return host.Result{}, fmt.Errorf("host.compliance.run: resolve checks: %w", err)
		}
		if resolved.NodeID != nodeID {
			return host.Result{}, fmt.Errorf("host.compliance.run: resolver returned node %q for %q", resolved.NodeID, nodeID)
		}
		if err := validateResolved(resolved, limits); err != nil {
			return host.Result{}, err
		}

		results := make([]materialize.CheckResult, 0, len(resolved.Checks))
		passed := true
		for _, check := range resolved.Checks {
			result := deps.Runner.Run(ctx, deps.Root, check)
			result.ID = check.ID
			result.Script = ""
			result.Reproduce = ""
			results = append(results, result)
			passed = passed && result.OK
		}
		summary := complianceSummary(results)
		semantic := evidenceSemantic{
			Schema: evidenceSchema, AppID: deps.AppID,
			CatalogDigest: resolved.CatalogDigest, NodeID: nodeID,
			TypeID: resolved.TypeID, Passed: passed, Summary: summary,
			Checks: results,
		}
		semanticBytes, err := json.Marshal(semantic)
		if err != nil {
			return host.Result{}, fmt.Errorf("host.compliance.run: encode evidence: %w", err)
		}
		if len(semanticBytes) > limits.MaxEvidenceBytes {
			return host.Result{}, fmt.Errorf(
				"host.compliance.run: encoded evidence is %d bytes, exceeds %d; refusing to truncate",
				len(semanticBytes), limits.MaxEvidenceBytes,
			)
		}
		sum := sha256.Sum256(semanticBytes)
		digest := hex.EncodeToString(sum[:])
		if ref, ok, err := deps.Evidence.Get(ctx, digest); err != nil {
			return host.Result{}, fmt.Errorf("host.compliance.run: read evidence: %w", err)
		} else if ok {
			return complianceResult(passed, ref, summary), nil
		}

		record := evidenceRecord{
			evidenceSemantic: semantic,
			EvidenceDigest:   digest,
			RecordedAt:       deps.Clock.Now().UTC().Format(time.RFC3339Nano),
		}
		raw, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return host.Result{}, fmt.Errorf("host.compliance.run: encode evidence record: %w", err)
		}
		if len(raw) > limits.MaxEvidenceBytes {
			return host.Result{}, fmt.Errorf(
				"host.compliance.run: encoded evidence record is %d bytes, exceeds %d; refusing to truncate",
				len(raw), limits.MaxEvidenceBytes,
			)
		}
		ref, err := deps.Evidence.Put(ctx, digest, append(raw, '\n'))
		if err != nil {
			return host.Result{}, fmt.Errorf("host.compliance.run: persist evidence: %w", err)
		}
		return complianceResult(passed, ref, summary), nil
	}
}

func resolvedLimits(configured Limits) (Limits, error) {
	if configured.MaxChecks == 0 {
		configured.MaxChecks = maxChecks
	}
	if configured.MaxResolvedBytes == 0 {
		configured.MaxResolvedBytes = maxResolvedBytes
	}
	if configured.MaxEvidenceBytes == 0 {
		configured.MaxEvidenceBytes = maxEvidenceBytes
	}
	switch {
	case configured.MaxChecks < 1 || configured.MaxChecks > maxChecks:
		return Limits{}, fmt.Errorf("host.compliance.run: max checks must be between 1 and %d", maxChecks)
	case configured.MaxResolvedBytes < 1 || configured.MaxResolvedBytes > maxResolvedBytes:
		return Limits{}, fmt.Errorf("host.compliance.run: max resolved bytes must be between 1 and %d", maxResolvedBytes)
	case configured.MaxEvidenceBytes < 1 || configured.MaxEvidenceBytes > maxEvidenceBytes:
		return Limits{}, fmt.Errorf("host.compliance.run: max evidence bytes must be between 1 and %d", maxEvidenceBytes)
	}
	return configured, nil
}

func validateDependencies(deps Dependencies) error {
	switch {
	case strings.TrimSpace(deps.AppID) == "":
		return fmt.Errorf("host.compliance.run: app id is unavailable")
	case strings.TrimSpace(deps.Root) == "":
		return fmt.Errorf("host.compliance.run: app root is unavailable")
	case deps.Authorizer == nil:
		return fmt.Errorf("host.compliance.run: authorizer is unavailable")
	case deps.Catalogs == nil:
		return fmt.Errorf("host.compliance.run: catalog resolver is unavailable")
	case deps.Runner == nil:
		return fmt.Errorf("host.compliance.run: check runner is unavailable")
	case deps.Evidence == nil:
		return fmt.Errorf("host.compliance.run: evidence store is unavailable")
	case deps.Clock == nil:
		return fmt.Errorf("host.compliance.run: clock is unavailable")
	default:
		return nil
	}
}

func parseArgs(args map[string]any) (string, string, error) {
	allowed := map[string]bool{"op": true, "catalog_path": true, "node_id": true}
	for key := range args {
		if !allowed[key] {
			return "", "", fmt.Errorf("host.compliance.run: unknown argument %q", key)
		}
	}
	if op, _ := args["op"].(string); op != "" && op != "run" {
		return "", "", fmt.Errorf("host.compliance: unknown op %q", op)
	}
	catalogPath, ok := args["catalog_path"].(string)
	if !ok || strings.TrimSpace(catalogPath) == "" {
		return "", "", fmt.Errorf("host.compliance.run: catalog_path must be a non-empty string")
	}
	nodeID, ok := args["node_id"].(string)
	if !ok || strings.TrimSpace(nodeID) == "" {
		return "", "", fmt.Errorf("host.compliance.run: node_id must be a non-empty string")
	}
	if len(catalogPath) > maxCatalogPathBytes {
		return "", "", fmt.Errorf("host.compliance.run: catalog_path exceeds %d bytes", maxCatalogPathBytes)
	}
	if len(nodeID) > maxNodeIDBytes {
		return "", "", fmt.Errorf("host.compliance.run: node_id exceeds %d bytes", maxNodeIDBytes)
	}
	return catalogPath, nodeID, nil
}

func validateResolved(resolved ResolvedNode, limits Limits) error {
	if resolved.CatalogDigest == "" || resolved.TypeID == "" {
		return fmt.Errorf("host.compliance.run: resolver returned incomplete typed node metadata")
	}
	if len(resolved.Checks) == 0 {
		return fmt.Errorf("host.compliance.run: node %q has no materialize checks", resolved.NodeID)
	}
	if len(resolved.Checks) > limits.MaxChecks {
		return fmt.Errorf(
			"host.compliance.run: node %q resolves %d checks, exceeds %d; refusing to truncate",
			resolved.NodeID, len(resolved.Checks), limits.MaxChecks,
		)
	}
	raw, err := json.Marshal(resolved.Checks)
	if err != nil {
		return fmt.Errorf("host.compliance.run: encode resolved checks: %w", err)
	}
	if len(raw) > limits.MaxResolvedBytes {
		return fmt.Errorf(
			"host.compliance.run: resolved checks encode to %d bytes, exceeds %d; refusing to truncate",
			len(raw), limits.MaxResolvedBytes,
		)
	}
	seen := map[string]bool{}
	for _, check := range resolved.Checks {
		if strings.TrimSpace(check.ID) == "" || len(check.ID) > maxNodeIDBytes {
			return fmt.Errorf("host.compliance.run: resolved check id is empty or exceeds %d bytes", maxNodeIDBytes)
		}
		if seen[check.ID] {
			return fmt.Errorf("host.compliance.run: duplicate resolved check id %q", check.ID)
		}
		seen[check.ID] = true
		if err := deterministicCapabilities(check.Capabilities); err != nil {
			return fmt.Errorf("host.compliance.run: check %q: %w", check.ID, err)
		}
	}
	return nil
}

func deterministicCapabilities(raw map[string]any) error {
	spec, err := starlarkhost.ParseCapabilities(raw)
	if err != nil {
		return err
	}
	switch {
	case spec.HTTP.Enabled:
		return fmt.Errorf("http capability is not allowed")
	case len(spec.Host.Verbs) > 0:
		return fmt.Errorf("host capability is not allowed")
	case len(spec.FS.WritePatterns) > 0:
		return fmt.Errorf("filesystem write capability is not allowed")
	default:
		return nil
	}
}

func complianceSummary(results []materialize.CheckResult) string {
	passed := 0
	failed := make([]string, 0)
	for _, result := range results {
		if result.OK {
			passed++
		} else {
			failed = append(failed, result.ID)
		}
	}
	if len(failed) == 0 {
		return fmt.Sprintf("%d/%d compliance checks passed", passed, len(results))
	}
	sort.Strings(failed)
	return fmt.Sprintf("%d/%d compliance checks passed; failed: %s", passed, len(results), strings.Join(failed, ", "))
}

func complianceResult(passed bool, ref, summary string) host.Result {
	return host.Result{Data: map[string]any{
		"passed": passed, "evidence_ref": ref, "summary": summary,
	}}
}

type evidenceSemantic struct {
	Schema        string                    `json:"schema"`
	AppID         string                    `json:"app_id"`
	CatalogDigest string                    `json:"catalog_digest"`
	NodeID        string                    `json:"node_id"`
	TypeID        string                    `json:"type_id"`
	Passed        bool                      `json:"passed"`
	Summary       string                    `json:"summary"`
	Checks        []materialize.CheckResult `json:"checks"`
}

type evidenceRecord struct {
	evidenceSemantic
	EvidenceDigest string `json:"evidence_digest"`
	RecordedAt     string `json:"recorded_at"`
}

// GraphCatalogResolver uses the same graph/materialize APIs as the server's
// graph.materialize.checks RPC, with an additional app-root containment gate.
type GraphCatalogResolver struct{}

func (GraphCatalogResolver) Resolve(_ context.Context, root, catalogPath, nodeID string) (ResolvedNode, error) {
	catalogAbs, err := containedExistingPath(root, catalogPath)
	if err != nil {
		return ResolvedNode{}, fmt.Errorf("catalog path: %w", err)
	}
	cat, err := graph.LoadCatalog(catalogAbs)
	if err != nil {
		return ResolvedNode{}, err
	}
	node, ok := cat.Nodes[graph.NodeID(nodeID)]
	if !ok {
		return ResolvedNode{}, fmt.Errorf("node %q not found in catalog", nodeID)
	}
	binding, err := materialize.ResolveBinding(cat, node)
	if err != nil {
		return ResolvedNode{}, err
	}
	checks := materialize.ResolveChecks(node, binding.Checks)
	for _, check := range checks {
		if check.Unresolved != "" {
			continue
		}
		if filepath.Ext(check.Script) != ".star" {
			return ResolvedNode{}, fmt.Errorf("check %q script must use the .star extension", check.ID)
		}
		if _, err := containedExistingPath(root, check.Script); err != nil {
			return ResolvedNode{}, fmt.Errorf("check %q script path: %w", check.ID, err)
		}
	}
	return ResolvedNode{
		CatalogDigest: cat.ContentDigest,
		NodeID:        string(node.ID),
		TypeID:        binding.TypeID,
		Checks:        checks,
	}, nil
}

// MaterializeCheckRunner delegates to the existing deterministic check runner.
type MaterializeCheckRunner struct{}

func (MaterializeCheckRunner) Run(ctx context.Context, root string, check materialize.ResolvedCheck) materialize.CheckResult {
	return materialize.RunCheck(ctx, root, check)
}

// BoundAuthorizer enforces the immutable application identity and root.
type BoundAuthorizer struct {
	AppID string
	Root  string
}

func (a BoundAuthorizer) Authorize(_ context.Context, scope Scope) error {
	if scope.AppID != a.AppID {
		return fmt.Errorf("application %q is outside bound scope", scope.AppID)
	}
	want, err := filepath.Abs(a.Root)
	if err != nil {
		return fmt.Errorf("resolve bound root: %w", err)
	}
	got, err := filepath.Abs(scope.Root)
	if err != nil {
		return fmt.Errorf("resolve requested root: %w", err)
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		return fmt.Errorf("application root is outside bound scope")
	}
	return nil
}

// FileEvidenceStore persists content-addressed JSON and returns opaque refs.
type FileEvidenceStore struct {
	Dir string
}

func (s FileEvidenceStore) Get(_ context.Context, digest string) (string, bool, error) {
	path, err := s.path(digest)
	if err != nil {
		return "", false, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var record struct {
		EvidenceDigest string `json:"evidence_digest"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return "", false, fmt.Errorf("decode existing evidence: %w", err)
	}
	if record.EvidenceDigest != digest {
		return "", false, fmt.Errorf("existing evidence digest mismatch")
	}
	return evidenceRef(digest), true, nil
}

func (s FileEvidenceStore) Put(ctx context.Context, digest string, raw []byte) (string, error) {
	if _, ok, err := s.Get(ctx, digest); err != nil {
		return "", err
	} else if ok {
		return evidenceRef(digest), nil
	}
	path, err := s.path(digest)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(s.Dir, ".compliance-*.tmp")
	if err != nil {
		return "", err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", err
	}
	_, writeErr := file.Write(raw)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	// Link publishes the fully-synced temp file without overwriting an
	// equivalent record another session may have won concurrently.
	if err := os.Link(tempPath, path); err != nil {
		if os.IsExist(err) {
			if _, ok, getErr := s.Get(ctx, digest); getErr != nil {
				return "", getErr
			} else if ok {
				return evidenceRef(digest), nil
			}
		}
		return "", err
	}
	if dir, err := os.Open(s.Dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return evidenceRef(digest), nil
}

func (s FileEvidenceStore) path(digest string) (string, error) {
	if len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("invalid evidence digest")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("invalid evidence digest")
	}
	if strings.TrimSpace(s.Dir) == "" {
		return "", fmt.Errorf("evidence directory is unavailable")
	}
	return filepath.Join(s.Dir, digest+".json"), nil
}

func evidenceRef(digest string) string {
	return "kitsoki://compliance/sha256/" + digest
}

func containedExistingPath(root, candidate string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	path := candidate
	if !filepath.IsAbs(path) {
		path = filepath.Join(rootReal, path)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	pathReal, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootReal, pathReal)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q escapes application root", candidate)
	}
	return pathReal, nil
}

// DiscoverRoot finds the containing project root without invoking git.
func DiscoverRoot(appPath string) string {
	start, err := filepath.Abs(filepath.Dir(appPath))
	if err != nil {
		return filepath.Dir(appPath)
	}
	for dir := start; ; dir = filepath.Dir(dir) {
		for _, marker := range []string{".git", ".kitsoki.yaml", ".kitsoki-root"} {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
	}
}
