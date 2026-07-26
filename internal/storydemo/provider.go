package storydemo

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"kitsoki/internal/host"
)

const receiptSchema = "kitsoki/story-demo-receipt/v1"

// BoundAuthorizer requires an authenticated actor and the exact bound app.
type BoundAuthorizer struct {
	AppID string
	Root  string
}

func (a BoundAuthorizer) Authorize(_ context.Context, scope Scope) error {
	if strings.TrimSpace(scope.Actor) == "" {
		return fmt.Errorf("authenticated actor is required")
	}
	if scope.AppID != a.AppID {
		return fmt.Errorf("application %q is outside bound scope", scope.AppID)
	}
	want, err := filepath.Abs(a.Root)
	if err != nil {
		return err
	}
	got, err := filepath.Abs(scope.Root)
	if err != nil {
		return err
	}
	if filepath.Clean(want) != filepath.Clean(got) {
		return fmt.Errorf("application root is outside bound scope")
	}
	return nil
}

// NewHandler returns the app-scoped typed host.demo prefix handler.
func NewHandler(deps Dependencies) host.Handler {
	var mu sync.Mutex
	return func(ctx context.Context, args map[string]any) (host.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := validateDependencies(deps); err != nil {
			return host.Result{}, err
		}
		op, _ := args["op"].(string)
		if err := deps.Authorizer.Authorize(ctx, Scope{
			AppID: deps.AppID, Root: deps.Root, Actor: host.ActorFromContext(ctx), Op: op,
		}); err != nil {
			return host.Result{}, fmt.Errorf("host.demo.%s: unauthorized: %w", op, err)
		}
		switch op {
		case "plan":
			return planOp(ctx, deps, args)
		case "materialize":
			return materializeOp(ctx, deps, args)
		case "project_mockup":
			return projectMockupOp(ctx, deps, args)
		case "create_mockup":
			return createMockupOp(ctx, deps, args)
		case "record":
			return recordOp(ctx, deps, args)
		case "doctor":
			return doctorOp(ctx, deps, args)
		default:
			return host.Result{}, fmt.Errorf("host.demo: unknown typed op %q", op)
		}
	}
}

func validateDependencies(deps Dependencies) error {
	switch {
	case strings.TrimSpace(deps.AppID) == "":
		return fmt.Errorf("host.demo: app id is unavailable")
	case strings.TrimSpace(deps.Root) == "":
		return fmt.Errorf("host.demo: app root is unavailable")
	case deps.Authorizer == nil:
		return fmt.Errorf("host.demo: authorizer is unavailable")
	case deps.Resolver == nil:
		return fmt.Errorf("host.demo: resolver is unavailable")
	case deps.Materializer == nil:
		return fmt.Errorf("host.demo: materializer is unavailable")
	case deps.Creator == nil:
		return fmt.Errorf("host.demo: mockup creator is unavailable")
	case deps.Capture == nil:
		return fmt.Errorf("host.demo: capture adapter is unavailable")
	case deps.Doctor == nil:
		return fmt.Errorf("host.demo: doctor adapter is unavailable")
	case deps.Evidence == nil:
		return fmt.Errorf("host.demo: evidence store is unavailable")
	case deps.Clock == nil:
		return fmt.Errorf("host.demo: clock is unavailable")
	default:
		return nil
	}
}

func planOp(ctx context.Context, deps Dependencies, args map[string]any) (host.Result, error) {
	catalogPath, nodeID, err := graphArgs("plan", args, nil)
	if err != nil {
		return host.Result{}, err
	}
	plan, err := deps.Resolver.Plan(ctx, deps.Root, catalogPath, nodeID)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.plan: resolve: %w", err)
	}
	if len(plan.ClosureOrder) == 0 || len(plan.ClosureOrder) > maxClosureNodes {
		return host.Result{}, fmt.Errorf("host.demo.plan: invalid closure size %d; refusing to truncate", len(plan.ClosureOrder))
	}
	manifestRef, err := putJSON(ctx, deps, "manifest", manifestRecord{Path: plan.Manifest.Path})
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.plan: persist manifest ref: %w", err)
	}
	handles, err := putArtifacts(ctx, deps, plan.Artifacts)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.plan: persist artifacts: %w", err)
	}
	return result(map[string]any{
		"closure_order": plan.ClosureOrder, "manifest_ref": manifestRef, "artifact_handles": handles,
	}), nil
}

func materializeOp(ctx context.Context, deps Dependencies, args map[string]any) (host.Result, error) {
	phase, err := requiredString("materialize", args, "phase", 32)
	if err != nil {
		return host.Result{}, err
	}
	if phase != "dependencies" && phase != "subject" && phase != "verify" {
		return host.Result{}, fmt.Errorf("host.demo.materialize: phase must be dependencies, subject, or verify")
	}
	_, _, err = graphArgs("materialize", args, map[string]bool{"phase": true})
	if err != nil {
		return host.Result{}, err
	}
	return host.Result{}, fmt.Errorf(
		"host.demo.materialize: phase %q is unavailable until a typed phase-action executor is configured",
		phase,
	)
}

func projectMockupOp(ctx context.Context, deps Dependencies, args map[string]any) (host.Result, error) {
	audience, err := requiredString("project_mockup", args, "audience", maxAudienceBytes)
	if err != nil {
		return host.Result{}, err
	}
	if audience != "internal" && audience != "public" {
		return host.Result{}, fmt.Errorf("host.demo.project_mockup: audience must be internal or public")
	}
	catalogPath, nodeID, err := graphArgs("project_mockup", args, map[string]bool{"audience": true})
	if err != nil {
		return host.Result{}, err
	}
	projection, err := deps.Resolver.ProjectMockup(ctx, deps.Root, catalogPath, nodeID, audience)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.project_mockup: resolve: %w", err)
	}
	scenarioRef, err := putJSON(ctx, deps, "scenario", projection.Scenario)
	if err != nil {
		return host.Result{}, err
	}
	projection.Manifest.ScenarioRef = scenarioRef
	manifestRef, err := putJSON(ctx, deps, "manifest", manifestRecord{Mockup: &projection.Manifest})
	if err != nil {
		return host.Result{}, err
	}
	return result(map[string]any{"scenario_ref": scenarioRef, "manifest_ref": manifestRef}), nil
}

func createMockupOp(ctx context.Context, deps Dependencies, args map[string]any) (host.Result, error) {
	manifestRef, err := refArgs("create_mockup", args)
	if err != nil {
		return host.Result{}, err
	}
	record, err := resolveManifest(ctx, deps, manifestRef)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.create_mockup: %w", err)
	}
	if record.Mockup == nil {
		return host.Result{}, fmt.Errorf("host.demo.create_mockup: manifest ref is not a projected mockup")
	}
	key := semanticDigest("create-receipt", []byte(manifestRef))
	if _, prior, ok, err := deps.Evidence.Find(ctx, "create", key); err != nil {
		return host.Result{}, err
	} else if ok {
		var receipt operationReceipt
		if err := json.Unmarshal(prior, &receipt); err != nil {
			return host.Result{}, err
		}
		mockupRef := ""
		if len(receipt.Artifacts) > 0 {
			mockupRef = receipt.Artifacts[0]
		}
		return result(map[string]any{"mockup_ref": mockupRef, "artifact_handles": receipt.Artifacts}), nil
	}
	created, err := deps.Creator.Create(ctx, deps.Root, *record.Mockup)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.create_mockup: create: %w", err)
	}
	artifacts := append([]Artifact{created.Primary}, created.Artifacts...)
	handles, err := putArtifacts(ctx, deps, artifacts)
	if err != nil {
		return host.Result{}, err
	}
	if len(handles) == 0 {
		return host.Result{}, fmt.Errorf("host.demo.create_mockup: creator returned no mockup")
	}
	receiptRef, err := putReceipt(ctx, deps, "create", key, "create_mockup", true, created.Summary, handles)
	if err != nil {
		return host.Result{}, err
	}
	_ = receiptRef
	return result(map[string]any{"mockup_ref": handles[0], "artifact_handles": handles}), nil
}

func recordOp(ctx context.Context, deps Dependencies, args map[string]any) (host.Result, error) {
	manifestRef, err := refArgs("record", args)
	if err != nil {
		return host.Result{}, err
	}
	record, err := resolveManifest(ctx, deps, manifestRef)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.record: %w", err)
	}
	manifest, err := record.resolve(deps.Root)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.record: %w", err)
	}
	key := semanticDigest("record-receipt", []byte(manifestRef+"\x00"+host.ActorFromContext(ctx)))
	if ref, _, ok, err := deps.Evidence.Find(ctx, "record", key); err != nil {
		return host.Result{}, err
	} else if ok {
		return result(map[string]any{"record_ref": ref}), nil
	}
	captured, err := deps.Capture.Record(ctx, deps.Root, manifest)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.record: capture: %w", err)
	}
	handles, err := putArtifacts(ctx, deps, captured.Artifacts)
	if err != nil {
		return host.Result{}, err
	}
	ref, err := putReceipt(ctx, deps, "record", key, "record", true, captured.Summary, handles)
	if err != nil {
		return host.Result{}, err
	}
	return result(map[string]any{"record_ref": ref}), nil
}

func doctorOp(ctx context.Context, deps Dependencies, args map[string]any) (host.Result, error) {
	manifestRef, err := refArgs("doctor", args)
	if err != nil {
		return host.Result{}, err
	}
	record, err := resolveManifest(ctx, deps, manifestRef)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.doctor: %w", err)
	}
	manifest, err := record.resolve(deps.Root)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.doctor: %w", err)
	}
	fingerprint, err := fileFingerprint(deps.Root, manifest.Path)
	if err != nil {
		return host.Result{}, err
	}
	key := semanticDigest("doctor-receipt", []byte(manifestRef+"\x00"+fingerprint))
	if ref, prior, ok, err := deps.Evidence.Find(ctx, "doctor", key); err != nil {
		return host.Result{}, err
	} else if ok {
		var receipt operationReceipt
		if err := json.Unmarshal(prior, &receipt); err != nil {
			return host.Result{}, err
		}
		report, _ := receipt.Evidence.(map[string]any)
		return result(map[string]any{"report": report, "ok": receipt.OK, "evidence_ref": ref}), nil
	}
	checked, err := deps.Doctor.Check(ctx, deps.Root, manifest)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.doctor: check: %w", err)
	}
	reportRaw, err := json.Marshal(checked.Report)
	if err != nil {
		return host.Result{}, err
	}
	if len(reportRaw) > maxPayloadBytes/2 {
		return host.Result{}, fmt.Errorf("host.demo.doctor: report is %d bytes, exceeds safe boundary; refusing to truncate", len(reportRaw))
	}
	ref, err := putReceipt(ctx, deps, "doctor", key, "doctor", checked.OK, checked.Report, nil)
	if err != nil {
		return host.Result{}, err
	}
	return result(map[string]any{"report": checked.Report, "ok": checked.OK, "evidence_ref": ref}), nil
}

func graphArgs(op string, args map[string]any, extra map[string]bool) (string, string, error) {
	allowed := map[string]bool{"op": true, "catalog_path": true, "node_id": true}
	for key := range extra {
		allowed[key] = true
	}
	if err := rejectUnknown(op, args, allowed); err != nil {
		return "", "", err
	}
	catalogPath, err := requiredString(op, args, "catalog_path", maxCatalogPathBytes)
	if err != nil {
		return "", "", err
	}
	if filepath.IsAbs(catalogPath) || strings.Contains(catalogPath, "://") {
		return "", "", fmt.Errorf("host.demo.%s: catalog_path must be application-relative", op)
	}
	clean := filepath.Clean(catalogPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("host.demo.%s: catalog_path escapes the application scope", op)
	}
	nodeID, err := requiredString(op, args, "node_id", maxNodeIDBytes)
	if err != nil {
		return "", "", err
	}
	return catalogPath, nodeID, nil
}

func refArgs(op string, args map[string]any) (string, error) {
	if err := rejectUnknown(op, args, map[string]bool{"op": true, "manifest_ref": true}); err != nil {
		return "", err
	}
	return requiredString(op, args, "manifest_ref", 512)
}

func rejectUnknown(op string, args map[string]any, allowed map[string]bool) error {
	for key := range args {
		if !allowed[key] {
			return fmt.Errorf("host.demo.%s: unknown argument %q", op, key)
		}
	}
	return nil
}

func requiredString(op string, args map[string]any, key string, max int) (string, error) {
	value, ok := args[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("host.demo.%s: %s must be a non-empty string", op, key)
	}
	if len(value) > max {
		return "", fmt.Errorf("host.demo.%s: %s exceeds %d bytes", op, key, max)
	}
	return value, nil
}

func putJSON(ctx context.Context, deps Dependencies, kind string, value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := semanticDigest(kind, payload)
	return deps.Evidence.Put(ctx, kind, digest, payload, deps.Clock.Now())
}

func putArtifacts(ctx context.Context, deps Dependencies, artifacts []Artifact) ([]string, error) {
	if len(artifacts) > maxArtifacts {
		return nil, fmt.Errorf("provider returned %d artifacts, exceeds %d; refusing to truncate", len(artifacts), maxArtifacts)
	}
	handles := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		metadata, err := artifactMetadata(deps.Root, artifact)
		if err != nil {
			return nil, err
		}
		ref, err := putJSON(ctx, deps, "artifact", metadata)
		if err != nil {
			return nil, err
		}
		handles = append(handles, ref)
	}
	return handles, nil
}

func resolveManifest(ctx context.Context, deps Dependencies, ref string) (manifestRecord, error) {
	payload, err := deps.Evidence.Resolve(ctx, ref, "manifest")
	if err != nil {
		return manifestRecord{}, err
	}
	var record manifestRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return manifestRecord{}, err
	}
	return record, nil
}

func (m manifestRecord) resolve(root string) (Manifest, error) {
	if m.Path != "" {
		path, err := containedExistingPath(root, m.Path)
		if err != nil {
			return Manifest{}, err
		}
		return manifestFromPath(root, path)
	}
	if m.Mockup == nil {
		return Manifest{}, fmt.Errorf("manifest reference has no server-owned target")
	}
	entries, err := filepath.Glob(filepath.Join(m.Mockup.WorkDir, "*.demo.json"))
	if err != nil {
		return Manifest{}, err
	}
	if len(entries) != 1 {
		return Manifest{}, fmt.Errorf("projected mockup has %d demo manifests; expected exactly one", len(entries))
	}
	path, err := containedExistingPath(root, entries[0])
	if err != nil {
		return Manifest{}, err
	}
	return manifestFromPath(root, path)
}

func manifestFromPath(root, path string) (Manifest, error) {
	document, _, err := readNativeManifest(root, path)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{Path: path}
	if document.Capture != nil {
		manifest.Capture = &CapturePlan{
			ApplicationID: document.Capture.ApplicationID,
			ScenarioRef:   document.Capture.ScenarioRef,
			ActionIDs:     append([]string(nil), document.Capture.ActionIDs...),
		}
	}
	return manifest, nil
}

func fileFingerprint(root, path string) (string, error) {
	metadata, err := artifactMetadata(root, Artifact{Kind: "manifest", Path: path})
	if err != nil {
		return "", err
	}
	return metadata.Digest, nil
}

func putReceipt(ctx context.Context, deps Dependencies, kind, key, op string, ok bool, evidence any, artifacts []string) (string, error) {
	receipt := operationReceipt{
		Schema: receiptSchema, AppID: deps.AppID, Operation: op, InputDigest: key,
		OK: ok, Evidence: evidence, Artifacts: artifacts,
		RecordedAt: deps.Clock.Now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	return deps.Evidence.Put(ctx, kind, key, payload, deps.Clock.Now())
}
