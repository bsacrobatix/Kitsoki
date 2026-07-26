package graph

// catalogdata.go is the storage-neutral face of a catalog for non-file
// CatalogStore implementations (store.go): CatalogData carries the exact
// wire shapes the YAML loader consumes (fileTypeDef / fileNode mappings),
// BuildCatalogFromData assembles and validates a *Catalog from them through
// the same Register/Resolve/buildNode pipeline LoadCatalog uses, ExportData
// is the inverse, and ApplyOperationsToData applies changeset Operations to
// the wire data in memory with the same semantics applyOperations (apply.go)
// has against YAML documents. Nothing here touches the filesystem, and
// nothing in the YAML load/commit path calls into this file — it is additive
// surface for stores whose durability is not files.

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ErrCASConflict is the exported face of the optimistic-concurrency conflict
// sentinel (guards.go's errCASConflict) for CatalogStore implementations
// OUTSIDE this package: wrap it (fmt.Errorf("...: %w", graph.ErrCASConflict))
// from Commit when the base revision went stale, and IsCASConflict reports
// true for callers driving the bounded-retry loop. The file-backed pipeline
// keeps returning the unexported sentinel directly; the two are the same
// error value.
var ErrCASConflict = errCASConflict

// DeltaErrorIssues is the exported face of the lint-diff gate (guards.go's
// newErrorIssues) for CatalogStore implementations outside this package:
// baseline is ErrorIssues(Lint(pre-write catalog)), candidate is
// ErrorIssues(Lint(post-operation catalog)); only genuinely NEW
// error-severity issues come back — pre-existing catalog dirt never blocks a
// write, exactly the policy commitScratchOperations enforces on the file path.
func DeltaErrorIssues(baseline, candidate []LintIssue) []LintIssue {
	return newErrorIssues(baseline, candidate)
}

// CatalogData is a catalog serialized to storage-neutral wire mappings: the
// same generic shapes the YAML files decode to (type_registry entries and
// node entries with type-specific fields inline), so a non-file store can
// persist them however it likes and still rebuild a Catalog through the
// loader's own validation.
type CatalogData struct {
	// Schema is the catalog-level schema pin (Catalog.Schema).
	Schema SchemaPin
	// Types are type-registry entries in the fileTypeDef wire shape
	// ({id, schema, extends, required_fields, edge_fields, artifact,
	// materialize, ...}).
	Types []map[string]any
	// Nodes are node entries in the fileNode wire shape: envelope keys
	// (schema/id/title/status/visibility/sources/edges) plus every
	// type-specific field inline — which is also where a storage:top_level
	// edge (e.g. change.depends_on) lives, matching the YAML contract.
	Nodes []map[string]any

	LintHints       *LintHints
	WritePolicy     *WritePolicy
	FeedbackRouting *FeedbackRouting
}

// BuildCatalogFromData assembles and validates a *Catalog from data, running
// the exact same registry Register/Resolve and per-node buildNode validation
// the YAML loader runs — a data-built catalog rejects everything a YAML load
// would reject (unknown types, cyclic extends, duplicate ids, cardinality
// violations, ...) with the same errors. ref becomes RootPath (a synthetic
// locator like "pg:pog", never stat'd here); NodeFile and ContentDigest stay
// empty — a data-built catalog has no backing files, and must never be
// handed to the file-commit pipeline (commitScratchOperations), whose
// scratch-copy/CAS machinery is meaningless without them.
func BuildCatalogFromData(ref string, data CatalogData) (*Catalog, error) {
	cat := &Catalog{
		Schema:          data.Schema,
		Registry:        NewRegistry(),
		Nodes:           map[NodeID]*Node{},
		NodeFile:        map[NodeID]string{},
		RootPath:        ref,
		LintHints:       data.LintHints,
		WritePolicy:     data.WritePolicy,
		FeedbackRouting: data.FeedbackRouting,
	}
	for _, tw := range data.Types {
		var ft fileTypeDef
		if err := reshapeWire(tw, &ft); err != nil {
			return nil, fmt.Errorf("graph: type entry: %w", err)
		}
		def, warning, err := ft.toTypeDef()
		if err != nil {
			return nil, err
		}
		if warning != "" {
			cat.Warnings = append(cat.Warnings, warning)
		}
		if err := cat.Registry.Register(def); err != nil {
			return nil, err
		}
	}
	if err := cat.Registry.Resolve(); err != nil {
		return nil, err
	}
	for _, nw := range data.Nodes {
		var fn fileNode
		if err := reshapeWire(nw, &fn); err != nil {
			return nil, fmt.Errorf("graph: node entry: %w", err)
		}
		node, err := buildNode(fn, cat.Registry)
		if err != nil {
			return nil, err
		}
		if _, exists := cat.Nodes[node.ID]; exists {
			return nil, fmt.Errorf("graph: duplicate node id %q", node.ID)
		}
		cat.Nodes[node.ID] = node
	}
	return cat, nil
}

// ResolveWireTypes builds and resolves a Registry from type-registry wire
// mappings alone — what a store needs BEFORE it can assemble node wire maps
// (it must know each edge field's declared storage to put a top_level edge
// back inline vs. under "edges"). Returns any deprecated-alias warnings the
// loader would have recorded.
func ResolveWireTypes(types []map[string]any) (*Registry, []string, error) {
	reg := NewRegistry()
	var warnings []string
	for _, tw := range types {
		var ft fileTypeDef
		if err := reshapeWire(tw, &ft); err != nil {
			return nil, nil, fmt.Errorf("graph: type entry: %w", err)
		}
		def, warning, err := ft.toTypeDef()
		if err != nil {
			return nil, nil, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if err := reg.Register(def); err != nil {
			return nil, nil, err
		}
	}
	if err := reg.Resolve(); err != nil {
		return nil, nil, err
	}
	return reg, warnings, nil
}

// ExportData serializes c back into wire mappings — the inverse of
// BuildCatalogFromData, faithful to the on-disk contract: edges-map edges go
// under "edges", storage:top_level edge values stay inline in the node
// mapping (they live in Node.Fields), and every type-specific field rides
// inline. Nodes and types come back in deterministic (id-sorted) order. The
// returned maps are deep copies; mutating them never aliases the catalog.
func (c *Catalog) ExportData() (CatalogData, error) {
	data := CatalogData{
		Schema:          c.Schema,
		LintHints:       c.LintHints,
		WritePolicy:     c.WritePolicy,
		FeedbackRouting: c.FeedbackRouting,
	}
	for _, def := range c.Registry.All() {
		tw, err := TypeDefToWire(def)
		if err != nil {
			return CatalogData{}, err
		}
		data.Types = append(data.Types, tw)
	}
	for _, id := range c.SortedNodeIDs() {
		data.Nodes = append(data.Nodes, nodeToWire(c.Nodes[id]))
	}
	return data, nil
}

// TypeDefToWire renders one TypeDef in the type-registry wire shape the
// loader's fileTypeDef decodes — including artifact:/materialize: blocks — by
// round-tripping through the file struct itself, so the wire form can never
// drift from what toTypeDef accepts. A DeprecatedParentAlias def serializes
// back as `derives_from:` so the load-time warning survives a store round
// trip.
func TypeDefToWire(def TypeDef) (map[string]any, error) {
	ft := fileTypeDef{
		ID:             def.ID,
		Schema:         string(def.Schema),
		Summary:        def.Summary,
		RequiredFields: def.RequiredFields,
	}
	if def.Extends != "" {
		if def.DeprecatedParentAlias {
			alias := def.Extends
			ft.DerivesFrom = &alias
		} else {
			ft.Extends = def.Extends
		}
	}
	for _, e := range def.EdgeFields {
		ft.EdgeFields = append(ft.EdgeFields, fileEdgeField{
			ID:          string(e.ID),
			TargetType:  e.TargetType,
			Cardinality: string(e.Cardinality),
			Storage:     string(e.Storage),
			Acyclic:     e.Acyclic,
			Renders:     e.Renders,
			NestsUnder:  e.NestsUnder,
		})
	}
	if def.Artifact != nil {
		ft.Artifact = &fileArtifactDecl{
			Schema:       string(def.Artifact.Schema),
			Format:       def.Artifact.Format,
			Presentation: def.Artifact.Presentation,
		}
	}
	if def.Materialize != nil {
		md := &fileMaterializeDecl{
			Story: def.Materialize.Story,
			Gates: def.Materialize.Gates,
		}
		for _, e := range def.Materialize.ContextEdges {
			md.ContextEdges = append(md.ContextEdges, string(e))
		}
		for _, e := range def.Materialize.IncomingContextEdges {
			md.IncomingContextEdges = append(md.IncomingContextEdges, string(e))
		}
		for _, p := range def.Materialize.Params {
			md.Params = append(md.Params, fileMaterializeParam{
				ID:          p.ID,
				Type:        p.Type,
				Default:     p.Default,
				Values:      p.Values,
				Required:    p.Required,
				SourceField: p.SourceField,
				SourceEdge:  string(p.SourceEdge),
			})
		}
		for _, c := range def.Materialize.Checks {
			md.Checks = append(md.Checks, fileMaterializeCheck{
				ID:           c.ID,
				Script:       c.Script,
				ScriptField:  c.ScriptField,
				Inputs:       c.Inputs,
				InputsField:  c.InputsField,
				Capabilities: c.Capabilities,
			})
		}
		ft.Materialize = md
	}
	var wire map[string]any
	if err := reshapeWire(ft, &wire); err != nil {
		return nil, fmt.Errorf("graph: type %q to wire: %w", def.ID, err)
	}
	return wire, nil
}

// nodeToWire renders a live Node in the fileNode wire shape: envelope keys,
// "edges" from the raw Edges map only (a top_level edge stays inline via
// Fields, exactly as authored), and every Fields entry inline. Deep-copied.
func nodeToWire(node *Node) map[string]any {
	m := map[string]any{
		"schema":     string(node.Schema),
		"id":         string(node.ID),
		"title":      node.Title,
		"status":     node.Status,
		"visibility": string(node.Visibility),
	}
	if len(node.Sources) > 0 {
		srcs := make([]any, len(node.Sources))
		for i, s := range node.Sources {
			srcs[i] = string(s)
		}
		m["sources"] = srcs
	}
	if len(node.Edges) > 0 {
		edges := map[string]any{}
		for field, targets := range node.Edges {
			ts := make([]any, len(targets))
			for i, t := range targets {
				ts[i] = string(t)
			}
			edges[string(field)] = ts
		}
		m["edges"] = edges
	}
	for k, v := range node.Fields {
		m[k] = deepCopyWire(v)
	}
	return m
}

// ApplyOperationsToData applies ops to a deep copy of data with the same
// semantics applyOperations (apply.go) has against the YAML documents —
// added appends, modified follows the FieldChange path convention, removed
// deletes, renamed rewrites the id plus every matching string scalar across
// nodes AND type entries (a single-file catalog's rename walk covers its
// type_registry section too), retyped rewrites the schema pin's middle
// segment, and the registry_type_* ops edit the Types list. The input data
// is never mutated. Failures come back as reject reasons (first failure,
// matching the file path's error-return), never as catalog corruption; a
// nil reasons slice means every op landed on the returned copy — which the
// caller must still re-validate via BuildCatalogFromData + the lint-delta
// gate, exactly as the file path re-loads and re-lints its scratch tree.
func ApplyOperationsToData(data CatalogData, ops []Operation) (CatalogData, []string) {
	out := CatalogData{
		Schema:          data.Schema,
		LintHints:       data.LintHints,
		WritePolicy:     data.WritePolicy,
		FeedbackRouting: data.FeedbackRouting,
	}
	for _, tw := range data.Types {
		out.Types = append(out.Types, deepCopyWire(tw).(map[string]any))
	}
	for _, nw := range data.Nodes {
		out.Nodes = append(out.Nodes, deepCopyWire(nw).(map[string]any))
	}

	for _, op := range ops {
		switch op.Kind {
		case OpAdded:
			out.Nodes = append(out.Nodes, deepCopyWire(op.After).(map[string]any))

		case OpModified:
			nm, _, found := findWireByID(out.Nodes, string(op.Node))
			if !found {
				return CatalogData{}, []string{fmt.Sprintf("modified op: node %q not found", op.Node)}
			}
			for _, ch := range op.Changes {
				if err := setWireField(nm, ch.Path, ch.After); err != nil {
					return CatalogData{}, []string{fmt.Sprintf("modified op for %q: %v", op.Node, err)}
				}
			}

		case OpRemoved:
			_, idx, found := findWireByID(out.Nodes, string(op.Node))
			if !found {
				return CatalogData{}, []string{fmt.Sprintf("removed op: node %q not found", op.Node)}
			}
			out.Nodes = append(out.Nodes[:idx], out.Nodes[idx+1:]...)

		case OpRenamed:
			nm, _, found := findWireByID(out.Nodes, string(op.From))
			if !found {
				return CatalogData{}, []string{fmt.Sprintf("renamed op: node %q not found", op.From)}
			}
			nm["id"] = string(op.To)
			// Coordinate-rewrite every reference to the renamed id — edges,
			// top_level-storage fields, and any other string scalar — across
			// every node and type entry, mirroring renameScalarRefs' walk.
			for i, m := range out.Nodes {
				out.Nodes[i] = renameWireRefs(m, string(op.From), string(op.To)).(map[string]any)
			}
			for i, m := range out.Types {
				out.Types[i] = renameWireRefs(m, string(op.From), string(op.To)).(map[string]any)
			}

		case OpRetyped:
			nm, _, found := findWireByID(out.Nodes, string(op.Node))
			if !found {
				return CatalogData{}, []string{fmt.Sprintf("retyped op: node %q not found", op.Node)}
			}
			schemaStr, _ := nm["schema"].(string)
			pack, _, version, err := ParseSchemaPin(SchemaPin(schemaStr))
			if err != nil {
				return CatalogData{}, []string{fmt.Sprintf("retyped op for %q: %v", op.Node, err)}
			}
			nm["schema"] = fmt.Sprintf("%s/%s/%s", pack, op.ToType, version)

		case OpRegistryTypeAdded:
			out.Types = append(out.Types, deepCopyWire(op.After).(map[string]any))

		case OpRegistryTypeModified:
			tm, _, found := findWireByID(out.Types, string(op.Node))
			if !found {
				return CatalogData{}, []string{fmt.Sprintf("registry_type_modified op: type %q not found", op.Node)}
			}
			for _, ch := range op.Changes {
				if err := setWireField(tm, ch.Path, ch.After); err != nil {
					return CatalogData{}, []string{fmt.Sprintf("registry_type_modified op for %q: %v", op.Node, err)}
				}
			}

		default:
			return CatalogData{}, []string{fmt.Sprintf("unknown op kind %q", op.Kind)}
		}
	}
	return out, nil
}

// setWireField applies one FieldChange's After value to a wire mapping, per
// the same path convention setNodeField (apply.go) implements on yaml.Node
// mappings: ["edges", f] under the "edges" submap, ["fields", k] inline at
// the top level (that IS where a type-specific field lives on the wire), any
// single-segment path directly.
func setWireField(m map[string]any, path []string, value any) error {
	if len(path) == 0 {
		return fmt.Errorf("empty field change path")
	}
	switch path[0] {
	case "edges":
		if len(path) != 2 {
			return fmt.Errorf("edges path must be [\"edges\", \"<field>\"], got %v", path)
		}
		edges, _ := m["edges"].(map[string]any)
		if edges == nil {
			edges = map[string]any{}
			m["edges"] = edges
		}
		edges[path[1]] = deepCopyWire(value)
		return nil
	case "fields":
		if len(path) != 2 {
			return fmt.Errorf("fields path must be [\"fields\", \"<key>\"], got %v", path)
		}
		m[path[1]] = deepCopyWire(value)
		return nil
	default:
		if len(path) != 1 {
			return fmt.Errorf("unsupported nested path %v", path)
		}
		m[path[0]] = deepCopyWire(value)
		return nil
	}
}

// findWireByID locates the wire mapping whose "id" equals id, mirroring
// findNodeMapping/findTypeMapping.
func findWireByID(list []map[string]any, id string) (map[string]any, int, bool) {
	for i, m := range list {
		if v, _ := m["id"].(string); v == id {
			return m, i, true
		}
	}
	return nil, -1, false
}

// renameWireRefs is renameScalarRefs' (apply.go) wire-map counterpart:
// every string value — and every string map key — equal to from becomes to.
// Returns the (possibly rebuilt) value.
func renameWireRefs(v any, from, to string) any {
	switch val := v.(type) {
	case string:
		if val == from {
			return to
		}
		return val
	case map[string]any:
		next := make(map[string]any, len(val))
		for k, item := range val {
			if k == from {
				k = to
			}
			next[k] = renameWireRefs(item, from, to)
		}
		return next
	case []any:
		for i, item := range val {
			val[i] = renameWireRefs(item, from, to)
		}
		return val
	case []string:
		for i, s := range val {
			if s == from {
				val[i] = to
			}
		}
		return val
	default:
		return v
	}
}

// deepCopyWire deep-copies a wire value (maps, slices, scalars) so op
// application never aliases caller-owned data.
func deepCopyWire(v any) any {
	switch val := v.(type) {
	case map[string]any:
		next := make(map[string]any, len(val))
		for k, item := range val {
			next[k] = deepCopyWire(item)
		}
		return next
	case []any:
		next := make([]any, len(val))
		for i, item := range val {
			next[i] = deepCopyWire(item)
		}
		return next
	case []string:
		next := make([]string, len(val))
		copy(next, val)
		return next
	default:
		return v
	}
}

// reshapeWire converts between a wire mapping and one of the loader's file
// structs (fileNode/fileTypeDef, either direction) via a YAML round trip —
// the same decode path the on-disk files take, so wire data can never be
// interpreted differently from a file. src is marshaled and unmarshaled
// into dst (a pointer).
func reshapeWire(src, dst any) error {
	raw, err := yaml.Marshal(src)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(raw, dst)
}
