package storydemo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kitsoki/internal/graph"
	"kitsoki/internal/materialize"
)

// GraphResolver uses graph type declarations and node fields, never Story
// supplied commands or paths, to construct private provider inputs.
type GraphResolver struct{}

func (GraphResolver) Plan(_ context.Context, root, catalogPath, nodeID string) (Plan, error) {
	cat, node, binding, path, err := loadBoundNode(root, catalogPath, nodeID)
	if err != nil {
		return Plan{}, err
	}
	closure, err := dependencyClosure(cat, node, binding)
	if err != nil {
		return Plan{}, err
	}
	manifestPath, err := manifestPathFor(root, node, binding)
	if err != nil {
		return Plan{}, err
	}
	artifacts, err := nodeArtifacts(root, node, true)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		CatalogDigest: cat.ContentDigest, CatalogPath: path, NodeID: nodeID,
		ClosureOrder: closure, Manifest: Manifest{Path: manifestPath}, Artifacts: artifacts,
	}, nil
}

func (GraphResolver) Materialization(_ context.Context, root, catalogPath, nodeID, phase string) (Materialization, error) {
	cat, node, binding, _, err := loadBoundNode(root, catalogPath, nodeID)
	if err != nil {
		return Materialization{}, err
	}
	closure, err := dependencyClosure(cat, node, binding)
	if err != nil {
		return Materialization{}, err
	}
	var taskIDs []string
	switch phase {
	case "dependencies":
		if len(closure) > 1 {
			taskIDs = append(taskIDs, closure[:len(closure)-1]...)
		}
	case "subject":
		taskIDs = []string{nodeID}
	case "verify":
		taskIDs = []string{nodeID}
	default:
		return Materialization{}, fmt.Errorf("unsupported materialization phase %q", phase)
	}
	if len(taskIDs) > maxTasks {
		return Materialization{}, fmt.Errorf("phase %q resolves %d tasks, exceeds %d; refusing to truncate", phase, len(taskIDs), maxTasks)
	}
	var tasks []Task
	var artifacts []Artifact
	seenArtifacts := map[string]bool{}
	for _, id := range taskIDs {
		target, ok := cat.Nodes[graph.NodeID(id)]
		if !ok {
			return Materialization{}, fmt.Errorf("materialization node %q is absent", id)
		}
		targetArtifacts, err := nodeArtifacts(root, target, true)
		if err != nil {
			return Materialization{}, err
		}
		targetBinding, err := materialize.ResolveBinding(cat, target)
		if err == nil {
			if path, manifestErr := manifestPathFor(root, target, targetBinding); manifestErr == nil {
				targetArtifacts = append(targetArtifacts, Artifact{Kind: "manifest", Path: path})
			}
		}
		tasks = append(tasks, Task{ID: id, Phase: phase, Artifacts: targetArtifacts})
		for _, artifact := range targetArtifacts {
			key := artifact.Kind + "\x00" + artifact.Path
			if !seenArtifacts[key] {
				seenArtifacts[key] = true
				artifacts = append(artifacts, artifact)
			}
		}
	}
	return Materialization{
		CatalogDigest: cat.ContentDigest, NodeID: nodeID, Phase: phase,
		Tasks: tasks, Artifacts: artifacts,
	}, nil
}

func (GraphResolver) ProjectMockup(_ context.Context, root, catalogPath, nodeID, audience string) (MockupProjection, error) {
	cat, node, _, _, err := loadBoundNode(root, catalogPath, nodeID)
	if err != nil {
		return MockupProjection{}, err
	}
	if audience == "public" && node.Visibility != graph.VisibilityPublic {
		return MockupProjection{}, fmt.Errorf("node %q is not public", nodeID)
	}
	summary := firstString(node, "summary", "description", "goal", "statement")
	if summary == "" {
		summary = node.Title
	}
	states := map[string]any{
		"start": scenarioState(node.Title, summary, 0),
		"done":  scenarioState(node.Title, summary, 1),
	}
	actionIDs, err := nodeActionIDs(node)
	if err != nil {
		return MockupProjection{}, err
	}
	scenario := map[string]any{
		"version": 1, "title": node.Title, "tagline": summary,
		"actions": actionIDs,
		"zones": map[string]any{
			"rail":      map[string]any{"heading": "Scenarios", "typeHeading": "Types", "scenarios": [][]string{{node.Title, summary}}, "types": []string{node.TypeID}},
			"intake":    map[string]any{"heading": "Context", "fields": []string{"Decision", "Claim", "Constraint"}},
			"graph":     map[string]any{"projection": "projection.json"},
			"inspector": map[string]any{"heading": "Inspector", "evidenceHeading": "Evidence"},
			"timeline":  map[string]any{"heading": "Timeline"},
		},
		"states": states,
	}
	raw, err := json.Marshal(scenario)
	if err != nil {
		return MockupProjection{}, err
	}
	if len(raw) > maxPayloadBytes {
		return MockupProjection{}, fmt.Errorf("projected scenario is %d bytes, exceeds %d; refusing to truncate", len(raw), maxPayloadBytes)
	}
	return MockupProjection{
		CatalogDigest: cat.ContentDigest, NodeID: nodeID, Audience: audience, Scenario: raw,
		Manifest: MockupManifest{
			Scenario: raw, ActionIDs: actionIDs,
		},
	}, nil
}

func nodeActionIDs(node *graph.Node) ([]string, error) {
	raw, _ := node.Fields["demo_actions"].([]any)
	if len(raw) > maxTasks {
		return nil, fmt.Errorf("node %q declares %d demo actions, exceeds %d; refusing to truncate", node.ID, len(raw), maxTasks)
	}
	out := make([]string, 0, len(raw))
	for index, value := range raw {
		id, ok := value.(string)
		if !ok || strings.TrimSpace(id) == "" || len(id) > maxNodeIDBytes {
			return nil, fmt.Errorf("node %q demo_actions[%d] must be a bounded action ID", node.ID, index)
		}
		out = append(out, id)
	}
	return out, nil
}

func loadBoundNode(root, catalogPath, nodeID string) (*graph.Catalog, *graph.Node, *materialize.Binding, string, error) {
	path, err := containedExistingPath(root, catalogPath)
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("catalog path: %w", err)
	}
	cat, err := graph.LoadCatalog(path)
	if err != nil {
		return nil, nil, nil, "", err
	}
	node, ok := cat.Nodes[graph.NodeID(nodeID)]
	if !ok {
		return nil, nil, nil, "", fmt.Errorf("node %q not found in catalog", nodeID)
	}
	binding, err := materialize.ResolveBinding(cat, node)
	if err != nil {
		return nil, nil, nil, "", err
	}
	return cat, node, binding, path, nil
}

func dependencyClosure(cat *graph.Catalog, root *graph.Node, binding *materialize.Binding) ([]string, error) {
	edgeIDs := map[graph.EdgeField]bool{}
	for _, param := range binding.Params {
		if param.SourceEdge != "" {
			edgeIDs[param.SourceEdge] = true
		}
	}
	var order []string
	visiting := map[graph.NodeID]bool{}
	visited := map[graph.NodeID]bool{}
	var visit func(*graph.Node) error
	visit = func(node *graph.Node) error {
		if visiting[node.ID] {
			return fmt.Errorf("dependency cycle at node %q", node.ID)
		}
		if visited[node.ID] {
			return nil
		}
		if len(visited) >= maxClosureNodes {
			return fmt.Errorf("dependency closure exceeds %d nodes; refusing to truncate", maxClosureNodes)
		}
		visiting[node.ID] = true
		eff, ok := cat.Registry.Effective(node.TypeID)
		if !ok {
			return fmt.Errorf("unknown type %q for node %q", node.TypeID, node.ID)
		}
		var targets []graph.NodeID
		for _, decl := range eff.EdgeFields {
			if edgeIDs[decl.ID] {
				targets = append(targets, node.EdgeTargets(decl)...)
			}
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
		for _, id := range targets {
			target, ok := cat.Nodes[id]
			if !ok {
				return fmt.Errorf("dependency %q referenced by %q is absent", id, node.ID)
			}
			if err := visit(target); err != nil {
				return err
			}
		}
		delete(visiting, node.ID)
		visited[node.ID] = true
		order = append(order, string(node.ID))
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	return order, nil
}

func manifestPathFor(root string, node *graph.Node, binding *materialize.Binding) (string, error) {
	for _, param := range binding.Params {
		if param.SourceField == "" {
			continue
		}
		name := strings.ToLower(param.ID + " " + param.SourceField)
		if !strings.Contains(name, "manifest") {
			continue
		}
		raw, _ := node.Fields[param.SourceField].(string)
		if raw == "" {
			break
		}
		path, err := containedExistingPath(root, raw)
		if err != nil {
			return "", fmt.Errorf("manifest declaration: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("node %q has no server-declared manifest path", node.ID)
}

func nodeArtifacts(root string, node *graph.Node, mustExist bool) ([]Artifact, error) {
	raw, _ := node.Fields["evidence"].([]any)
	if len(raw) > maxArtifacts {
		return nil, fmt.Errorf("node %q declares %d artifacts, exceeds %d; refusing to truncate", node.ID, len(raw), maxArtifacts)
	}
	out := make([]Artifact, 0, len(raw))
	for i, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("evidence[%d] must be an object", i)
		}
		pathValue, _ := entry["path"].(string)
		if pathValue == "" {
			continue
		}
		path, err := containedPath(root, pathValue, mustExist)
		if err != nil {
			return nil, fmt.Errorf("evidence[%d]: %w", i, err)
		}
		kind, _ := entry["kind"].(string)
		if kind == "" {
			kind = "artifact"
		}
		out = append(out, Artifact{Kind: kind, Path: path})
	}
	return out, nil
}

func scenarioState(title, summary string, done int) map[string]any {
	return map[string]any{
		"group":  "subject",
		"rail":   map[string]any{"active": 0, "decision": title, "detail": summary},
		"intake": []string{title, summary, "Typed Story Application"},
		"graph":  map[string]any{"title": title, "sub": summary, "state": "subject"},
		"inspector": map[string]any{
			"headline": title, "meaning": summary, "metrics": [][]string{{"status", fmt.Sprint(done)}},
			"evidence": [][]string{{"green", title, "catalog projection"}}, "action": "Review evidence",
		},
		"timeline": map[string]any{"steps": []string{"Project", "Create"}, "done": done},
	}
}

func firstString(node *graph.Node, fields ...string) string {
	for _, field := range fields {
		if value, ok := node.Fields[field].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func containedExistingPath(root, candidate string) (string, error) {
	return containedPath(root, candidate, true)
}

func containedPath(root, candidate string, existing bool) (string, error) {
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
	} else if rel, relErr := filepath.Rel(rootAbs, path); relErr == nil &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// macOS commonly exposes /var through the /private/var symlink. Preserve
		// the caller's root-relative identity while comparing against rootReal.
		path = filepath.Join(rootReal, rel)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	check := path
	if existing {
		check, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
	} else {
		parent, evalErr := filepath.EvalSymlinks(filepath.Dir(path))
		if evalErr == nil {
			check = filepath.Join(parent, filepath.Base(path))
		}
	}
	rel, err := filepath.Rel(rootReal, check)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q escapes application root", candidate)
	}
	return check, nil
}

func artifactMetadata(root string, artifact Artifact) (artifactRecord, error) {
	path, err := containedExistingPath(root, artifact.Path)
	if err != nil {
		return artifactRecord{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return artifactRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return artifactRecord{}, fmt.Errorf("%q is not a regular file", path)
	}
	if info.Size() > maxArtifactBytes {
		return artifactRecord{}, fmt.Errorf("artifact %q is %d bytes, exceeds %d; refusing to truncate", filepath.Base(path), info.Size(), maxArtifactBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return artifactRecord{}, err
	}
	return artifactRecord{Kind: artifact.Kind, Path: path, Size: info.Size(), Digest: semanticDigest("file", raw)}, nil
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
