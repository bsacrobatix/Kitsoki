package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// JSONSchemaValidator validates application inputs and outputs against real
// Draft 2020-12 JSON Schemas. Compiled schemas are cached by canonical digest
// and resource provenance. Roots permit file references strictly within
// verified story/package boundaries. Network and escaping paths are denied.
type JSONSchemaValidator struct {
	Root  string
	Roots []string
	cache sync.Map
}

func (v *JSONSchemaValidator) Validate(ctx context.Context, schemaRaw, valueRaw json.RawMessage) error {
	return v.validate(ctx, SchemaReference{}, schemaRaw, valueRaw)
}

// ValidateReference validates with a trusted, wire-excluded schema location.
// Relative $id and $ref values resolve from the schema file while every file
// load remains inside that schema's owning story or package root.
func (v *JSONSchemaValidator) ValidateReference(
	ctx context.Context,
	reference SchemaReference,
	schemaRaw json.RawMessage,
	valueRaw json.RawMessage,
) error {
	return v.validate(ctx, reference, schemaRaw, valueRaw)
}

func (v *JSONSchemaValidator) validate(
	_ context.Context,
	reference SchemaReference,
	schemaRaw json.RawMessage,
	valueRaw json.RawMessage,
) error {
	digest, err := DigestJSON(schemaRaw)
	if err != nil {
		return fmt.Errorf("invalid schema document: %w", err)
	}
	roots, resourceURI, err := v.compileScope(reference, digest)
	if err != nil {
		return err
	}
	cacheKey := digest + "\x00" + resourceURI + "\x00" + strings.Join(roots, "\x00")
	var compiled *jsonschema.Schema
	if cached, ok := v.cache.Load(cacheKey); ok {
		compiled = cached.(*jsonschema.Schema)
	} else {
		var document any
		if err := json.Unmarshal(schemaRaw, &document); err != nil {
			return fmt.Errorf("parse schema: %w", err)
		}
		compiler := jsonschema.NewCompiler()
		compiler.UseLoader(rootedSchemaLoader{roots: roots})
		if err := compiler.AddResource(resourceURI, document); err != nil {
			return fmt.Errorf("register schema: %w", err)
		}
		compiled, err = compiler.Compile(resourceURI)
		if err != nil {
			return fmt.Errorf("compile schema: %w", err)
		}
		actual, _ := v.cache.LoadOrStore(cacheKey, compiled)
		compiled = actual.(*jsonschema.Schema)
	}
	var value any
	if err := json.Unmarshal(valueRaw, &value); err != nil {
		return fmt.Errorf("parse value: %w", err)
	}
	if err := compiled.Validate(value); err != nil {
		return formatSchemaValidationError(err)
	}
	return nil
}

func (v *JSONSchemaValidator) compileScope(
	reference SchemaReference,
	digest string,
) ([]string, string, error) {
	configured, err := canonicalSchemaRoots(append([]string{v.Root}, v.Roots...))
	if err != nil {
		return nil, "", err
	}
	if reference.Path == "" && reference.Root == "" {
		return configured, "application://schema/" + strings.TrimPrefix(digest, "sha256:"), nil
	}
	if reference.Path == "" || reference.Root == "" {
		return nil, "", fmt.Errorf("application schema reference provenance is incomplete")
	}
	owner, err := filepath.EvalSymlinks(filepath.Clean(reference.Root))
	if err != nil {
		return nil, "", fmt.Errorf("application schema owning root: %w", err)
	}
	target, err := filepath.EvalSymlinks(filepath.Clean(reference.Path))
	if err != nil {
		return nil, "", fmt.Errorf("application schema resource: %w", err)
	}
	if !pathWithinSchemaRoot(owner, target) {
		return nil, "", fmt.Errorf("application schema resource escapes its owning story root")
	}
	if len(configured) > 0 {
		authorized := false
		for _, root := range configured {
			if pathWithinSchemaRoot(root, owner) {
				authorized = true
				break
			}
		}
		if !authorized {
			return nil, "", fmt.Errorf("application schema owning root is not registered")
		}
	}
	return []string{owner}, (&url.URL{Scheme: "file", Path: target}).String(), nil
}

type rootedSchemaLoader struct {
	roots []string
}

func (l rootedSchemaLoader) Load(rawURL string) (any, error) {
	reference, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("application schema reference %q: %w", rawURL, err)
	}
	if reference.Scheme != "file" {
		return nil, fmt.Errorf("application schema reference %q: network and non-file references are denied", rawURL)
	}
	if reference.Host != "" && reference.Host != "localhost" {
		return nil, fmt.Errorf("application schema reference %q: remote file hosts are denied", rawURL)
	}
	if len(l.roots) == 0 {
		return nil, fmt.Errorf("application schema reference %q: no story root is configured", rawURL)
	}
	path, err := url.PathUnescape(reference.Path)
	if err != nil {
		return nil, fmt.Errorf("application schema reference %q: %w", rawURL, err)
	}
	target, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("application schema reference %q: %w", rawURL, err)
	}
	if !pathWithinAnySchemaRoot(l.roots, target) {
		return nil, fmt.Errorf("application schema reference %q escapes story root", rawURL)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("application schema reference %q: %w", rawURL, err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("application schema reference %q: parse: %w", rawURL, err)
	}
	return document, nil
}

func canonicalSchemaRoots(candidates []string) ([]string, error) {
	seen := make(map[string]struct{}, len(candidates))
	roots := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		root, err := filepath.EvalSymlinks(filepath.Clean(candidate))
		if err != nil {
			return nil, fmt.Errorf("application schema root: %w", err)
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return roots, nil
}

func pathWithinAnySchemaRoot(roots []string, target string) bool {
	for _, root := range roots {
		if pathWithinSchemaRoot(root, target) {
			return true
		}
	}
	return false
}

func formatSchemaValidationError(err error) error {
	var validation *jsonschema.ValidationError
	if !errors.As(err, &validation) {
		return err
	}
	var details []string
	collectSchemaValidationErrors(validation.BasicOutput(), &details)
	if len(details) == 0 {
		return fmt.Errorf("schema validation failed: %w", err)
	}
	return fmt.Errorf("schema validation failed: %s", strings.Join(details, "; "))
}

func collectSchemaValidationErrors(unit *jsonschema.OutputUnit, details *[]string) {
	if unit == nil || unit.Valid {
		return
	}
	if len(unit.Errors) == 0 && unit.Error != nil {
		location := unit.InstanceLocation
		if location == "" {
			location = "/"
		}
		*details = append(*details, location+": "+unit.Error.String())
		return
	}
	for i := range unit.Errors {
		collectSchemaValidationErrors(&unit.Errors[i], details)
	}
}
