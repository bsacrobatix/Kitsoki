package application

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kitsoki/internal/app"
)

// SchemaReference is internal filesystem provenance for one loaded schema.
// Wire-facing application types embed it with json:"-" so local paths never
// enter frames, discovery, receipts, or bundles.
type SchemaReference struct {
	Path string `json:"-"`
	Root string `json:"-"`
}

// ResolvedSchema pairs normalized schema JSON with its owning story or package
// root. Relative identifiers and references are resolved from Path.
type ResolvedSchema struct {
	Schema    json.RawMessage
	Reference SchemaReference
}

// ResolveApplicationSchema loads one story-declared schema from the root
// application, an imported story, or a verified application package.
func ResolveApplicationSchema(
	def *app.AppDef,
	label string,
	reference string,
) (ResolvedSchema, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return ResolvedSchema{}, nil
	}
	if def == nil || def.BaseDir == "" {
		return ResolvedSchema{}, fmt.Errorf("application: %s schema %q has no story root", label, reference)
	}
	if strings.Contains(reference, "{{") {
		return ResolvedSchema{}, fmt.Errorf("application: %s schema path may not be templated", label)
	}
	target := reference
	if !filepath.IsAbs(target) {
		target = filepath.Join(def.BaseDir, target)
	}
	target, err := filepath.EvalSymlinks(filepath.Clean(target))
	if err != nil {
		return ResolvedSchema{}, fmt.Errorf("application: %s schema %q: %w", label, reference, err)
	}
	roots := ApplicationSchemaRoots(def)
	owner := ""
	for _, root := range roots {
		if pathWithinSchemaRoot(root, target) && len(root) > len(owner) {
			owner = root
		}
	}
	if owner == "" {
		return ResolvedSchema{}, fmt.Errorf(
			"application: %s schema %q escapes story and package roots",
			label,
			reference,
		)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return ResolvedSchema{}, fmt.Errorf("application: %s schema %q: %w", label, reference, err)
	}
	normalized, err := NormalizeJSON(raw)
	if err != nil {
		return ResolvedSchema{}, fmt.Errorf("application: %s schema %q: %w", label, reference, err)
	}
	return ResolvedSchema{
		Schema:    normalized,
		Reference: SchemaReference{Path: target, Root: owner},
	}, nil
}

// ApplicationSchemaRoots returns canonical story, import, and verified package
// roots. The most specific root is first so nested imported stories retain
// their own boundary instead of inheriting a broader parent root.
func ApplicationSchemaRoots(def *app.AppDef) []string {
	if def == nil {
		return nil
	}
	candidates := []string{def.BaseDir}
	for _, manifest := range def.LoadedManifests {
		candidates = append(candidates, filepath.Dir(manifest))
	}
	candidates = append(candidates, def.ApplicationPackageRoots...)
	seen := make(map[string]struct{}, len(candidates))
	roots := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		root, err := filepath.EvalSymlinks(filepath.Clean(candidate))
		if err != nil {
			continue
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	sort.Slice(roots, func(i, j int) bool {
		if len(roots[i]) == len(roots[j]) {
			return roots[i] < roots[j]
		}
		return len(roots[i]) > len(roots[j])
	})
	return roots
}

func pathWithinSchemaRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
