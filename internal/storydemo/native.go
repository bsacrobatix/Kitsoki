package storydemo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const nativeManifestSchema = "kitsoki/story-demo-manifest/v1"
const legacyManifestSchema = "kitsoki/legacy-demo-manifest/v1"

// NativePlatform implements typed demo operations using only graph and
// artifact primitives. It never launches a process or interprets executable
// text.
type NativePlatform struct{}

func (NativePlatform) Run(_ context.Context, root string, task Task) (TaskResult, error) {
	records := make([]artifactRecord, 0, len(task.Artifacts))
	for _, artifact := range task.Artifacts {
		record, err := artifactMetadata(root, artifact)
		if err != nil {
			return TaskResult{}, err
		}
		records = append(records, record)
	}
	raw, err := json.Marshal(struct {
		ID        string           `json:"id"`
		Phase     string           `json:"phase"`
		Artifacts []artifactRecord `json:"artifacts"`
	}{ID: task.ID, Phase: task.Phase, Artifacts: records})
	if err != nil {
		return TaskResult{}, err
	}
	return TaskResult{ID: task.ID, OK: true, OutputHash: semanticDigest("materialized-artifacts", raw)}, nil
}

func (NativePlatform) Create(_ context.Context, _ string, _ MockupManifest) (ToolResult, error) {
	return ToolResult{}, fmt.Errorf(
		"Story Application mockup creation is unavailable until a typed artifact executor is configured",
	)
}

func (NativePlatform) Check(_ context.Context, root string, manifest Manifest) (DoctorResult, error) {
	document, raw, err := readNativeManifest(root, manifest.Path)
	if err != nil {
		return DoctorResult{}, err
	}
	artifacts, err := resolveNativeArtifacts(root, manifest.Path, document.Artifacts)
	if err != nil {
		return DoctorResult{
			Report: map[string]any{"schema": document.Schema, "ok": false, "error": err.Error()},
			OK:     false,
		}, nil
	}
	checks := []map[string]any{
		{"id": "manifest-schema", "ok": supportedManifestSchema(document.Schema)},
		{"id": "manifest-json", "ok": json.Valid(raw)},
		{"id": "artifacts-resolved", "ok": len(artifacts) == len(document.Artifacts)},
	}
	ok := true
	for _, check := range checks {
		value, _ := check["ok"].(bool)
		ok = ok && value
	}
	return DoctorResult{Report: map[string]any{
		"schema": document.Schema, "ok": ok, "checks": checks,
		"artifact_count": len(artifacts),
	}, OK: ok}, nil
}

type nativeManifest struct {
	Schema    string             `json:"schema"`
	Mockup    string             `json:"mockup"`
	Scenario  string             `json:"scenario"`
	Artifacts []nativeArtifact   `json:"artifacts"`
	Capture   *nativeCapturePlan `json:"capture,omitempty"`
}

type nativeCapturePlan struct {
	ApplicationID string   `json:"application_id"`
	ScenarioRef   string   `json:"scenario_ref"`
	ActionIDs     []string `json:"action_ids"`
}

type legacyManifest struct {
	Version int    `json:"version"`
	Mockup  string `json:"mockup"`
	Deck    string `json:"deck"`
	Tours   []struct {
		Out string `json:"out"`
	} `json:"tours"`
}

type nativeArtifact struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

func readNativeManifest(root, path string) (nativeManifest, []byte, error) {
	path, err := containedExistingPath(root, path)
	if err != nil {
		return nativeManifest{}, nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nativeManifest{}, nil, err
	}
	if len(raw) > maxPayloadBytes {
		return nativeManifest{}, nil, fmt.Errorf("demo manifest is %d bytes, exceeds %d; refusing to truncate", len(raw), maxPayloadBytes)
	}
	var document nativeManifest
	if err := json.Unmarshal(raw, &document); err != nil {
		return nativeManifest{}, nil, fmt.Errorf("decode native demo manifest: %w", err)
	}
	if document.Schema == "" {
		var legacy legacyManifest
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return nativeManifest{}, nil, fmt.Errorf("decode legacy demo manifest: %w", err)
		}
		if legacy.Version != 1 {
			return nativeManifest{}, nil, fmt.Errorf("unsupported demo manifest version %d", legacy.Version)
		}
		document.Schema = legacyManifestSchema
		if legacy.Mockup != "" {
			document.Artifacts = append(document.Artifacts, nativeArtifact{Kind: "mockup", Path: legacy.Mockup})
		}
		if legacy.Deck != "" {
			document.Artifacts = append(document.Artifacts, nativeArtifact{Kind: "slidey", Path: legacy.Deck})
		}
		for _, tour := range legacy.Tours {
			if tour.Out != "" {
				document.Artifacts = append(document.Artifacts, nativeArtifact{Kind: "rrweb", Path: tour.Out})
			}
		}
	}
	if !supportedManifestSchema(document.Schema) {
		return nativeManifest{}, nil, fmt.Errorf("unsupported demo manifest schema %q", document.Schema)
	}
	if len(document.Artifacts) > maxArtifacts {
		return nativeManifest{}, nil, fmt.Errorf("demo manifest declares %d artifacts, exceeds %d; refusing to truncate", len(document.Artifacts), maxArtifacts)
	}
	if document.Capture != nil {
		if err := validateCapturePlan(document.Capture); err != nil {
			return nativeManifest{}, nil, err
		}
	}
	return document, raw, nil
}

func validateCapturePlan(plan *nativeCapturePlan) error {
	if plan == nil {
		return nil
	}
	if strings.TrimSpace(plan.ApplicationID) == "" || len(plan.ApplicationID) > maxNodeIDBytes {
		return fmt.Errorf("capture application_id is required and bounded")
	}
	if strings.TrimSpace(plan.ScenarioRef) == "" || len(plan.ScenarioRef) > 1024 {
		return fmt.Errorf("capture scenario_ref is required and bounded")
	}
	if len(plan.ActionIDs) == 0 || len(plan.ActionIDs) > maxTasks {
		return fmt.Errorf("capture action count %d is outside 1..%d", len(plan.ActionIDs), maxTasks)
	}
	for index, id := range plan.ActionIDs {
		if strings.TrimSpace(id) == "" || len(id) > maxNodeIDBytes {
			return fmt.Errorf("capture action %d is empty or exceeds %d bytes", index, maxNodeIDBytes)
		}
	}
	return nil
}

func resolveNativeArtifacts(root, manifestPath string, declarations []nativeArtifact) ([]Artifact, error) {
	out := make([]Artifact, 0, len(declarations))
	var total int64
	for index, declaration := range declarations {
		if declaration.Path == "" || filepath.IsAbs(declaration.Path) {
			return nil, fmt.Errorf("artifact %d path must be manifest-relative", index)
		}
		clean := filepath.Clean(declaration.Path)
		path, err := containedExistingPath(root, filepath.Join(filepath.Dir(manifestPath), clean))
		if err != nil {
			return nil, fmt.Errorf("artifact %d: %w", index, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("artifact %d is not a regular file", index)
		}
		total += info.Size()
		if total > maxArtifactBytes {
			return nil, fmt.Errorf("demo artifacts exceed %d bytes; refusing to truncate", maxArtifactBytes)
		}
		kind := declaration.Kind
		if kind == "" {
			kind = "artifact"
		}
		if kind == "rrweb" || strings.HasSuffix(strings.ToLower(path), ".rrweb.json") {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			if !json.Valid(raw) {
				return nil, fmt.Errorf("rrweb artifact %d is not valid JSON", index)
			}
		}
		out = append(out, Artifact{Kind: kind, Path: path})
	}
	return out, nil
}

func supportedManifestSchema(schema string) bool {
	return schema == nativeManifestSchema || schema == legacyManifestSchema
}

func writeAtomic(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".story-demo-*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
