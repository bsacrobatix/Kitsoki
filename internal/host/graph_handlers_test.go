package host

import (
	"context"
	"testing"

	objectgraph "kitsoki/internal/graph"
)

const seedCatalogPath = "../../docs/proposals/project-object-graph/seed-objects.yaml"
const seedOverlayPath = "../../docs/proposals/project-object-graph/seed-objects.overlay-ui-declutter.yaml"

// TestGraphHandler_Project exercises the host.graph.project op end to end
// (the S5 replacement for the deleted internal/app/graph/objectcatalog.go —
// see objectCatalogGraph/objectCatalogDiffGraph below for the ported
// nests_under / diff-badging regression checks against those unexported
// helpers directly).
func TestGraphHandler_Project(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "project",
		"catalog_path": seedCatalogPath,
		"graph_id":     "test-project",
	})
	if err != nil {
		t.Fatalf("GraphHandler(project): %v", err)
	}
	wire, ok := res.Data["graph"].(interface{})
	if !ok || wire == nil {
		t.Fatal("expected result.Data[\"graph\"]")
	}
}

// TestGraphHandler_Project_RegistryCarriesArtifactMaterialize is the C1
// regression test for the kit-mode gap this passthrough closes: before it,
// host.graph.project's response had no "registry" key at all, so a
// kit-served portal (unlike the Vite-dev-only /api/catalog splice route)
// never saw a type's artifact:/materialize: declaration — the Materialize
// button/gates checklist silently never rendered outside `npm run dev`.
func TestGraphHandler_Project_RegistryCarriesArtifactMaterialize(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "project",
		"catalog_path": "../materialize/testdata/catalog.yaml",
		"graph_id":     "test-registry",
	})
	if err != nil {
		t.Fatalf("GraphHandler(project): %v", err)
	}
	registry, ok := res.Data["registry"].([]map[string]any)
	if !ok {
		t.Fatalf("expected result.Data[\"registry\"] as []map[string]any, got %T", res.Data["registry"])
	}
	var workItem map[string]any
	for _, entry := range registry {
		if entry["id"] == "work-item" {
			workItem = entry
			break
		}
	}
	if workItem == nil {
		t.Fatalf("no \"work-item\" entry in registry passthrough: %v", registry)
	}
	artifact, ok := workItem["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("work-item registry entry missing artifact: %v", workItem)
	}
	materialize, ok := artifact["materialize"].(map[string]any)
	if !ok {
		t.Fatalf("work-item artifact missing materialize: %v", artifact)
	}
	if materialize["story"] != "story" {
		t.Errorf("materialize.story = %v, want %q", materialize["story"], "story")
	}
	gates, _ := materialize["gates"].([]string)
	if len(gates) != 2 || gates[0] != "gate" || gates[1] != "owner" {
		t.Errorf("materialize.gates = %v, want [gate owner]", materialize["gates"])
	}
	for _, entry := range registry {
		if entry["id"] == "core-node" {
			t.Error("core-node should be filtered out of the registry passthrough, matching vite.config.ts's dev route")
		}
	}
}

func TestGraphHandler_Load(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "load",
		"catalog_path": seedCatalogPath,
	})
	if err != nil {
		t.Fatalf("GraphHandler(load): %v", err)
	}
	if res.Data["node_count"].(int) == 0 {
		t.Fatal("expected node_count > 0")
	}
}

func TestGraphHandler_Lint(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "lint",
		"catalog_path": seedCatalogPath,
	})
	if err != nil {
		t.Fatalf("GraphHandler(lint): %v", err)
	}
	if _, ok := res.Data["issues"]; !ok {
		t.Fatal("expected issues key")
	}
}

func TestGraphHandler_UnknownOp(t *testing.T) {
	_, err := GraphHandler(context.Background(), map[string]any{"op": "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown op")
	}
}

func TestGraphHandler_PresentationRequiresKitDir(t *testing.T) {
	_, err := GraphHandler(context.Background(), map[string]any{"op": "presentation"})
	if err == nil {
		t.Fatal("expected error when _kit_dir is missing")
	}
}

func TestObjectCatalogDiffGraph_BadgesTheOverlayAddition(t *testing.T) {
	current, err := objectgraph.LoadCatalog(seedCatalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog(base): %v", err)
	}
	desired, err := objectgraph.LoadCatalogWithOverlay(seedCatalogPath, seedOverlayPath)
	if err != nil {
		t.Fatalf("LoadCatalogWithOverlay: %v", err)
	}

	g := objectCatalogDiffGraph(current, desired, "test-diff")
	if g.Kind != "object-graph-diff" {
		t.Errorf("g.Kind = %q, want object-graph-diff", g.Kind)
	}
	if len(g.Nodes) != len(desired.Nodes) {
		t.Fatalf("diff graph has %d nodes, want %d (no removed nodes in this fixture pair)", len(g.Nodes), len(desired.Nodes))
	}

	var added, unchanged int
	for _, n := range g.Nodes {
		switch n.Attrs["diff_kind"] {
		case "added":
			added++
			if n.ID != "evidence-object-graph-ui-persona-review" {
				t.Errorf("unexpected added node %q", n.ID)
			}
		case "unchanged":
			unchanged++
		default:
			t.Errorf("node %q has unexpected diff_kind %v", n.ID, n.Attrs["diff_kind"])
		}
	}
	if added != 1 {
		t.Errorf("added = %d, want exactly 1", added)
	}
	if unchanged != len(current.Nodes) {
		t.Errorf("unchanged = %d, want %d (every base node)", unchanged, len(current.Nodes))
	}
}

func TestObjectCatalogGraph_NestsUnderThreadsToWireEdge(t *testing.T) {
	cat, err := objectgraph.LoadCatalog(seedCatalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	g := objectCatalogGraph(cat, "test")

	var found bool
	for _, e := range g.Edges {
		if e.Kind != "child_of" {
			continue
		}
		found = true
		if e.Attrs["nests_under"] != true {
			t.Errorf("child_of edge %s: attrs[nests_under] = %v, want true", e.ID, e.Attrs["nests_under"])
		}
	}
	if !found {
		t.Fatal("expected at least one child_of edge in the wire graph")
	}

	for _, e := range g.Edges {
		if e.Kind != "proposes" {
			continue
		}
		if e.Attrs["nests_under"] == true {
			t.Errorf("proposes edge %s unexpectedly carries attrs[nests_under]=true", e.ID)
		}
	}
}
func TestGraphHandler_Query(t *testing.T) {
	// refs-to mode
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "query",
		"catalog_path": seedCatalogPath,
		"mode":         "refs-to",
		"target":       "usecase-developer-traces-requirement-to-proof",
	})
	if err != nil {
		t.Fatalf("GraphHandler(query refs-to): %v", err)
	}
	refs, ok := res.Data["references"].([]any)
	if !ok {
		t.Fatal("expected references list")
	}
	if len(refs) == 0 {
		t.Fatal("expected references to not be empty")
	}

	// explain-type mode
	res, err = GraphHandler(context.Background(), map[string]any{
		"op":           "query",
		"catalog_path": seedCatalogPath,
		"mode":         "explain-type",
		"target":       "feature",
	})
	if err != nil {
		t.Fatalf("GraphHandler(query explain-type): %v", err)
	}
	if res.Data["type_id"] != "feature" {
		t.Errorf("expected type_id feature, got %v", res.Data["type_id"])
	}

	// impact mode
	res, err = GraphHandler(context.Background(), map[string]any{
		"op":           "query",
		"catalog_path": seedCatalogPath,
		"mode":         "impact",
		"target":       "usecase-developer-traces-requirement-to-proof",
		"to_type":      "feature",
	})
	if err != nil {
		t.Fatalf("GraphHandler(query impact): %v", err)
	}
	if res.Data["node_id"] != "usecase-developer-traces-requirement-to-proof" {
		t.Errorf("expected node_id usecase-developer-traces-requirement-to-proof, got %v", res.Data["node_id"])
	}
}

// impactFixturePath is a purpose-built catalog (linear 3-level inbound
// chain, an inbound cycle, a second edge kind, and an unrelated component —
// see the file's header comment) for host.graph.query{mode:"impact"}'s
// transitive option. Shared with internal/graph's TransitiveImpact unit
// tests (../graph/neighbors_test.go's impactFixturePath) via the same file.
const impactFixturePath = "testdata/graph-impact-fixture.yaml"

// TestGraphHandler_Query_Impact_DefaultIsByteCompatOneHop is the byte-compat
// regression test for the transitive option: when transitive is
// absent/false, the response must carry exactly the same key set as before
// the option existed — no "transitive", "edge_kinds", or "impact_closure"
// key at all (not even a false/empty one) — and "references" must remain
// the original one-row-per-(node,edge_field) one-hop list.
func TestGraphHandler_Query_Impact_DefaultIsByteCompatOneHop(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "query",
		"catalog_path": impactFixturePath,
		"mode":         "impact",
		"target":       "target",
	})
	if err != nil {
		t.Fatalf("GraphHandler(query impact): %v", err)
	}

	wantKeys := []string{"node_id", "current_type", "explain_type", "references", "incompatible_refs"}
	if len(res.Data) != len(wantKeys) {
		t.Fatalf("default response has %d keys %v, want exactly %v", len(res.Data), res.Data, wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := res.Data[k]; !ok {
			t.Errorf("missing expected key %q in default (non-transitive) response: %v", k, res.Data)
		}
	}
	for _, forbidden := range []string{"transitive", "edge_kinds", "impact_closure"} {
		if _, ok := res.Data[forbidden]; ok {
			t.Errorf("default (transitive absent) response unexpectedly carries %q — byte-compat broken: %v", forbidden, res.Data)
		}
	}

	refs, ok := res.Data["references"].([]any)
	if !ok || len(refs) != 3 {
		t.Fatalf("expected exactly 3 one-hop references (blk1/blocks, c1/depends_on, cy1/depends_on), got %v", res.Data["references"])
	}
}

// TestGraphHandler_Query_Impact_Transitive exercises transitive:true end to
// end through the host op: the closure covers all 6 transitively impacted
// nodes (see graph-impact-fixture.yaml), in deterministic BFS order, while
// "references" stays exactly the one-hop list from the default-mode test
// above — transitive mode is additive, not a replacement.
func TestGraphHandler_Query_Impact_Transitive(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "query",
		"catalog_path": impactFixturePath,
		"mode":         "impact",
		"target":       "target",
		"transitive":   true,
	})
	if err != nil {
		t.Fatalf("GraphHandler(query impact transitive): %v", err)
	}
	if res.Data["transitive"] != true {
		t.Errorf("expected transitive=true echoed back, got %v", res.Data["transitive"])
	}
	if _, ok := res.Data["edge_kinds"]; ok {
		t.Errorf("edge_kinds should be absent from the response when not supplied, got %v", res.Data["edge_kinds"])
	}

	refs, ok := res.Data["references"].([]any)
	if !ok || len(refs) != 3 {
		t.Fatalf("expected references to remain the 3-row one-hop list even in transitive mode, got %v", res.Data["references"])
	}

	closure, ok := res.Data["impact_closure"].([]any)
	if !ok {
		t.Fatalf("expected impact_closure list, got %T %v", res.Data["impact_closure"], res.Data["impact_closure"])
	}
	if len(closure) != 6 {
		t.Fatalf("expected 6 impacted nodes, got %d: %+v", len(closure), closure)
	}

	first, ok := closure[0].(map[string]any)
	if !ok {
		t.Fatalf("expected impact_closure[0] to be an object, got %T", closure[0])
	}
	if first["node"] != "blk1" || first["edge_field"] != "blocks" || first["depth"] != 1 {
		t.Errorf("impact_closure[0] = %+v, want node=blk1 edge_field=blocks depth=1 (deterministic BFS order)", first)
	}
	path, ok := first["path"].([]any)
	if !ok || len(path) != 2 || path[0] != "blk1" || path[1] != "target" {
		t.Errorf("impact_closure[0].path = %v, want [blk1 target]", first["path"])
	}
}

// TestGraphHandler_Query_Impact_EdgeKindsFilter checks that edge_kinds
// restricts the transitive walk and is echoed back in the response.
func TestGraphHandler_Query_Impact_EdgeKindsFilter(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "query",
		"catalog_path": impactFixturePath,
		"mode":         "impact",
		"target":       "target",
		"transitive":   true,
		"edge_kinds":   []any{"depends_on"},
	})
	if err != nil {
		t.Fatalf("GraphHandler(query impact transitive edge_kinds): %v", err)
	}

	closure, ok := res.Data["impact_closure"].([]any)
	if !ok {
		t.Fatalf("expected impact_closure list, got %v", res.Data["impact_closure"])
	}
	if len(closure) != 5 {
		t.Fatalf("expected 5 impacted nodes when filtered to depends_on (blk1 excluded), got %d: %+v", len(closure), closure)
	}
	for _, row := range closure {
		m, _ := row.(map[string]any)
		if m["node"] == "blk1" {
			t.Fatalf("blk1 (blocks-only referencer) unexpectedly present when filtered to depends_on: %+v", closure)
		}
	}

	edgeKinds, ok := res.Data["edge_kinds"].([]any)
	if !ok || len(edgeKinds) != 1 || edgeKinds[0] != "depends_on" {
		t.Errorf("expected edge_kinds echoed back as [depends_on], got %v", res.Data["edge_kinds"])
	}
}

// TestGraphHandler_Query_Impact_EdgeKindsRequiresTransitive checks the
// least-surprise guard: edge_kinds is meaningless without transitive:true,
// so it is rejected rather than silently ignored.
func TestGraphHandler_Query_Impact_EdgeKindsRequiresTransitive(t *testing.T) {
	_, err := GraphHandler(context.Background(), map[string]any{
		"op":           "query",
		"catalog_path": impactFixturePath,
		"mode":         "impact",
		"target":       "target",
		"edge_kinds":   []any{"depends_on"},
	})
	if err == nil {
		t.Fatal("expected an error when edge_kinds is set without transitive:true")
	}
}

// TestGraphHandler_Query_Impact_UnknownEdgeKind checks edge_kinds entries
// are validated against the registry's declared edge vocabulary, the same
// way host.graph.neighbors' edges filter already is.
func TestGraphHandler_Query_Impact_UnknownEdgeKind(t *testing.T) {
	_, err := GraphHandler(context.Background(), map[string]any{
		"op":           "query",
		"catalog_path": impactFixturePath,
		"mode":         "impact",
		"target":       "target",
		"transitive":   true,
		"edge_kinds":   []any{"not-a-real-edge"},
	})
	if err == nil {
		t.Fatal("expected an error for an unknown edge_kinds entry")
	}
}

// recordingCatalogStore wraps the file-backed store, recording the ref it
// was resolved for — the seam-injection proof for graphCatalogStoreResolver.
type recordingCatalogStore struct {
	objectgraph.CatalogStore
	loads *int
}

func (s recordingCatalogStore) Load(ctx context.Context) (*objectgraph.Catalog, objectgraph.CatalogRev, error) {
	*s.loads++
	return s.CatalogStore.Load(ctx)
}

// TestGraphHandler_Load_UsesInjectedCatalogStore proves loadCatalogArg's
// construction seam: swapping graphCatalogStoreResolver routes every plain
// (non-overlay) read through the injected CatalogStore, with no RPC/tool
// schema change — the hook a later phase uses to map non-file refs to other
// stores.
func TestGraphHandler_Load_UsesInjectedCatalogStore(t *testing.T) {
	loads := 0
	var gotRef string
	orig := graphCatalogStoreResolver
	graphCatalogStoreResolver = func(ref string) objectgraph.CatalogStore {
		gotRef = ref
		return recordingCatalogStore{CatalogStore: objectgraph.NewFileCatalogStore(ref), loads: &loads}
	}
	t.Cleanup(func() { graphCatalogStoreResolver = orig })

	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "load",
		"catalog_path": seedCatalogPath,
	})
	if err != nil {
		t.Fatalf("GraphHandler(load): %v", err)
	}
	if gotRef != seedCatalogPath {
		t.Errorf("resolver saw ref %q, want %q", gotRef, seedCatalogPath)
	}
	if loads != 1 {
		t.Errorf("injected store Load called %d times, want 1", loads)
	}
	if _, ok := res.Data["node_count"]; !ok {
		t.Error("expected the load op's normal payload through the injected store")
	}
}
