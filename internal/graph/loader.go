package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Catalog is a loaded, type-checked project object graph.
type Catalog struct {
	Schema   SchemaPin
	Registry *Registry
	Nodes    map[NodeID]*Node

	// RootPath is the path LoadCatalog was called with (a bundle dir or a
	// single catalog file).
	RootPath string
	// NodeFile maps each node id to the file it was loaded from — for a
	// single-file catalog every node maps to RootPath; for a bundle, to
	// whichever nodes/*.yaml file declared it. Apply (W2.0) uses this to
	// know which file to rewrite for a given node.
	NodeFile map[NodeID]string

	// Warnings collects non-fatal issues surfaced during load, e.g. a type
	// def using the deprecated `derives_from:` alias instead of `extends:`.
	Warnings []string
}

// SortedNodeIDs returns the catalog's node ids in deterministic order.
func (c *Catalog) SortedNodeIDs() []NodeID {
	ids := make([]NodeID, 0, len(c.Nodes))
	for id := range c.Nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// ParseSchemaPin splits a schema pin "<pack>/<type>/<version>" into its
// three segments.
func ParseSchemaPin(pin SchemaPin) (pack, typeID, version string, err error) {
	parts := strings.Split(string(pin), "/")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("schema pin %q must have shape <pack>/<type>/<version>", pin)
	}
	return parts[0], parts[1], parts[2], nil
}

// --- YAML file shapes -------------------------------------------------

type fileEdgeField struct {
	ID          string `yaml:"id"`
	TargetType  string `yaml:"target_type"`
	Cardinality string `yaml:"cardinality"`
	Storage     string `yaml:"storage"`
	Acyclic     bool   `yaml:"acyclic"`
	Renders     bool   `yaml:"renders"`
	NestsUnder  bool   `yaml:"nests_under"`
}

type fileTypeDef struct {
	ID             string          `yaml:"id"`
	Schema         string          `yaml:"schema"`
	Extends        string          `yaml:"extends"`
	DerivesFrom    *string         `yaml:"derives_from"`
	Summary        string          `yaml:"summary"`
	RequiredFields []string        `yaml:"required_fields"`
	EdgeFields     []fileEdgeField `yaml:"edge_fields"`
}

func (ft fileTypeDef) toTypeDef() (TypeDef, string, error) {
	def := TypeDef{
		ID:             ft.ID,
		Schema:         SchemaPin(ft.Schema),
		Summary:        ft.Summary,
		RequiredFields: ft.RequiredFields,
	}
	var warning string
	switch {
	case ft.Extends != "":
		def.Extends = ft.Extends
	case ft.DerivesFrom != nil && *ft.DerivesFrom != "":
		def.Extends = *ft.DerivesFrom
		def.DeprecatedParentAlias = true
		warning = fmt.Sprintf("type %q uses deprecated `derives_from:` (%q) — use `extends:` (Shared decision 2)", ft.ID, *ft.DerivesFrom)
	}
	for _, fe := range ft.EdgeFields {
		if fe.Cardinality != string(CardinalityOne) && fe.Cardinality != string(CardinalityMany) {
			return TypeDef{}, "", fmt.Errorf("type %q edge field %q: cardinality must be \"one\" or \"many\", got %q", ft.ID, fe.ID, fe.Cardinality)
		}
		def.EdgeFields = append(def.EdgeFields, EdgeFieldDecl{
			ID:          EdgeField(fe.ID),
			TargetType:  fe.TargetType,
			Cardinality: Cardinality(fe.Cardinality),
			Storage:     EdgeStorage(fe.Storage),
			Acyclic:     fe.Acyclic,
			Renders:     fe.Renders,
			NestsUnder:  fe.NestsUnder,
		})
	}
	return def, warning, nil
}

type fileNode struct {
	Schema     string              `yaml:"schema"`
	ID         string              `yaml:"id"`
	Title      string              `yaml:"title"`
	Status     string              `yaml:"status"`
	Visibility string              `yaml:"visibility"`
	Sources    []string            `yaml:"sources"`
	Edges      map[string][]string `yaml:"edges"`
	Extra      map[string]any      `yaml:",inline"`
}

type fileSingleCatalog struct {
	Schema       string          `yaml:"schema"`
	Catalog      map[string]any  `yaml:"catalog"`
	TypeRegistry []fileTypeDef   `yaml:"type_registry"`
	Nodes        []fileNode      `yaml:"nodes"`
}

// --- loading ------------------------------------------------------------

// LoadCatalog loads a project object graph catalog from path. If path is a
// directory, it is loaded as a bundle catalog (catalog.yaml +
// type_registry.yaml + sources.yaml + nodes/*.yaml); otherwise it is loaded
// as a single-file catalog (the seed-objects.yaml review fixture shape).
func LoadCatalog(path string) (*Catalog, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("graph: stat %s: %w", path, err)
	}
	if info.IsDir() {
		return loadBundle(path)
	}
	return loadSingleFile(path)
}

func loadSingleFile(path string) (*Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("graph: read %s: %w", path, err)
	}
	var file fileSingleCatalog
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("graph: parse %s: %w", path, err)
	}
	sources := make([]nodeSource, len(file.Nodes))
	for i, n := range file.Nodes {
		sources[i] = nodeSource{node: n, file: path}
	}
	return buildCatalog(path, SchemaPin(file.Schema), file.TypeRegistry, sources)
}

func loadBundle(dir string) (*Catalog, error) {
	var schema string
	if raw, err := os.ReadFile(filepath.Join(dir, "catalog.yaml")); err == nil {
		var cat struct {
			Schema string `yaml:"schema"`
		}
		if err := yaml.Unmarshal(raw, &cat); err != nil {
			return nil, fmt.Errorf("graph: parse %s/catalog.yaml: %w", dir, err)
		}
		schema = cat.Schema
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("graph: read %s/catalog.yaml: %w", dir, err)
	}

	var typeDefs []fileTypeDef
	if raw, err := os.ReadFile(filepath.Join(dir, "type_registry.yaml")); err == nil {
		if err := yaml.Unmarshal(raw, &typeDefs); err != nil {
			return nil, fmt.Errorf("graph: parse %s/type_registry.yaml: %w", dir, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("graph: read %s/type_registry.yaml: %w", dir, err)
	}

	nodeFiles, err := filepath.Glob(filepath.Join(dir, "nodes", "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("graph: glob %s/nodes/*.yaml: %w", dir, err)
	}
	sort.Strings(nodeFiles)

	var sources []nodeSource
	for _, nf := range nodeFiles {
		raw, err := os.ReadFile(nf)
		if err != nil {
			return nil, fmt.Errorf("graph: read %s: %w", nf, err)
		}
		var batch []fileNode
		if err := yaml.Unmarshal(raw, &batch); err != nil {
			return nil, fmt.Errorf("graph: parse %s: %w", nf, err)
		}
		for _, n := range batch {
			sources = append(sources, nodeSource{node: n, file: nf})
		}
	}

	return buildCatalog(dir, SchemaPin(schema), typeDefs, sources)
}

// nodeSource pairs a decoded node with the file it came from, so Apply
// (W2.0) knows which file to rewrite for a given node id.
type nodeSource struct {
	node fileNode
	file string
}

func buildCatalog(rootPath string, schema SchemaPin, typeDefs []fileTypeDef, sources []nodeSource) (*Catalog, error) {
	cat := &Catalog{
		Schema:   schema,
		Registry: NewRegistry(),
		Nodes:    map[NodeID]*Node{},
		NodeFile: map[NodeID]string{},
		RootPath: rootPath,
	}

	for _, ft := range typeDefs {
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

	for _, src := range sources {
		node, err := buildNode(src.node, cat.Registry)
		if err != nil {
			return nil, err
		}
		if _, exists := cat.Nodes[node.ID]; exists {
			return nil, fmt.Errorf("graph: duplicate node id %q", node.ID)
		}
		cat.Nodes[node.ID] = node
		cat.NodeFile[node.ID] = src.file
	}

	return cat, nil
}

func buildNode(fn fileNode, reg *Registry) (*Node, error) {
	if fn.ID == "" {
		return nil, fmt.Errorf("graph: node missing id (schema %q)", fn.Schema)
	}
	if !IsKebabID(fn.ID) {
		return nil, fmt.Errorf("graph: node %q is not a kebab-case id", fn.ID)
	}
	if fn.Schema == "" {
		return nil, fmt.Errorf("graph: node %q missing schema", fn.ID)
	}
	_, typeID, _, err := ParseSchemaPin(SchemaPin(fn.Schema))
	if err != nil {
		return nil, fmt.Errorf("graph: node %q: %w", fn.ID, err)
	}
	eff, ok := reg.Effective(typeID)
	if !ok {
		return nil, fmt.Errorf("graph: node %q has unknown type %q (schema %q)", fn.ID, typeID, fn.Schema)
	}
	if fn.Title == "" {
		return nil, fmt.Errorf("graph: node %q missing title", fn.ID)
	}
	if fn.Status == "" {
		return nil, fmt.Errorf("graph: node %q missing status", fn.ID)
	}
	if fn.Visibility == "" {
		return nil, fmt.Errorf("graph: node %q missing visibility", fn.ID)
	}
	vis := Visibility(fn.Visibility)
	if vis != VisibilityPublic && vis != VisibilityInternal {
		return nil, fmt.Errorf("graph: node %q has invalid visibility %q (must be public or internal)", fn.ID, fn.Visibility)
	}

	node := &Node{
		Schema:     SchemaPin(fn.Schema),
		ID:         NodeID(fn.ID),
		Title:      fn.Title,
		Status:     fn.Status,
		Visibility: vis,
		TypeID:     typeID,
		Fields:     fn.Extra,
	}
	for _, s := range fn.Sources {
		node.Sources = append(node.Sources, NodeID(s))
	}
	if len(fn.Edges) > 0 {
		node.Edges = map[EdgeField][]NodeID{}
		for field, targets := range fn.Edges {
			ids := make([]NodeID, 0, len(targets))
			for _, t := range targets {
				ids = append(ids, NodeID(t))
			}
			node.Edges[EdgeField(field)] = ids
		}
	}

	if err := checkEdgeCardinality(node, eff); err != nil {
		return nil, err
	}

	return node, nil
}

// checkEdgeCardinality enforces the LOCAL shape of a declared cardinality
// (a "one" edge may have at most one target). It does not check that edge
// targets exist or are visibility-safe — that is W1.1's catalog lint, which
// needs the whole catalog assembled first.
func checkEdgeCardinality(node *Node, eff EffectiveType) error {
	for _, decl := range eff.EdgeFields {
		targets := node.EdgeTargets(decl)
		if decl.Cardinality == CardinalityOne && len(targets) > 1 {
			return fmt.Errorf("graph: node %q edge %q has cardinality \"one\" but %d targets", node.ID, decl.ID, len(targets))
		}
	}
	return nil
}
