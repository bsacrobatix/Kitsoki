package storydemo

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	if err := rejectUnknown("plan", args, map[string]bool{"op": true, "node_id": true}); err != nil {
		return host.Result{}, err
	}
	nodeID, err := requiredString("plan", args, "node_id", maxNodeIDBytes)
	if err != nil {
		return host.Result{}, err
	}
	if strings.TrimSpace(deps.CatalogPath) == "" {
		return host.Result{}, fmt.Errorf("host.demo.plan: daemon artifact binding is not configured")
	}
	plan, err := deps.Resolver.Plan(ctx, deps.Root, deps.CatalogPath, nodeID)
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
	if deps.Artifacts == nil {
		return host.Result{}, fmt.Errorf("host.demo.materialize: application artifact executor is unavailable outside daemon mode")
	}
	phase, err := requiredString("materialize", args, "phase", 32)
	if err != nil {
		return host.Result{}, err
	}
	if phase != "dependencies" && phase != "subject" && phase != "verify" {
		return host.Result{}, fmt.Errorf("host.demo.materialize: phase must be dependencies, subject, or verify")
	}
	if err := rejectUnknown("materialize", args, map[string]bool{
		"op": true, "node_id": true, "phase": true,
	}); err != nil {
		return host.Result{}, err
	}
	nodeID, err := requiredString("materialize", args, "node_id", maxNodeIDBytes)
	if err != nil {
		return host.Result{}, err
	}
	if strings.TrimSpace(deps.CatalogPath) == "" || strings.TrimSpace(deps.CatalogRef) == "" {
		return host.Result{}, fmt.Errorf("host.demo.materialize: daemon artifact binding is not configured")
	}
	materialization, err := deps.Resolver.Materialization(
		ctx,
		deps.Root,
		deps.CatalogPath,
		nodeID,
		phase,
	)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.materialize: resolve: %w", err)
	}
	contextDigest, err := materializationDigest(deps.Root, materialization)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.materialize: context: %w", err)
	}
	executed, err := deps.Artifacts.ExecuteApplicationArtifact(ctx, ApplicationArtifactRequest{
		CallerApplicationID: deps.AppID,
		Operation:           "materialize." + phase,
		Input: ApplicationArtifactInput{
			Schema:     "kitsoki/story-application-materialize-input/v1",
			CatalogRef: deps.CatalogRef, NodeID: nodeID, ContextDigest: contextDigest,
		},
	})
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.materialize: execute: %w", err)
	}
	if len(executed.Artifacts) == 0 {
		return host.Result{}, fmt.Errorf("host.demo.materialize: producer returned no artifact handles")
	}
	key := semanticDigest("materialize-receipt", []byte(strings.Join([]string{
		deps.CatalogRef, nodeID, phase, contextDigest,
	}, "\x00")))
	ref, err := putApplicationReceipt(
		ctx,
		deps,
		"materialize",
		key,
		"materialize."+phase,
		executed,
	)
	if err != nil {
		return host.Result{}, err
	}
	return result(map[string]any{
		"evidence_ref": ref, "artifact_handles": append([]string(nil), executed.Artifacts...),
	}), nil
}

func projectMockupOp(ctx context.Context, deps Dependencies, args map[string]any) (host.Result, error) {
	audience, err := requiredString("project_mockup", args, "audience", maxAudienceBytes)
	if err != nil {
		return host.Result{}, err
	}
	if audience != "internal" && audience != "public" {
		return host.Result{}, fmt.Errorf("host.demo.project_mockup: audience must be internal or public")
	}
	if err := rejectUnknown("project_mockup", args, map[string]bool{
		"op": true, "node_id": true, "audience": true,
	}); err != nil {
		return host.Result{}, err
	}
	nodeID, err := requiredString("project_mockup", args, "node_id", maxNodeIDBytes)
	if err != nil {
		return host.Result{}, err
	}
	if strings.TrimSpace(deps.CatalogPath) == "" {
		return host.Result{}, fmt.Errorf("host.demo.project_mockup: daemon artifact binding is not configured")
	}
	projection, err := deps.Resolver.ProjectMockup(ctx, deps.Root, deps.CatalogPath, nodeID, audience)
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.project_mockup: resolve: %w", err)
	}
	scenarioRef, err := putJSON(ctx, deps, "scenario", projection.Scenario)
	if err != nil {
		return host.Result{}, err
	}
	if deps.MockupApplicationID == "" {
		return host.Result{}, fmt.Errorf("host.demo.project_mockup: no producer application is configured")
	}
	projection.Manifest.ApplicationID = deps.MockupApplicationID
	projection.Manifest.ScenarioRef = scenarioRef
	manifestRef, err := putJSON(ctx, deps, "manifest", manifestRecord{Mockup: &projection.Manifest})
	if err != nil {
		return host.Result{}, err
	}
	return result(map[string]any{"scenario_ref": scenarioRef, "manifest_ref": manifestRef}), nil
}

func createMockupOp(ctx context.Context, deps Dependencies, args map[string]any) (host.Result, error) {
	if deps.Artifacts == nil {
		return host.Result{}, fmt.Errorf("host.demo.create_mockup: application artifact executor is unavailable outside daemon mode")
	}
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
	scenarioDigest := semanticDigest("scenario", record.Mockup.Scenario)
	created, err := deps.Artifacts.ExecuteApplicationArtifact(ctx, ApplicationArtifactRequest{
		CallerApplicationID: deps.AppID,
		Operation:           "create_mockup",
		Input: ApplicationArtifactInput{
			Schema:         "kitsoki/story-application-mockup-input/v1",
			ScenarioRef:    record.Mockup.ScenarioRef,
			ScenarioDigest: scenarioDigest,
			ActionIDs:      append([]string(nil), record.Mockup.ActionIDs...),
		},
	})
	if err != nil {
		return host.Result{}, fmt.Errorf("host.demo.create_mockup: execute: %w", err)
	}
	if created.Primary == "" || created.Bundle == "" || len(created.Artifacts) == 0 {
		return host.Result{}, fmt.Errorf("host.demo.create_mockup: producer returned incomplete mockup and bundle handles")
	}
	key := semanticDigest("create-receipt", []byte(manifestRef))
	_, err = putApplicationReceipt(ctx, deps, "create", key, "create_mockup", created)
	if err != nil {
		return host.Result{}, err
	}
	return result(map[string]any{
		"mockup_ref":       created.Primary,
		"bundle_ref":       created.Bundle,
		"artifact_handles": append([]string(nil), created.Artifacts...),
	}), nil
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
	fingerprint, err := manifestFingerprint(deps.Root, manifest)
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

func putApplicationReceipt(
	ctx context.Context,
	deps Dependencies,
	kind string,
	key string,
	operation string,
	executed ApplicationArtifactResult,
) (string, error) {
	receipt := operationReceipt{
		Schema: receiptSchema, AppID: deps.AppID, Operation: operation,
		InputDigest: key, OK: true,
		Artifacts:  append([]string(nil), executed.Artifacts...),
		Receipts:   append([]string(nil), executed.ReceiptIDs...),
		Bundle:     executed.Bundle,
		RecordedAt: deps.Clock.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	return deps.Evidence.Put(ctx, kind, key, raw, deps.Clock.Now())
}

func materializationDigest(root string, materialization Materialization) (string, error) {
	type boundedArtifact struct {
		Kind   string `json:"kind"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
	}
	type boundedTask struct {
		ID        string            `json:"id"`
		Phase     string            `json:"phase"`
		Artifacts []boundedArtifact `json:"artifacts"`
	}
	if len(materialization.Tasks) > maxTasks {
		return "", fmt.Errorf("task count exceeds %d", maxTasks)
	}
	tasks := make([]boundedTask, 0, len(materialization.Tasks))
	for _, task := range materialization.Tasks {
		bounded := boundedTask{ID: task.ID, Phase: task.Phase}
		for _, artifact := range task.Artifacts {
			metadata, err := artifactMetadata(root, artifact)
			if err != nil {
				return "", err
			}
			bounded.Artifacts = append(bounded.Artifacts, boundedArtifact{
				Kind: metadata.Kind, Size: metadata.Size, Digest: metadata.Digest,
			})
		}
		tasks = append(tasks, bounded)
	}
	raw, err := json.Marshal(struct {
		CatalogDigest string        `json:"catalog_digest"`
		NodeID        string        `json:"node_id"`
		Phase         string        `json:"phase"`
		Tasks         []boundedTask `json:"tasks"`
	}{
		CatalogDigest: materialization.CatalogDigest,
		NodeID:        materialization.NodeID,
		Phase:         materialization.Phase,
		Tasks:         tasks,
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + semanticDigest("materialization-context", raw), nil
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
	if strings.TrimSpace(m.Mockup.ApplicationID) == "" {
		return Manifest{}, fmt.Errorf("projected mockup has no producer application")
	}
	if strings.TrimSpace(m.Mockup.ScenarioRef) == "" {
		return Manifest{}, fmt.Errorf("projected mockup has no scenario reference")
	}
	if len(m.Mockup.ActionIDs) > maxTasks {
		return Manifest{}, fmt.Errorf("projected mockup has %d actions, exceeds %d", len(m.Mockup.ActionIDs), maxTasks)
	}
	return Manifest{Capture: &CapturePlan{
		ApplicationID: m.Mockup.ApplicationID,
		ScenarioRef:   m.Mockup.ScenarioRef,
		ActionIDs:     append([]string(nil), m.Mockup.ActionIDs...),
	}}, nil
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

func manifestFingerprint(root string, manifest Manifest) (string, error) {
	if manifest.Path != "" {
		return fileFingerprint(root, manifest.Path)
	}
	if manifest.Capture == nil {
		return "", fmt.Errorf("manifest has neither a native path nor a capture plan")
	}
	raw, err := json.Marshal(manifest.Capture)
	if err != nil {
		return "", err
	}
	return "sha256:" + semanticDigest("capture-plan", raw), nil
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
