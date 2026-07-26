package host

// The integration-train story deliberately owns no credentials or shell
// access. This file provides its small, durable authority boundary: a project
// installs one explicit command per phase, and the host persists the exact
// request/evidence pair before returning it to the story. Repeated turns read
// the stored evidence rather than invoking an effect again.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kitsoki/internal/atomicfile"
)

// IntegrationTrainAuthorityConfigEnv points at a local, operator-owned JSON
// configuration file. Keeping this outside story input prevents an untrusted
// run from selecting an executable, state root, or deployment authority.
const IntegrationTrainAuthorityConfigEnv = "KITSOKI_INTEGRATION_TRAIN_AUTHORITY_CONFIG"

const integrationTrainAuthoritySchema = "kitsoki/integration-train-authority-config/v1"

type integrationTrainAuthorityConfig struct {
	Schema    string                                    `json:"schema"`
	StateRoot string                                    `json:"state_root"`
	Phases    map[string]integrationTrainAuthorityPhase `json:"phases"`
}

type integrationTrainAuthorityPhase struct {
	Command []string `json:"command"`
	Timeout string   `json:"timeout,omitempty"`
}

type integrationTrainRequest struct {
	Phase      string         `json:"phase"`
	Job        map[string]any `json:"job"`
	Checkpoint map[string]any `json:"checkpoint"`
}

// IntegrationTrainCommandRunner is deliberately injectable so the durable
// authority contract is fully testable without executing a project command.
type IntegrationTrainCommandRunner func(context.Context, []string, []byte) ([]byte, error)

// IntegrationTrainAuthority is a concrete, command-backed implementation of
// host.integration_train. Commands receive the exact request JSON on stdin and
// must emit one integration-train authority evidence JSON document on stdout.
// They are expected to use the normal Capsule/queue/deploy APIs; this carrier
// never grants a story arbitrary shell text.
type IntegrationTrainAuthority struct {
	Config integrationTrainAuthorityConfig
	Run    IntegrationTrainCommandRunner
}

// IntegrationTrainHandler is the built-in host handler. An unconfigured host
// returns structured needs_input evidence, rather than an opaque missing-host
// failure or a fabricated delivery result.
func IntegrationTrainHandler(ctx context.Context, args map[string]any) (Result, error) {
	request, err := integrationTrainRequestFromArgs(args)
	if err != nil {
		return Result{Error: "host.integration_train: " + err.Error()}, nil
	}
	configPath := strings.TrimSpace(os.Getenv(IntegrationTrainAuthorityConfigEnv))
	if configPath == "" {
		return Result{Data: map[string]any{"evidence": integrationTrainNeedsInput(request, "no effectful integration-train authority is configured")}}, nil
	}
	authority, err := loadIntegrationTrainAuthority(configPath)
	if err != nil {
		return Result{Data: map[string]any{"evidence": integrationTrainNeedsInput(request, err.Error())}}, nil
	}
	evidence, err := authority.Reconcile(ctx, request)
	if err != nil {
		return Result{Data: map[string]any{"evidence": integrationTrainNeedsInput(request, err.Error())}}, nil
	}
	return Result{Data: map[string]any{"evidence": evidence}}, nil
}

func integrationTrainRequestFromArgs(args map[string]any) (integrationTrainRequest, error) {
	raw, ok := args["request"].(map[string]any)
	if !ok {
		return integrationTrainRequest{}, fmt.Errorf("request object is required")
	}
	phase, _ := raw["phase"].(string)
	job, _ := raw["job"].(map[string]any)
	checkpoint, _ := raw["checkpoint"].(map[string]any)
	phase = strings.TrimSpace(phase)
	if !integrationTrainPhaseValid(phase) || job == nil {
		return integrationTrainRequest{}, fmt.Errorf("request must contain a supported phase and job")
	}
	return integrationTrainRequest{Phase: phase, Job: job, Checkpoint: checkpoint}, nil
}

func loadIntegrationTrainAuthority(path string) (IntegrationTrainAuthority, error) {
	if !filepath.IsAbs(path) {
		return IntegrationTrainAuthority{}, fmt.Errorf("authority config path must be absolute")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return IntegrationTrainAuthority{}, fmt.Errorf("read authority config: %w", err)
	}
	var config integrationTrainAuthorityConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return IntegrationTrainAuthority{}, fmt.Errorf("parse authority config: %w", err)
	}
	if config.Schema != integrationTrainAuthoritySchema || !filepath.IsAbs(config.StateRoot) {
		return IntegrationTrainAuthority{}, fmt.Errorf("authority config must declare schema %s and an absolute state_root", integrationTrainAuthoritySchema)
	}
	for phase, spec := range config.Phases {
		if !integrationTrainPhaseValid(phase) || len(spec.Command) == 0 || !filepath.IsAbs(spec.Command[0]) {
			return IntegrationTrainAuthority{}, fmt.Errorf("authority config phase %q must use an absolute command", phase)
		}
		if spec.Timeout != "" {
			if _, err := time.ParseDuration(spec.Timeout); err != nil {
				return IntegrationTrainAuthority{}, fmt.Errorf("authority config phase %q timeout: %w", phase, err)
			}
		}
	}
	return IntegrationTrainAuthority{Config: config, Run: runIntegrationTrainCommand}, nil
}

// Reconcile returns cached evidence for the exact sealed request. It never
// overwrites a record: a changed checkpoint gets a new content-addressed
// request record, while a retried identical request gets the original receipt.
func (a IntegrationTrainAuthority) Reconcile(ctx context.Context, request integrationTrainRequest) (map[string]any, error) {
	if err := integrationTrainRequestValid(request); err != nil {
		return nil, err
	}
	if a.Run == nil {
		a.Run = runIntegrationTrainCommand
	}
	spec, ok := a.Config.Phases[request.Phase]
	if !ok {
		return integrationTrainNeedsInput(request, "no authority command is configured for phase "+request.Phase), nil
	}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode authority request: %w", err)
	}
	digest := integrationTrainDigest(rawRequest)
	dir := filepath.Join(a.Config.StateRoot, "integration-train", integrationTrainSafePath(request.trainID()), request.Phase)
	path := filepath.Join(dir, strings.TrimPrefix(digest, "sha256:")+".json")
	if raw, err := os.ReadFile(path); err == nil {
		var record integrationTrainAuthorityRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, fmt.Errorf("read cached authority evidence: %w", err)
		}
		if record.RequestDigest != digest || record.Evidence == nil {
			return nil, fmt.Errorf("cached authority evidence digest mismatch")
		}
		return record.Evidence, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read cached authority evidence: %w", err)
	}

	callCtx := ctx
	if spec.Timeout != "" {
		timeout, _ := time.ParseDuration(spec.Timeout)
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	out, err := a.Run(callCtx, append([]string(nil), spec.Command...), rawRequest)
	if err != nil {
		return nil, fmt.Errorf("authority command for %s: %w", request.Phase, err)
	}
	var evidence map[string]any
	if err := json.Unmarshal(out, &evidence); err != nil {
		return nil, fmt.Errorf("authority command for %s returned invalid JSON: %w", request.Phase, err)
	}
	if err := integrationTrainEvidenceMatches(request, evidence); err != nil {
		return nil, err
	}
	record := integrationTrainAuthorityRecord{Schema: "kitsoki/integration-train-authority-record/v1", RequestDigest: digest, Evidence: evidence}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode authority evidence: %w", err)
	}
	if err := atomicfile.WriteFile(path, append(encoded, '\n'), 0o600, 0o755); err != nil {
		return nil, fmt.Errorf("persist authority evidence: %w", err)
	}
	return evidence, nil
}

type integrationTrainAuthorityRecord struct {
	Schema        string         `json:"schema"`
	RequestDigest string         `json:"request_digest"`
	Evidence      map[string]any `json:"evidence"`
}

func runIntegrationTrainCommand(ctx context.Context, command []string, input []byte) ([]byte, error) {
	if len(command) == 0 || !filepath.IsAbs(command[0]) {
		return nil, fmt.Errorf("authority command must be absolute")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin = strings.NewReader(string(input))
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func integrationTrainRequestValid(request integrationTrainRequest) error {
	if !integrationTrainPhaseValid(request.Phase) || request.trainID() == "" || request.manifestDigest() == "" {
		return fmt.Errorf("authority request has invalid phase, train_id, or manifest_digest")
	}
	manifest, _ := request.Job["manifest"].(map[string]any)
	computed, err := integrationTrainManifestDigest(manifest)
	if err != nil {
		return fmt.Errorf("authority request manifest seal: %w", err)
	}
	if request.manifestDigest() != computed {
		return fmt.Errorf("authority request manifest_digest does not match canonical manifest: got %s want %s", request.manifestDigest(), computed)
	}
	return nil
}

// integrationTrainManifestDigest is the canonical seal shared with manifest
// assemblers. The digest input is compact UTF-8 JSON of the manifest with
// manifest_digest omitted, object keys recursively ordered lexicographically,
// and array order preserved. encoding/json already orders map keys; disabling
// HTML escaping keeps the bytes canonical UTF-8 rather than spelling <, >, and
// & as optional escape sequences.
func integrationTrainManifestDigest(manifest map[string]any) (string, error) {
	if manifest == nil {
		return "", fmt.Errorf("manifest object is required")
	}
	unsigned := make(map[string]any, len(manifest)-1)
	for key, value := range manifest {
		if key != "manifest_digest" {
			unsigned[key] = value
		}
	}
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(unsigned); err != nil {
		return "", fmt.Errorf("encode canonical manifest: %w", err)
	}
	return integrationTrainDigest(bytes.TrimSuffix(raw.Bytes(), []byte{'\n'})), nil
}

func integrationTrainEvidenceMatches(request integrationTrainRequest, evidence map[string]any) error {
	if evidence["schema"] != "kitsoki/integration-train-authority/v1" || evidence["phase"] != request.Phase || evidence["train_id"] != request.trainID() || evidence["manifest_digest"] != request.manifestDigest() {
		return fmt.Errorf("authority evidence does not match sealed request")
	}
	if _, ok := evidence["checkpoint"].(map[string]any); !ok {
		return fmt.Errorf("authority evidence is missing checkpoint")
	}
	if _, ok := evidence["status"].(string); !ok {
		return fmt.Errorf("authority evidence is missing status")
	}
	return nil
}

func integrationTrainNeedsInput(request integrationTrainRequest, message string) map[string]any {
	sequence := 1
	previous := ""
	if prior := request.Checkpoint; prior != nil {
		if n, ok := integrationTrainSequence(prior["sequence"]); ok && n >= 1 {
			sequence = n + 1
		}
		previous, _ = prior["checkpoint_digest"].(string)
	}
	checkpoint := map[string]any{"schema": "kitsoki/integration-train-checkpoint/v1", "train_id": request.trainID(), "manifest_digest": request.manifestDigest(), "phase": request.Phase, "sequence": sequence, "previous_digest": previous, "evidence": map[string]any{"error": message}}
	checkpoint["checkpoint_digest"] = integrationTrainDigest(mustJSON(checkpoint))
	return map[string]any{"schema": "kitsoki/integration-train-authority/v1", "phase": request.Phase, "train_id": request.trainID(), "manifest_digest": request.manifestDigest(), "status": "needs_input", "error": message, "checkpoint": checkpoint}
}

func integrationTrainSequence(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), n == float64(int(n))
	default:
		return 0, false
	}
}

func (r integrationTrainRequest) trainID() string {
	v, _ := r.Job["train_id"].(string)
	return strings.TrimSpace(v)
}
func (r integrationTrainRequest) manifestDigest() string {
	manifest, _ := r.Job["manifest"].(map[string]any)
	v, _ := manifest["manifest_digest"].(string)
	return strings.TrimSpace(v)
}

func integrationTrainPhaseValid(phase string) bool {
	switch phase {
	case "validate", "integrate", "gate", "staging", "main", "deploy":
		return true
	default:
		return false
	}
}

func integrationTrainDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func integrationTrainSafePath(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mustJSON(value any) []byte { raw, _ := json.Marshal(value); return raw }
