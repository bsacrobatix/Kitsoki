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
// Draft 2020-12 JSON Schemas. Compiled schemas are cached by canonical digest.
// Root, when set, permits file references strictly within one story/package.
// Network references and paths that escape Root are always denied.
type JSONSchemaValidator struct {
	Root  string
	cache sync.Map
}

func (v *JSONSchemaValidator) Validate(_ context.Context, schemaRaw, valueRaw json.RawMessage) error {
	digest, err := DigestJSON(schemaRaw)
	if err != nil {
		return fmt.Errorf("invalid schema document: %w", err)
	}
	var compiled *jsonschema.Schema
	if cached, ok := v.cache.Load(digest); ok {
		compiled = cached.(*jsonschema.Schema)
	} else {
		var document any
		if err := json.Unmarshal(schemaRaw, &document); err != nil {
			return fmt.Errorf("parse schema: %w", err)
		}
		compiler := jsonschema.NewCompiler()
		compiler.UseLoader(rootedSchemaLoader{root: v.Root})
		uri := "application://schema/" + strings.TrimPrefix(digest, "sha256:")
		if err := compiler.AddResource(uri, document); err != nil {
			return fmt.Errorf("register schema: %w", err)
		}
		compiled, err = compiler.Compile(uri)
		if err != nil {
			return fmt.Errorf("compile schema: %w", err)
		}
		actual, _ := v.cache.LoadOrStore(digest, compiled)
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

type rootedSchemaLoader struct {
	root string
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
	if l.root == "" {
		return nil, fmt.Errorf("application schema reference %q: no story root is configured", rawURL)
	}
	path, err := url.PathUnescape(reference.Path)
	if err != nil {
		return nil, fmt.Errorf("application schema reference %q: %w", rawURL, err)
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(l.root))
	if err != nil {
		return nil, fmt.Errorf("application schema root: %w", err)
	}
	target, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("application schema reference %q: %w", rawURL, err)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("application schema reference %q escapes story root %q", rawURL, root)
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
