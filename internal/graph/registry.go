package graph

import (
	"fmt"
	"sort"
	"strings"
)

// Cardinality is the arity of an edge field.
type Cardinality string

const (
	CardinalityOne  Cardinality = "one"
	CardinalityMany Cardinality = "many"
)

// EdgeStorage says where an edge field's data actually lives on a Node.
type EdgeStorage string

const (
	// StorageEdges is the default: the edge lives at Node.Edges[field].
	StorageEdges EdgeStorage = ""
	// StorageTopLevel means the edge is read from Node.Fields[field]
	// instead — how `change` keeps `depends_on` as a top-level field
	// matching the shipped change-node schema, with no edges.depends_on
	// migration required.
	StorageTopLevel EdgeStorage = "top_level"
)

// EdgeFieldDecl declares one typed edge slot a type contributes.
type EdgeFieldDecl struct {
	ID          EdgeField
	TargetType  string
	Cardinality Cardinality
	Storage     EdgeStorage
	// Acyclic marks an edge field whose targets must not form a cycle
	// (e.g. change.depends_on) — checked by W1.1's catalog lint.
	Acyclic bool
	// Renders marks an edge field that public site codegen (G3) actually
	// walks to build rendered output — e.g. a site-page's `presents`. Only
	// Renders edges are checked for the internal-node-reachable-from-a-
	// public-edge leak; most edges (implemented_by, verified_by,
	// assigned_to, proposed_by, ...) are traceability cross-references a
	// generator never recurses into, so an internal target there is not a
	// leak. W3 (the site conversion) is what decides which edges are
	// Renders when it wires the real generator.
	Renders bool
	// NestsUnder marks an edge field whose source node is a sub-item of its
	// target, not a peer — e.g. persona.persona_of (a persona profiles an
	// actor) or proposal.child_of (a proposal slice decomposes an epic). A
	// generic UI list projection (unlike a graph canvas, where the edge
	// itself already draws the relationship) can use this to nest a node
	// under its target instead of listing it as a flat peer, without a
	// hand-maintained kind->edge table that silently drifts from the type
	// registry (see runstatus's CatalogPanel.vue, W6.2 follow-up).
	NestsUnder bool
}

// TypeDef is one type-registry entry: a node type's schema pin, optional
// parent (Extends), required envelope fields, and edge field declarations.
type TypeDef struct {
	ID             string
	Schema         SchemaPin
	Extends        string
	Summary        string
	RequiredFields []string
	EdgeFields     []EdgeFieldDecl

	// DeprecatedParentAlias is set when the type def was authored with the
	// pre-decision `derives_from:` field instead of the canonical
	// `extends:` (Shared decision 2). The loader accepts it as an alias for
	// Extends and records a warning; new type defs should use `extends:`.
	DeprecatedParentAlias bool

	// Artifact declares what a materialized instance of this type is
	// (schema/format/presentation). Nil when the type does not declare
	// itself an artifact. See node-artifact-materialization plan (POG
	// .context/node-artifact-materialization-plan.md).
	Artifact *ArtifactDecl

	// Materialize declares how to produce this type's artifact: the bound
	// story, the edge kinds pulled into the materialization context, the
	// params accepted at invocation, and the node fields that must be set
	// (gates) before invocation is offered. Nil when Artifact is nil or the
	// type declares artifact: without a materialize: binding (contract-only,
	// no invocation yet — see plan slice 1).
	Materialize *MaterializeDecl
}

// ArtifactDecl is a type's `artifact:` declaration.
type ArtifactDecl struct {
	Schema       SchemaPin
	Format       string
	Presentation string
}

// CanonicalArtifactKind returns the one persisted and wire-visible kind for an
// artifact declaration. Presentation is the registry's semantic artifact kind
// (for example, "document"), whereas Format describes its encoding (for
// example, "markdown"). Older declarations that predate presentation retain a
// truthful generic kind instead of letting callers invent incompatible aliases.
func CanonicalArtifactKind(artifact *ArtifactDecl) string {
	if artifact == nil {
		return ""
	}
	if kind := strings.TrimSpace(artifact.Presentation); kind != "" {
		return kind
	}
	return "artifact"
}

// MaterializeDecl is a type's `materialize:` binding.
type MaterializeDecl struct {
	// Story is a story root path (e.g. "stories/materialize-work-item"),
	// relative to the repo root the catalog lives in — not to the catalog
	// file itself. Story is the legacy materializer identity and is mutually
	// exclusive with ApplicationID/Phases.
	Story string
	// ApplicationID selects one exact application from the daemon's registered
	// story catalog. It is never resolved as a filesystem path.
	ApplicationID string
	// Phases are typed application operations. Each phase owns its artifact
	// outputs so handles and receipts cannot be attributed ambiguously.
	Phases []MaterializePhaseDecl
	// ContextEdges are edge field ids followed (recursively, through edges
	// of the same kinds) to build the node's materialization context.
	ContextEdges []EdgeField
	// IncomingContextEdges are declared targeting edge fields whose sources
	// belong in materialization context. They are deliberately opt-in: an app
	// can include requirements that apply_to it without every inbound graph
	// reference becoming materialization context.
	IncomingContextEdges []EdgeField
	// Params are invocation-time parameters (id/type/default/values),
	// supplied before or at invocation.
	Params []MaterializeParamDecl
	// Gates names node fields that must be non-empty before the materialize
	// intent is offered.
	Gates []string
	// Checks are deterministic Starlark gate assertions run by the
	// materialize driver AFTER the bound story's rooms complete — the
	// machine-checkable counterpart to a node's prose gate: field. A false
	// (or unresolvable) check fails the materialize job. Unlike Gates,
	// which only require field presence before start, a check evaluates
	// whether the gate is actually satisfied.
	Checks []MaterializeCheckDecl
}

// MaterializePhaseDecl is one typed application operation in a materializer.
// Exactly one of Handler or Action is set. ArtifactOutputs are top-level
// fields in the canonical application output whose values must be opaque
// handles. Inputs are intentionally not authorable: the daemon supplies the
// bounded catalog reference, node identity, and context digest.
type MaterializePhaseDecl struct {
	ID              string
	Handler         string
	Action          string
	ArtifactOutputs []string
}

const (
	maxMaterializePhases          = 32
	maxMaterializeOutputsPerPhase = 16
)

// MaterializeCheckDecl is one entry of a materialize: declaration's checks
// list. Exactly one of Script (type-provided, reusable across every node of
// the type) or ScriptField (the node field naming its own .star script) is
// set — that is how a reusable type either fixes the assertion or lets each
// node supply one. Inputs are the literal ctx.inputs for the script;
// InputsField names a node field whose map value is merged over Inputs, so
// nodes parameterize a shared assertion. Capabilities is the starlark
// sandbox grant (internal/host/starlark ParseCapabilities shape).
type MaterializeCheckDecl struct {
	ID           string
	Script       string
	ScriptField  string
	Inputs       map[string]any
	InputsField  string
	Capabilities map[string]any
}

// MaterializeParamDecl is one entry of a materialize: declaration's params list.
type MaterializeParamDecl struct {
	ID          string
	Type        string
	Default     any
	Values      []string
	Required    bool
	SourceField string
	SourceEdge  EdgeField
}

// EffectiveType is a TypeDef with its whole extends ancestry resolved: the
// union of required fields, and edge fields inherited by name. A subtype may
// add edges; redeclaring an inherited edge field incompatibly is a
// registration error (v1 does not support covariant edge overrides).
type EffectiveType struct {
	TypeDef
	// Ancestry is ordered root-to-self, inclusive of the type's own id.
	Ancestry []string
}

// Registry resolves extends chains into EffectiveTypes — GTS's
// schema-per-type-with-validation-at-registration discipline (Shared
// decision 2), without GTS's dotted-id grammar or Rust crates.
type Registry struct {
	defs      map[string]TypeDef
	effective map[string]EffectiveType
}

// NewRegistry returns an empty type registry.
func NewRegistry() *Registry {
	return &Registry{defs: map[string]TypeDef{}, effective: map[string]EffectiveType{}}
}

// Register adds a type definition. Types may be registered in any order —
// call Resolve once every type in the catalog has been registered.
func (r *Registry) Register(def TypeDef) error {
	if def.ID == "" {
		return fmt.Errorf("type registry: type with empty id")
	}
	if !IsKebabID(def.ID) {
		return fmt.Errorf("type registry: type id %q is not kebab-case", def.ID)
	}
	if _, exists := r.defs[def.ID]; exists {
		return fmt.Errorf("type registry: duplicate type id %q", def.ID)
	}
	if err := validateMaterializeDecl(def.ID, def.Materialize); err != nil {
		return err
	}
	r.defs[def.ID] = def
	return nil
}

func validateMaterializeDecl(typeID string, decl *MaterializeDecl) error {
	if decl == nil {
		return nil
	}
	legacy := strings.TrimSpace(decl.Story) != ""
	typed := strings.TrimSpace(decl.ApplicationID) != "" || len(decl.Phases) > 0
	if legacy == typed {
		return fmt.Errorf(
			"type registry: type %q materialize must declare exactly one of legacy story or typed application_id with phases",
			typeID,
		)
	}
	if legacy {
		return nil
	}
	if strings.TrimSpace(decl.ApplicationID) == "" || len(decl.Phases) == 0 {
		return fmt.Errorf(
			"type registry: type %q typed materialize requires application_id and phases",
			typeID,
		)
	}
	if len(decl.Params) > 0 {
		return fmt.Errorf("type registry: type %q typed materialize cannot declare legacy params", typeID)
	}
	if len(decl.Checks) > 0 {
		return fmt.Errorf("type registry: type %q typed materialize cannot declare legacy script checks; use a typed phase", typeID)
	}
	if len(decl.Phases) > maxMaterializePhases {
		return fmt.Errorf("type registry: type %q typed materialize exceeds %d phases", typeID, maxMaterializePhases)
	}
	phaseIDs := map[string]struct{}{}
	for i, phase := range decl.Phases {
		path := fmt.Sprintf("type registry: type %q materialize phase %d", typeID, i)
		if !IsKebabID(phase.ID) {
			return fmt.Errorf("%s: id %q is not kebab-case", path, phase.ID)
		}
		if _, exists := phaseIDs[phase.ID]; exists {
			return fmt.Errorf("%s: duplicate id %q", path, phase.ID)
		}
		phaseIDs[phase.ID] = struct{}{}
		if (strings.TrimSpace(phase.Handler) == "") == (strings.TrimSpace(phase.Action) == "") {
			return fmt.Errorf("%s %q: exactly one of handler or action is required", path, phase.ID)
		}
		if !validMaterializeMemberID(firstNonEmpty(phase.Handler, phase.Action)) {
			return fmt.Errorf("%s %q: operation id is invalid", path, phase.ID)
		}
		if len(phase.ArtifactOutputs) == 0 {
			return fmt.Errorf("%s %q: artifact_outputs is required", path, phase.ID)
		}
		if len(phase.ArtifactOutputs) > maxMaterializeOutputsPerPhase {
			return fmt.Errorf("%s %q: artifact_outputs exceeds %d entries", path, phase.ID, maxMaterializeOutputsPerPhase)
		}
		outputs := map[string]struct{}{}
		for _, output := range phase.ArtifactOutputs {
			if !validMaterializeOutputField(output) {
				return fmt.Errorf("%s %q: artifact output %q is invalid", path, phase.ID, output)
			}
			if _, exists := outputs[output]; exists {
				return fmt.Errorf("%s %q: duplicate artifact output %q", path, phase.ID, output)
			}
			outputs[output] = struct{}{}
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func validMaterializeMemberID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for i, r := range value {
		if i == 0 && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validMaterializeOutputField(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// Resolve validates the whole registry (missing/cyclic parents, incompatible
// edge-field redeclaration) and computes every type's EffectiveType.
func (r *Registry) Resolve() error {
	r.effective = map[string]EffectiveType{}
	ids := make([]string, 0, len(r.defs))
	for id := range r.defs {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic error ordering
	for _, id := range ids {
		if _, err := r.resolve(id, nil); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) resolve(id string, chain []string) (EffectiveType, error) {
	if eff, ok := r.effective[id]; ok {
		return eff, nil
	}
	for _, seen := range chain {
		if seen == id {
			return EffectiveType{}, fmt.Errorf("type registry: cyclic extends chain: %s -> %s", strings.Join(chain, " -> "), id)
		}
	}
	def, ok := r.defs[id]
	if !ok {
		return EffectiveType{}, fmt.Errorf("type registry: unknown type %q referenced by extends chain %s", id, strings.Join(append(append([]string{}, chain...), id), " -> "))
	}
	chain = append(append([]string{}, chain...), id)

	if def.Extends == "" {
		eff := EffectiveType{TypeDef: def, Ancestry: []string{id}}
		r.effective[id] = eff
		return eff, nil
	}

	parent, err := r.resolve(def.Extends, chain)
	if err != nil {
		return EffectiveType{}, err
	}

	required := append([]string{}, parent.RequiredFields...)
	required = appendUnique(required, def.RequiredFields...)

	edgeByID := map[EdgeField]EdgeFieldDecl{}
	for _, e := range parent.EdgeFields {
		edgeByID[e.ID] = e
	}
	for _, e := range def.EdgeFields {
		if existing, ok := edgeByID[e.ID]; ok && existing != e {
			return EffectiveType{}, fmt.Errorf("type registry: %q redeclares inherited edge field %q incompatibly (inherited from ancestor of %q)", id, e.ID, def.Extends)
		}
		edgeByID[e.ID] = e
	}
	edgeIDs := make([]string, 0, len(edgeByID))
	for fieldID := range edgeByID {
		edgeIDs = append(edgeIDs, string(fieldID))
	}
	sort.Strings(edgeIDs)
	edges := make([]EdgeFieldDecl, 0, len(edgeIDs))
	for _, fieldID := range edgeIDs {
		edges = append(edges, edgeByID[EdgeField(fieldID)])
	}

	eff := EffectiveType{
		TypeDef: TypeDef{
			ID:             id,
			Schema:         def.Schema,
			Extends:        def.Extends,
			Summary:        def.Summary,
			RequiredFields: required,
			EdgeFields:     edges,
			Artifact:       def.Artifact,
			Materialize:    def.Materialize,
		},
		Ancestry: append(append([]string{}, parent.Ancestry...), id),
	}
	r.effective[id] = eff
	return eff, nil
}

// Effective returns the fully-resolved type, or false if unknown / Resolve
// has not been called successfully.
func (r *Registry) Effective(id string) (EffectiveType, bool) {
	eff, ok := r.effective[id]
	return eff, ok
}

// HasTypeDef reports whether a type ID exists in the registry.
func (r *Registry) HasTypeDef(id string) bool {
	_, exists := r.defs[id]
	return exists
}

// TypeDef returns the TypeDef for id, or false if not registered.
func (r *Registry) TypeDef(id string) (TypeDef, bool) {
	def, ok := r.defs[id]
	return def, ok
}

// All returns every registered TypeDef, sorted by ID for a deterministic
// wire order — used by host.graph.project's registry passthrough (POG
// use-case-loop-plan.md §4 row C1) so kit-served clients see the same
// artifact:/materialize: declarations the dev-only splice route already
// forwarded from the raw catalog YAML.
func (r *Registry) All() []TypeDef {
	out := make([]TypeDef, 0, len(r.defs))
	for _, def := range r.defs {
		out = append(out, def)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// IsA reports whether typeID equals ancestorID or extends it (directly or
// transitively) — the assignability check an edge target uses (an edge
// declared with target_type "requirement" accepts any type extending
// "requirement", e.g. an ISO pack's "iso9001-clause").
func (r *Registry) IsA(typeID, ancestorID string) bool {
	eff, ok := r.effective[typeID]
	if !ok {
		return false
	}
	for _, a := range eff.Ancestry {
		if a == ancestorID {
			return true
		}
	}
	return false
}

func appendUnique(base []string, add ...string) []string {
	seen := make(map[string]bool, len(base))
	for _, b := range base {
		seen[b] = true
	}
	for _, a := range add {
		if !seen[a] {
			base = append(base, a)
			seen[a] = true
		}
	}
	return base
}
