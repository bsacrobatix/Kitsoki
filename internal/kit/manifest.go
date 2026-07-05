// Package kit validates and loads kit.yaml manifests: the kit/v1 pinned
// schema that names, versions, and describes a distributable bundle of
// stories (docs/proposals/kits.md, .context/kits-implementation-plan.md "S1").
//
// The pattern mirrors internal/projectprofile: a pinned JSON Schema embedded
// via go:embed, validated with santhosh-tekuri/jsonschema/v6, plus a typed Go
// projection (KitDef) for callers that want structured access rather than a
// raw map. internal/app.BuildKitImporter consumes KitDef the way
// SynthesizeRoot/BuildRootImporter consume the dev-story base story.
package kit

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	goyaml "github.com/goccy/go-yaml"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema.json
var schemaBytes []byte

// SchemaJSON returns the embedded kit/v1 JSON Schema.
func SchemaJSON() []byte {
	out := make([]byte, len(schemaBytes))
	copy(out, schemaBytes)
	return out
}

// Result is the machine-readable schema-validation report, mirroring
// projectprofile.Result.
type Result struct {
	OK     bool     `json:"ok"`
	Schema []string `json:"schema,omitempty"`
}

// ManifestFileName is the conventional manifest filename inside a kit root.
const ManifestFileName = "kit.yaml"

// Dependency is one `extends:`/`composes:` entry.
type Dependency struct {
	Kit        string `yaml:"kit"`
	Constraint string `yaml:"constraint,omitempty"`
}

// Parameter is one entry in the `parameters:` specialization surface.
type Parameter struct {
	Type        string `yaml:"type"`
	Required    bool   `yaml:"required,omitempty"`
	Default     any    `yaml:"default,omitempty"`
	Description string `yaml:"description,omitempty"`
}

// Provides declares what a kit contributes.
type Provides struct {
	Stories    []string `yaml:"stories"`
	Schemas    []string `yaml:"schemas,omitempty"`
	Interfaces []string `yaml:"interfaces,omitempty"`
	UI         []string `yaml:"ui,omitempty"`
	Onboarding string   `yaml:"onboarding,omitempty"`
}

// Conformance declares the kit's no-LLM contract suite.
type Conformance struct {
	Flows []string `yaml:"flows,omitempty"`
}

// Requires declares engine version constraints.
type Requires struct {
	Kitsoki string `yaml:"kitsoki,omitempty"`
}

// Compat declares mechanical migration hints for downstream absorbers.
type Compat struct {
	// Renamed is keyed by category (exits, intents, world, ...) then
	// old-name -> new-name.
	Renamed map[string]map[string]string `yaml:"renamed,omitempty"`
}

// Def is the typed projection of a kit.yaml document.
type Def struct {
	Schema    string `yaml:"schema"`
	Kit       string `yaml:"kit"`
	Namespace string `yaml:"namespace"`
	Version   string `yaml:"version"`
	Title     string `yaml:"title,omitempty"`
	Summary   string `yaml:"summary,omitempty"`

	Requires Requires `yaml:"requires,omitempty"`

	Extends  []Dependency `yaml:"extends,omitempty"`
	Composes []Dependency `yaml:"composes,omitempty"`

	Parameters map[string]Parameter `yaml:"parameters,omitempty"`

	Provides    Provides    `yaml:"provides"`
	Conformance Conformance `yaml:"conformance,omitempty"`
	Compat      Compat      `yaml:"compat,omitempty"`

	// dir is the absolute kit root directory this manifest was loaded from
	// (not part of the YAML surface). Empty when built in-memory.
	dir string
}

// Identity returns the fully-qualified "@namespace/kit" name.
func (d *Def) Identity() string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("@%s/%s", d.Namespace, d.Kit)
}

// Dir returns the kit root directory this manifest was loaded from.
func (d *Def) Dir() string {
	if d == nil {
		return ""
	}
	return d.dir
}

// HasStory reports whether name is among provides.stories.
func (d *Def) HasStory(name string) bool {
	if d == nil {
		return false
	}
	for _, s := range d.Provides.Stories {
		if s == name {
			return true
		}
	}
	return false
}

// StoryDir returns the absolute directory a provided story resolves to:
// <kit-root>/stories/<name>. Callers should check HasStory first; StoryDir
// does not itself validate that name is declared.
func (d *Def) StoryDir(name string) string {
	if d == nil {
		return ""
	}
	return filepath.Join(d.dir, "stories", name)
}

// ValidateFile validates a kit.yaml file on disk against the kit/v1 schema
// (schema-only; no semantic/filesystem cross-checks — those live in Load,
// which also checks provided story dirs exist).
func ValidateFile(path string) (Result, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("read kit manifest: %w", err)
	}
	return Validate(raw)
}

// Validate validates kit/v1 YAML bytes against the schema.
func Validate(raw []byte) (Result, error) {
	doc, err := decodeYAML(raw)
	if err != nil {
		return Result{}, err
	}
	res := Result{Schema: validateSchema(doc)}
	res.OK = len(res.Schema) == 0
	return res, nil
}

// Load reads, schema-validates, and decodes a kit.yaml at manifestPath into a
// Def. manifestPath's parent directory becomes Def.Dir(). Load additionally
// checks that every provides.stories entry resolves to an existing
// stories/<name>/app.yaml on disk (a "does this kit even contain what it
// claims" fail-fast, distinct from the pure-schema Validate/ValidateFile
// entry points).
func Load(manifestPath string) (*Def, error) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read kit manifest: %w", err)
	}
	res, err := Validate(raw)
	if err != nil {
		return nil, err
	}
	if !res.OK {
		return nil, fmt.Errorf("kit manifest %s failed kit/v1 schema validation: %v", manifestPath, res.Schema)
	}

	var def Def
	if err := goyaml.Unmarshal(raw, &def); err != nil {
		return nil, fmt.Errorf("decode kit manifest %s: %w", manifestPath, err)
	}

	abs, err := filepath.Abs(filepath.Dir(manifestPath))
	if err != nil {
		abs = filepath.Dir(manifestPath)
	}
	def.dir = abs

	var missing []string
	for _, story := range def.Provides.Stories {
		storyManifest := filepath.Join(def.StoryDir(story), "app.yaml")
		if _, statErr := os.Stat(storyManifest); statErr != nil {
			missing = append(missing, story)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("kit manifest %s: provides.stories names story(ies) with no stories/<name>/app.yaml on disk: %v", manifestPath, missing)
	}

	return &def, nil
}

// LoadDir is Load(filepath.Join(kitDir, ManifestFileName)).
func LoadDir(kitDir string) (*Def, error) {
	return Load(filepath.Join(kitDir, ManifestFileName))
}

func decodeYAML(raw []byte) (map[string]any, error) {
	var doc map[string]any
	if err := goyaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse kit manifest yaml: %w", err)
	}
	normalized := normalize(doc)
	root, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("kit manifest must be an object")
	}
	return root, nil
}

func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[k] = normalize(v)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[fmt.Sprint(k)] = normalize(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = normalize(v)
		}
		return out
	default:
		return v
	}
}

func validateSchema(doc map[string]any) []string {
	var schemaVal any
	if err := json.Unmarshal(schemaBytes, &schemaVal); err != nil {
		return []string{"embedded kit/v1 schema is invalid JSON: " + err.Error()}
	}
	compiler := jsonschema.NewCompiler()
	const uri = "kitsoki://kit/v1"
	if err := compiler.AddResource(uri, schemaVal); err != nil {
		return []string{"embedded kit/v1 schema is invalid: " + err.Error()}
	}
	schema, err := compiler.Compile(uri)
	if err != nil {
		return []string{"embedded kit/v1 schema does not compile: " + err.Error()}
	}
	if err := schema.Validate(doc); err != nil {
		return formatValidationErrors(err)
	}
	return nil
}

func formatValidationErrors(err error) []string {
	verr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []string{err.Error()}
	}
	var out []string
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			out = append(out, e.Error())
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(verr)
	sort.Strings(out)
	return out
}
