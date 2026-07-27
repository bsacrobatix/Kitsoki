package applicationassurance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"kitsoki/internal/graph"
	"kitsoki/internal/host"
)

type SuiteBinding struct {
	ID      string
	App     string
	Flows   string
	Version string
}

// ResolvedBinding contains only server-resolved absolute paths. It is built
// once when a session runtime is constructed and never exposed to story code.
type ResolvedBinding struct {
	Root        string
	CatalogPath string
	Suites      []host.FlowEvidenceSuite
}

func ResolveBinding(
	root, catalog string,
	suites []SuiteBinding,
	maxSuiteBytes int,
) (ResolvedBinding, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return ResolvedBinding{}, fmt.Errorf("application assurance root: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return ResolvedBinding{}, fmt.Errorf("application assurance root: %w", err)
	}
	catalogAbs, err := containedRegularFile(rootReal, catalog)
	if err != nil {
		return ResolvedBinding{}, fmt.Errorf("application assurance catalog: %w", err)
	}
	resolved := ResolvedBinding{
		Root: rootReal, CatalogPath: catalogAbs,
		Suites: make([]host.FlowEvidenceSuite, 0, len(suites)),
	}
	for _, suite := range suites {
		appPath, err := containedRegularFile(rootReal, suite.App)
		if err != nil {
			return ResolvedBinding{}, fmt.Errorf("application assurance suite %q app: %w", suite.ID, err)
		}
		flowGlob, revision, err := resolveFlowGlob(
			rootReal, suite.Flows, appPath, suite.Version, maxSuiteBytes,
		)
		if err != nil {
			return ResolvedBinding{}, fmt.Errorf("application assurance suite %q flows: %w", suite.ID, err)
		}
		resolved.Suites = append(resolved.Suites, host.FlowEvidenceSuite{
			ID: suite.ID, Revision: revision, AppPath: appPath, FlowGlob: flowGlob,
		})
	}
	sort.Slice(resolved.Suites, func(i, j int) bool {
		return resolved.Suites[i].ID < resolved.Suites[j].ID
	})
	return resolved, nil
}

// CatalogResolver verifies the semantic node exists in the bound catalog and
// returns only the pre-resolved deterministic suites.
type CatalogResolver struct {
	Binding ResolvedBinding
}

func (r CatalogResolver) ResolveFlowEvidence(
	_ context.Context,
	request host.FlowEvidenceResolveRequest,
) (host.FlowEvidenceTarget, error) {
	if request.Scope.CatalogPath != r.Binding.CatalogPath {
		return host.FlowEvidenceTarget{}, fmt.Errorf("registered catalog scope changed")
	}
	catalog, err := graph.LoadCatalog(r.Binding.CatalogPath)
	if err != nil {
		return host.FlowEvidenceTarget{}, err
	}
	if _, ok := catalog.Nodes[graph.NodeID(request.NodeID)]; !ok {
		return host.FlowEvidenceTarget{}, fmt.Errorf("node %q not found in catalog", request.NodeID)
	}
	return host.FlowEvidenceTarget{
		Scope:           request.Scope,
		CatalogRevision: catalog.ContentDigest,
		NodeID:          request.NodeID,
		Suites:          append([]host.FlowEvidenceSuite(nil), r.Binding.Suites...),
	}, nil
}

// NewSemanticHandler constrains a legacy typed operation to the Story
// Application contract and injects its registered catalog path.
func NewSemanticHandler(
	name, operation, catalogPath string,
	delegate host.Handler,
) host.Handler {
	return func(ctx context.Context, args map[string]any) (host.Result, error) {
		nodeID, err := semanticNodeID(name, operation, args)
		if err != nil {
			return host.Result{}, err
		}
		return delegate(ctx, map[string]any{
			"op": operation, "catalog_path": catalogPath, "node_id": nodeID,
		})
	}
}

func semanticNodeID(name, operation string, args map[string]any) (string, error) {
	if key, ok := prohibitedAuthorityKey(args); ok {
		return "", fmt.Errorf("%s.%s: story input cannot supply authority key %q", name, operation, key)
	}
	for key := range args {
		if key != "op" && key != "node_id" {
			return "", fmt.Errorf("%s.%s: unknown argument %q", name, operation, key)
		}
	}
	if op, _ := args["op"].(string); op != "" && op != operation {
		return "", fmt.Errorf("%s: unknown op %q", name, op)
	}
	nodeID, ok := args["node_id"].(string)
	if !ok || !graph.IsKebabID(nodeID) {
		return "", fmt.Errorf("%s.%s: node_id must be a catalog node id", name, operation)
	}
	return nodeID, nil
}

func prohibitedAuthorityKey(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if authorityKey(key) {
				return key, true
			}
			if nested, ok := prohibitedAuthorityKey(child); ok {
				return nested, true
			}
		}
	case []any:
		for _, child := range typed {
			if nested, ok := prohibitedAuthorityKey(child); ok {
				return nested, true
			}
		}
	}
	return "", false
}

func authorityKey(key string) bool {
	if key == "node_id" || key == "op" {
		return false
	}
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return '_'
	}, key)
	for _, token := range strings.FieldsFunc(normalized, func(r rune) bool { return r == '_' }) {
		switch token {
		case "path", "url", "uri", "command", "cmd", "program", "script",
			"provider", "profile", "actor", "session", "transport", "bound",
			"bounds", "limit", "limits", "max", "root", "catalog", "suite",
			"evidence", "runner", "store", "application", "app", "cwd",
			"workdir", "repository", "repo":
			return true
		}
	}
	return false
}

func containedRegularFile(root, candidate string) (string, error) {
	if filepath.IsAbs(candidate) {
		return "", fmt.Errorf("path must be repository-relative")
	}
	joined := filepath.Join(root, candidate)
	real, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	if !contained(root, real) {
		return "", fmt.Errorf("path escapes repository root")
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file")
	}
	return real, nil
}

func resolveFlowGlob(
	root, pattern, appPath, version string,
	maxBytes int,
) (string, string, error) {
	absolutePattern := filepath.Join(root, pattern)
	matches, err := filepath.Glob(absolutePattern)
	if err != nil {
		return "", "", err
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("glob matched no flow fixtures")
	}
	sort.Strings(matches)
	sum := sha256.New()
	total := 0
	files := append([]string{appPath}, matches...)
	for _, file := range files {
		real, err := filepath.EvalSymlinks(file)
		if err != nil {
			return "", "", err
		}
		if !contained(root, real) {
			return "", "", fmt.Errorf("matched file escapes repository root")
		}
		info, err := os.Stat(real)
		if err != nil {
			return "", "", err
		}
		if !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("matched path is not a regular file")
		}
		raw, err := os.ReadFile(real)
		if err != nil {
			return "", "", err
		}
		total += len(raw)
		if total > maxBytes {
			return "", "", fmt.Errorf("suite input exceeds %d bytes", maxBytes)
		}
		relative, err := filepath.Rel(root, real)
		if err != nil {
			return "", "", err
		}
		_, _ = sum.Write([]byte(filepath.ToSlash(relative)))
		_, _ = sum.Write([]byte{0})
		_, _ = sum.Write(raw)
	}
	_, _ = sum.Write([]byte(version))
	return absolutePattern, hex.EncodeToString(sum.Sum(nil)), nil
}

func contained(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
