// Package componentpackage defines the portable, versioned UI component
// package contract used by story applications. Resolution remains owned by the
// existing kit subsystem; this package validates an already-resolved package
// and its kits.lock pin.
package componentpackage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	yaml "github.com/goccy/go-yaml"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"kitsoki/internal/kitlock"
	"kitsoki/internal/kitver"
)

const (
	SchemaV1 = "application-component-package/v1"
	FileName = "component-package.yaml"
)

var segmentRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type Manifest struct {
	Schema         string                `yaml:"schema" json:"schema"`
	Namespace      string                `yaml:"namespace" json:"namespace"`
	Name           string                `yaml:"name" json:"name"`
	Version        string                `yaml:"version" json:"version"`
	Description    string                `yaml:"description,omitempty" json:"description,omitempty"`
	Components     map[string]*Component `yaml:"components" json:"components"`
	Schemas        map[string]string     `yaml:"schemas,omitempty" json:"schemas,omitempty"`
	RoomTemplates  map[string]any        `yaml:"room_templates,omitempty" json:"room_templates,omitempty"`
	Intents        map[string]any        `yaml:"intents,omitempty" json:"intents,omitempty"`
	Agents         map[string]any        `yaml:"agents,omitempty" json:"agents,omitempty"`
	Toolboxes      map[string]any        `yaml:"toolboxes,omitempty" json:"toolboxes,omitempty"`
	Providers      map[string]any        `yaml:"providers,omitempty" json:"providers,omitempty"`
	HostInterfaces map[string]any        `yaml:"host_interfaces,omitempty" json:"host_interfaces,omitempty"`
	Tokens         map[string]string     `yaml:"tokens,omitempty" json:"tokens,omitempty"`
	Dependencies   []Dependency          `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
}

type Component struct {
	Name            string             `yaml:"name" json:"name"`
	Description     string             `yaml:"description" json:"description"`
	SemanticRef     string             `yaml:"semantic_ref" json:"semantic_ref"`
	SemanticAliases []string           `yaml:"semantic_aliases,omitempty" json:"semantic_aliases,omitempty"`
	Web             *WebComponent      `yaml:"web,omitempty" json:"web,omitempty"`
	PropsSchema     string             `yaml:"props_schema,omitempty" json:"props_schema,omitempty"`
	Events          map[string]string  `yaml:"events,omitempty" json:"events,omitempty"`
	Fallback        *ComponentFallback `yaml:"fallback,omitempty" json:"fallback,omitempty"`
}

type WebComponent struct {
	Module string `yaml:"module" json:"module"`
	Export string `yaml:"export,omitempty" json:"export,omitempty"`
}

type ComponentFallback struct {
	Element   string `yaml:"element" json:"element"`
	ValueProp string `yaml:"value_prop,omitempty" json:"value_prop,omitempty"`
}

type Dependency struct {
	Package    string `yaml:"package" json:"package"`
	Constraint string `yaml:"constraint,omitempty" json:"constraint,omitempty"`
}

func (m *Manifest) Identity() string {
	if m == nil || m.Namespace == "" || m.Name == "" {
		return ""
	}
	return m.Namespace + "." + m.Name
}

func LoadDir(dir string) (*Manifest, error) {
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("component package: read %q: %w", path, err)
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("component package: parse %q: %w", path, err)
	}
	if err := manifest.Validate(dir); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (m *Manifest) Validate(root string) error {
	var problems []string
	if m == nil {
		return fmt.Errorf("component package: manifest is nil")
	}
	if m.Schema != SchemaV1 {
		problems = append(problems, fmt.Sprintf("schema must be %q", SchemaV1))
	}
	if !segmentRE.MatchString(m.Namespace) {
		problems = append(problems, "namespace must be a lowercase kebab-case segment")
	}
	if !segmentRE.MatchString(m.Name) {
		problems = append(problems, "name must be a lowercase kebab-case segment")
	}
	if _, err := kitver.ParseVersion(m.Version); err != nil {
		problems = append(problems, "version: "+err.Error())
	}
	if m.memberCount() == 0 {
		problems = append(problems, "package must declare at least one selectable member")
	}
	for _, id := range sortedKeys(m.Components) {
		component := m.Components[id]
		path := "components." + id
		if !segmentRE.MatchString(id) {
			problems = append(problems, path+": id must be a lowercase kebab-case segment")
			continue
		}
		if component == nil {
			problems = append(problems, path+": definition is empty")
			continue
		}
		wantRef := m.Identity() + ".component." + id
		if component.SemanticRef != wantRef {
			problems = append(problems, fmt.Sprintf("%s.semantic_ref must be %q", path, wantRef))
		}
		if component.Web == nil && component.Fallback == nil {
			problems = append(problems, path+": requires a web renderer or portable fallback")
		}
		if component.Fallback != nil && component.Fallback.ValueProp != "" {
			if !segmentRE.MatchString(component.Fallback.ValueProp) {
				problems = append(problems, path+".fallback.value_prop: must be a lowercase kebab-case prop name")
			}
			if component.PropsSchema == "" {
				problems = append(problems, path+".fallback.value_prop: requires props_schema")
			} else {
				validateSchemaProperty(
					root, path+".fallback.value_prop", component.PropsSchema,
					component.Fallback.ValueProp, &problems,
				)
			}
		}
		if component.Web != nil {
			validateFile(root, path+".web.module", component.Web.Module, &problems)
		}
		validateFile(root, path+".props_schema", component.PropsSchema, &problems)
		for _, event := range sortedKeys(component.Events) {
			validateMemberID(path+".events", event, &problems)
			if strings.TrimSpace(component.Events[event]) == "" {
				problems = append(problems, path+".events."+event+": payload schema is required")
				continue
			}
			validateFile(root, path+".events."+event, component.Events[event], &problems)
			validatePortableEventSchema(root, path+".events."+event, component.Events[event], &problems)
		}
	}
	for _, id := range sortedKeys(m.Tokens) {
		validateMemberID("tokens", id, &problems)
		validateFile(root, "tokens."+id, m.Tokens[id], &problems)
	}
	for _, id := range sortedKeys(m.Schemas) {
		validateMemberID("schemas", id, &problems)
		validateFile(root, "schemas."+id, m.Schemas[id], &problems)
	}
	for _, id := range sortedKeys(m.RoomTemplates) {
		validateMemberID("room_templates", id, &problems)
		template, ok := m.RoomTemplates[id].(map[string]any)
		if !ok || len(template) == 0 {
			problems = append(problems, "room_templates."+id+": definition is empty")
		}
	}
	for _, id := range sortedKeys(m.Intents) {
		validateMemberID("intents", id, &problems)
	}
	for _, id := range sortedKeys(m.Agents) {
		validateMemberID("agents", id, &problems)
		if m.Agents[id] == nil {
			problems = append(problems, "agents."+id+": definition is empty")
		}
	}
	for _, id := range sortedKeys(m.Toolboxes) {
		validateMemberID("toolboxes", id, &problems)
		if m.Toolboxes[id] == nil {
			problems = append(problems, "toolboxes."+id+": definition is empty")
		}
	}
	for _, id := range sortedKeys(m.Providers) {
		validateMemberID("providers", id, &problems)
		if m.Providers[id] == nil {
			problems = append(problems, "providers."+id+": definition is empty")
		}
	}
	for _, id := range sortedKeys(m.HostInterfaces) {
		validateMemberID("host_interfaces", id, &problems)
		if m.HostInterfaces[id] == nil {
			problems = append(problems, "host_interfaces."+id+": definition is empty")
		}
	}
	dependencyNames := map[string]struct{}{}
	for i, dependency := range m.Dependencies {
		if strings.TrimSpace(dependency.Package) == "" {
			problems = append(problems, fmt.Sprintf("dependencies[%d].package is required", i))
			continue
		}
		if _, duplicate := dependencyNames[dependency.Package]; duplicate {
			problems = append(problems, fmt.Sprintf("dependencies[%d].package %q is duplicated", i, dependency.Package))
		}
		dependencyNames[dependency.Package] = struct{}{}
		if dependency.Constraint != "" {
			if _, err := kitver.Satisfies("0.0.0", dependency.Constraint); err != nil {
				problems = append(problems, fmt.Sprintf("dependencies[%d].constraint: %v", i, err))
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("component package: %s", strings.Join(problems, "; "))
	}
	return nil
}

// VerifyLock requires the package's resolved version and content hash to be
// represented by the same lockfile used for kits.
func (m *Manifest) VerifyLock(lock *kitlock.Lockfile) error {
	if m == nil || lock == nil {
		return fmt.Errorf("component package: manifest and lockfile are required")
	}
	entry := lock.Kits[m.Identity()]
	if entry == nil {
		return fmt.Errorf("component package: %q is not pinned in kits.lock", m.Identity())
	}
	if entry.Version != m.Version {
		return fmt.Errorf("component package: lock version %q does not match manifest version %q", entry.Version, m.Version)
	}
	if strings.TrimSpace(entry.TreeHash) == "" {
		return fmt.Errorf("component package: lock entry %q has no tree_hash", m.Identity())
	}
	ok, err := kitver.Satisfies(m.Version, entry.Constraint)
	if err != nil {
		return fmt.Errorf("component package: lock constraint: %w", err)
	}
	if !ok {
		return fmt.Errorf("component package: version %q does not satisfy lock constraint %q", m.Version, entry.Constraint)
	}
	for _, dependency := range m.Dependencies {
		dependencyEntry := lock.Kits[dependency.Package]
		if dependencyEntry == nil {
			return fmt.Errorf("component package: dependency %q is not pinned in kits.lock", dependency.Package)
		}
		ok, err := kitver.Satisfies(dependencyEntry.Version, dependency.Constraint)
		if err != nil {
			return fmt.Errorf("component package: dependency %q constraint: %w", dependency.Package, err)
		}
		if !ok {
			return fmt.Errorf("component package: dependency %q version %q does not satisfy %q", dependency.Package, dependencyEntry.Version, dependency.Constraint)
		}
		if strings.TrimSpace(dependencyEntry.TreeHash) == "" {
			return fmt.Errorf("component package: dependency %q has no tree_hash", dependency.Package)
		}
	}
	return nil
}

// Selection is an à-la-carte, namespaced subset of a package. It deliberately
// contains no states/root, so consuming it cannot import a room graph.
type Selection struct {
	Components     map[string]*Component
	Schemas        map[string]string
	Tokens         map[string]string
	RoomTemplates  map[string]any
	Intents        map[string]any
	Agents         map[string]any
	Toolboxes      map[string]any
	Providers      map[string]any
	HostInterfaces map[string]any
}

// Select resolves exact selectors such as "components.card" or
// "room_templates.review". Returned keys are package-qualified.
func (m *Manifest) Select(selectors []string) (*Selection, error) {
	out := &Selection{
		Components: map[string]*Component{}, Schemas: map[string]string{}, Tokens: map[string]string{},
		RoomTemplates: map[string]any{}, Intents: map[string]any{},
		Agents: map[string]any{}, Toolboxes: map[string]any{},
		Providers: map[string]any{}, HostInterfaces: map[string]any{},
	}
	for _, selector := range selectors {
		category, id, ok := strings.Cut(selector, ".")
		if !ok || id == "" {
			return nil, fmt.Errorf("component package: selector %q must be category.member", selector)
		}
		qualified := m.Identity() + "." + id
		switch category {
		case "components":
			value, exists := m.Components[id]
			if !exists {
				return nil, unknownSelector(selector)
			}
			out.Components[qualified] = value
		case "schemas":
			value, exists := m.Schemas[id]
			if !exists {
				return nil, unknownSelector(selector)
			}
			out.Schemas[qualified] = value
		case "tokens":
			value, exists := m.Tokens[id]
			if !exists {
				return nil, unknownSelector(selector)
			}
			out.Tokens[qualified] = value
		case "room_templates":
			value, exists := m.RoomTemplates[id]
			if !exists {
				return nil, unknownSelector(selector)
			}
			out.RoomTemplates[qualified] = value
		case "intents":
			value, exists := m.Intents[id]
			if !exists {
				return nil, unknownSelector(selector)
			}
			out.Intents[qualified] = value
		case "agents":
			value, exists := m.Agents[id]
			if !exists {
				return nil, unknownSelector(selector)
			}
			out.Agents[qualified] = value
		case "toolboxes":
			value, exists := m.Toolboxes[id]
			if !exists {
				return nil, unknownSelector(selector)
			}
			out.Toolboxes[qualified] = value
		case "providers":
			value, exists := m.Providers[id]
			if !exists {
				return nil, unknownSelector(selector)
			}
			out.Providers[qualified] = value
		case "host_interfaces":
			value, exists := m.HostInterfaces[id]
			if !exists {
				return nil, unknownSelector(selector)
			}
			out.HostInterfaces[qualified] = value
		default:
			return nil, unknownSelector(selector)
		}
	}
	return out, nil
}

func (m *Manifest) memberCount() int {
	return len(m.Components) + len(m.Schemas) + len(m.Tokens) + len(m.RoomTemplates) + len(m.Intents) +
		len(m.Agents) + len(m.Toolboxes) + len(m.Providers) + len(m.HostInterfaces)
}

func validateMemberID(category, id string, problems *[]string) {
	if !segmentRE.MatchString(id) {
		*problems = append(*problems, category+"."+id+": id must be a lowercase kebab-case segment")
	}
}

func unknownSelector(selector string) error {
	return fmt.Errorf("component package: selector %q is not declared", selector)
}

func validateFile(root, field, name string, problems *[]string) {
	if name == "" {
		return
	}
	if filepath.IsAbs(name) {
		*problems = append(*problems, field+": path must be relative")
		return
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		*problems = append(*problems, field+": path escapes package root")
		return
	}
	if root != "" {
		rootPath, rootErr := filepath.EvalSymlinks(filepath.Clean(root))
		targetPath, targetErr := filepath.EvalSymlinks(filepath.Join(root, clean))
		if rootErr != nil || targetErr != nil {
			*problems = append(*problems, field+": file does not exist")
			return
		}
		relative, relErr := filepath.Rel(rootPath, targetPath)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			*problems = append(*problems, field+": symlink escapes package root")
			return
		}
		if info, err := os.Stat(targetPath); err != nil || info.IsDir() {
			*problems = append(*problems, field+": file does not exist")
		}
	}
}

func validatePortableEventSchema(root, field, name string, problems *[]string) {
	if root == "" || name == "" || filepath.IsAbs(name) {
		return
	}
	rootPath, rootErr := filepath.EvalSymlinks(filepath.Clean(root))
	targetPath, targetErr := filepath.EvalSymlinks(filepath.Join(root, filepath.Clean(name)))
	if rootErr != nil || targetErr != nil {
		return
	}
	relative, relErr := filepath.Rel(rootPath, targetPath)
	if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return
	}
	raw, err := os.ReadFile(targetPath)
	if err != nil {
		return
	}
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		*problems = append(*problems, field+": payload schema is not valid JSON")
		return
	}
	if err := validatePortableSchemaNode(schema); err != nil {
		*problems = append(*problems, field+": "+err.Error())
		return
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("application://component-event/schema", schema); err != nil {
		*problems = append(*problems, field+": invalid payload schema: "+err.Error())
		return
	}
	if _, err := compiler.Compile("application://component-event/schema"); err != nil {
		*problems = append(*problems, field+": invalid payload schema: "+err.Error())
	}
}

func validateSchemaProperty(root, field, schemaFile, property string, problems *[]string) {
	if root == "" || schemaFile == "" || filepath.IsAbs(schemaFile) {
		return
	}
	rootPath, rootErr := filepath.EvalSymlinks(filepath.Clean(root))
	targetPath, targetErr := filepath.EvalSymlinks(filepath.Join(root, filepath.Clean(schemaFile)))
	if rootErr != nil || targetErr != nil {
		return
	}
	relative, relErr := filepath.Rel(rootPath, targetPath)
	if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return
	}
	raw, err := os.ReadFile(targetPath)
	if err != nil {
		return
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &schema) != nil {
		return
	}
	if _, ok := schema.Properties[property]; !ok {
		*problems = append(*problems, fmt.Sprintf("%s: %q is not declared by props_schema", field, property))
	}
}

func validatePortableSchemaNode(schema any) error {
	if _, ok := schema.(bool); ok {
		return nil
	}
	object, ok := schema.(map[string]any)
	if !ok {
		return fmt.Errorf("payload schema nodes must be objects or booleans")
	}
	for keyword, value := range object {
		switch keyword {
		case "$schema", "$id", "title", "description", "default", "examples",
			"type", "enum", "const", "required",
			"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
			"minLength", "maxLength", "pattern",
			"minItems", "maxItems", "uniqueItems",
			"minProperties", "maxProperties":
		case "properties":
			properties, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("properties must be an object")
			}
			for name, property := range properties {
				if err := validatePortableSchemaNode(property); err != nil {
					return fmt.Errorf("properties.%s: %w", name, err)
				}
			}
		case "items", "contains", "not", "additionalProperties":
			if _, boolean := value.(bool); !boolean {
				if err := validatePortableSchemaNode(value); err != nil {
					return fmt.Errorf("%s: %w", keyword, err)
				}
			}
		case "allOf", "anyOf", "oneOf", "prefixItems":
			members, ok := value.([]any)
			if !ok {
				return fmt.Errorf("%s must be an array", keyword)
			}
			for i, member := range members {
				if err := validatePortableSchemaNode(member); err != nil {
					return fmt.Errorf("%s[%d]: %w", keyword, i, err)
				}
			}
		default:
			return fmt.Errorf("unsupported portable payload-schema keyword %q", keyword)
		}
	}
	return nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
