package materialize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/effect"
	"kitsoki/internal/graph"
	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
)

var (
	opaqueArtifactHandlePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}:[A-Za-z0-9._~-]+$`)
	receiptIDPattern            = regexp.MustCompile(`^ar_[0-9a-f]{32}$`)
)

const maxOpaqueArtifactHandleLength = 512

// ApplicationPhaseInput is the complete input authority available to a typed
// materialization phase. CatalogRef is an allowlisted alias, not a resolved
// filesystem path.
type ApplicationPhaseInput struct {
	CatalogRef    string `json:"catalog_ref"`
	NodeID        string `json:"node_id"`
	ContextDigest string `json:"context_digest"`
}

// ApplicationPhaseRequest describes one server-authorized application call.
// The executor owns the live session, actor, transport, and registry service.
type ApplicationPhaseRequest struct {
	ApplicationID  string
	Phase          graph.MaterializePhaseDecl
	Input          ApplicationPhaseInput
	IdempotencyKey string
}

// ApplicationPhaseExecutor invokes a phase through the registered application
// service. Implementations must not reinterpret graph-authored values as paths,
// commands, scripts, or URLs.
type ApplicationPhaseExecutor interface {
	ExecuteApplicationPhase(context.Context, ApplicationPhaseRequest) (appplatform.OutcomeEnvelope, error)
}

type applicationDurableWriteback interface {
	AppendEvidence(string, string, EvidenceEntry, string, string) error
	WriteMaterialization(string, string, MaterializationRecord) error
}

type graphApplicationWriteback struct{}

func (graphApplicationWriteback) AppendEvidence(catalogPath, nodeID string, entry EvidenceEntry, jobID, producer string) error {
	return AppendEvidence(catalogPath, nodeID, entry, jobID, producer)
}

func (graphApplicationWriteback) WriteMaterialization(catalogPath, nodeID string, record MaterializationRecord) error {
	return WriteMaterialization(catalogPath, nodeID, record)
}

// ValidateApplicationBinding proves that every phase names an exact exported
// application member before the scheduler accepts the job.
func ValidateApplicationBinding(def *app.AppDef, binding *Binding) error {
	if def == nil {
		return fmt.Errorf("materialize: registered application definition is required")
	}
	if binding == nil || binding.ApplicationID == "" {
		return fmt.Errorf("materialize: typed application binding is required")
	}
	if def.App.ID != binding.ApplicationID {
		return fmt.Errorf("materialize: registered application id %q does not match binding %q", def.App.ID, binding.ApplicationID)
	}
	if def.Exports == nil {
		return fmt.Errorf("materialize: application %q declares no exports", binding.ApplicationID)
	}
	exportedActions := map[string]struct{}{}
	if def.Exports.Application != nil {
		for _, id := range def.Exports.Application.Actions {
			exportedActions[id] = struct{}{}
		}
	}
	for _, phase := range binding.Phases {
		var handlerID string
		switch {
		case phase.Handler != "":
			handlerID = phase.Handler
		case phase.Action != "":
			if def.Application == nil {
				return fmt.Errorf("materialize: phase %q action %q is not declared by application %q", phase.ID, phase.Action, binding.ApplicationID)
			}
			action := def.Application.Actions[phase.Action]
			if action == nil {
				return fmt.Errorf("materialize: phase %q action %q is not declared by application %q", phase.ID, phase.Action, binding.ApplicationID)
			}
			if _, ok := exportedActions[phase.Action]; !ok {
				return fmt.Errorf("materialize: phase %q action %q is not exported by application %q", phase.ID, phase.Action, binding.ApplicationID)
			}
			if action.Intent != "" || action.Handler == "" {
				return fmt.Errorf("materialize: phase %q action %q must be handler-backed", phase.ID, phase.Action)
			}
			handlerID = action.Handler
		default:
			return fmt.Errorf("materialize: phase %q has no operation", phase.ID)
		}
		handler := def.Exports.Handlers[handlerID]
		if handler == nil {
			return fmt.Errorf("materialize: phase %q handler %q is not exported by application %q", phase.ID, handlerID, binding.ApplicationID)
		}
		if !containsString(handler.Expose, string(appplatform.TransportJSONRPC)) {
			return fmt.Errorf("materialize: phase %q handler %q is not exposed over jsonrpc", phase.ID, handlerID)
		}
		if handler.Effect == effect.Write || handler.Effect == effect.External {
			if handler.Idempotency == nil ||
				strings.TrimSpace(handler.Idempotency.Key) == "" ||
				handler.Idempotency.Scope != "application" {
				return fmt.Errorf(
					"materialize: phase %q effectful handler %q requires idempotency with scope application",
					phase.ID,
					handlerID,
				)
			}
		}
	}
	return nil
}

// ApplicationBindingRequiresDurableReplay reports whether any validated phase
// resolves to a write or external handler. Callers use it to fail closed when
// no restart-safe application replay store is available.
func ApplicationBindingRequiresDurableReplay(def *app.AppDef, binding *Binding) bool {
	if def == nil || def.Exports == nil || binding == nil {
		return false
	}
	for _, phase := range binding.Phases {
		handlerID := phase.Handler
		if handlerID == "" && def.Application != nil {
			if action := def.Application.Actions[phase.Action]; action != nil {
				handlerID = action.Handler
			}
		}
		handler := def.Exports.Handlers[handlerID]
		if handler != nil && (handler.Effect == effect.Write || handler.Effect == effect.External) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func phaseIdempotencyKey(applicationID string, nodeID graph.NodeID, phaseID, contextDigest string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"graph-materialize-application/v1",
		applicationID,
		string(nodeID),
		phaseID,
		contextDigest,
	}, "\x00")))
	return "graph-materialize:" + hex.EncodeToString(sum[:])
}

func applicationPhaseOutput(outcome appplatform.OutcomeEnvelope, phase graph.MaterializePhaseDecl) (map[string]string, error) {
	if outcome.Error != nil {
		return nil, fmt.Errorf("materialize: phase %q application error %q: %s", phase.ID, outcome.Error.Code, outcome.Error.Message)
	}
	if outcome.Outcome != "ok" {
		return nil, fmt.Errorf("materialize: phase %q returned outcome %q", phase.ID, outcome.Outcome)
	}
	if !receiptIDPattern.MatchString(outcome.Receipt.ID) {
		return nil, fmt.Errorf("materialize: phase %q returned non-canonical receipt id", phase.ID)
	}
	var output map[string]any
	if err := json.Unmarshal(outcome.Output, &output); err != nil {
		return nil, fmt.Errorf("materialize: phase %q output is not an object: %w", phase.ID, err)
	}
	handles := make(map[string]string, len(phase.ArtifactOutputs))
	for _, field := range phase.ArtifactOutputs {
		handle, ok := output[field].(string)
		if !ok || len(handle) > maxOpaqueArtifactHandleLength ||
			!opaqueArtifactHandlePattern.MatchString(handle) ||
			strings.Contains(handle, "..") || strings.Contains(handle, "://") ||
			strings.ContainsAny(handle, `/\`) {
			return nil, fmt.Errorf("materialize: phase %q output %q is not an opaque artifact handle", phase.ID, field)
		}
		handles[field] = handle
	}
	return handles, nil
}

func driveApplicationHandler(p *Prepared, sched jobs.Scheduler, sessionID string, executor ApplicationPhaseExecutor) host.Handler {
	return func(ctx context.Context, args map[string]any) (host.Result, error) {
		jobID, _ := args["__job_id"].(string)
		statuses := make([]string, len(p.Stages))
		for i := range statuses {
			statuses[i] = "waiting"
		}
		heartbeat := func(i int, status string) {
			statuses[i] = status
			if sched != nil && jobID != "" {
				_ = sched.Heartbeat(jobID, StageEvent{Stage: p.Stages[i], Status: status})
			}
		}
		stageSnapshot := func() []Stage {
			stages := make([]Stage, len(p.Stages))
			for i, id := range p.Stages {
				stages[i] = Stage{ID: id, Status: statuses[i]}
			}
			return stages
		}

		var (
			artifacts []MaterializationArtifact
			receipts  []string
			checks    []CheckResult
		)
		writeback := p.writeback
		if writeback == nil {
			writeback = graphApplicationWriteback{}
		}
		finalize := func(status string) error {
			return writeback.WriteMaterialization(p.Req.CatalogPath, string(p.Req.NodeID), MaterializationRecord{
				JobID:         jobID,
				SessionID:     sessionID,
				Status:        status,
				ApplicationID: p.Binding.ApplicationID,
				Stages:        stageSnapshot(),
				Artifacts:     artifacts,
				Checks:        checks,
				ContextDigest: p.ContextDigest,
				ReceiptIDs:    receipts,
			})
		}
		fail := func(index int, err error) (host.Result, error) {
			heartbeat(index, "failed")
			if writebackErr := finalize("failed"); writebackErr != nil {
				err = fmt.Errorf("%w; persist failed materialization: %v", err, writebackErr)
			}
			handles := make([]string, len(artifacts))
			for i, artifact := range artifacts {
				handles[i] = artifact.Handle
			}
			return host.Result{
				Data: map[string]any{
					"artifact_handles": handles,
					"receipt_ids":      append([]string(nil), receipts...),
					"stages":           stageSnapshot(),
				},
				Error: err.Error(),
			}, nil
		}

		for i, phase := range p.Binding.Phases {
			heartbeat(i, "in-progress")
			outcome, err := executor.ExecuteApplicationPhase(ctx, ApplicationPhaseRequest{
				ApplicationID: p.Binding.ApplicationID,
				Phase:         phase,
				Input: ApplicationPhaseInput{
					CatalogRef:    p.Req.CatalogRef,
					NodeID:        string(p.Req.NodeID),
					ContextDigest: p.ContextDigest,
				},
				IdempotencyKey: phaseIdempotencyKey(p.Binding.ApplicationID, p.Req.NodeID, phase.ID, p.ContextDigest),
			})
			if err != nil {
				return fail(i, fmt.Errorf("materialize: phase %q: %w", phase.ID, err))
			}
			handles, err := applicationPhaseOutput(outcome, phase)
			if err != nil {
				return fail(i, err)
			}
			receipts = append(receipts, outcome.Receipt.ID)
			for _, output := range phase.ArtifactOutputs {
				produced := MaterializationArtifact{
					Kind:       p.Binding.ArtifactKind,
					Title:      artifactTitle(p.Binding.ArtifactSchema),
					Handle:     handles[output],
					PhaseID:    phase.ID,
					Output:     output,
					ProducedAt: time.Now().UTC().Format(time.RFC3339),
				}
				if err := writeback.AppendEvidence(
					p.Req.CatalogPath,
					string(p.Req.NodeID),
					EvidenceEntry{
						Kind: produced.Kind, Title: produced.Title, Handle: produced.Handle,
						PhaseID: produced.PhaseID, Output: produced.Output,
					},
					jobID,
					"application:"+p.Binding.ApplicationID,
				); err != nil {
					return fail(i, fmt.Errorf("materialize: persist phase %q output %q evidence: %w", phase.ID, output, err))
				}
				artifacts = append(artifacts, produced)
			}
			heartbeat(i, "complete")
		}

		for j, check := range p.Checks {
			index := len(p.Binding.Phases) + j
			heartbeat(index, "in-progress")
			result := RunCheck(ctx, p.Req.RepoRoot, check)
			checks = append(checks, result)
			if !result.OK {
				reason := result.Error
				if reason == "" {
					reason = strings.Join(result.Reasons, "; ")
				}
				return fail(index, fmt.Errorf("materialize: gate check %q failed: %s", check.ID, reason))
			}
			heartbeat(index, "complete")
		}

		if err := finalize("complete"); err != nil {
			return fail(len(p.Binding.Phases)-1, fmt.Errorf("materialize: persist completed materialization: %w", err))
		}
		handles := make([]string, len(artifacts))
		for i, artifact := range artifacts {
			handles[i] = artifact.Handle
		}
		return host.Result{Data: map[string]any{
			"artifact_handles": handles,
			"receipt_ids":      append([]string(nil), receipts...),
			"stages":           stageSnapshot(),
		}}, nil
	}
}
