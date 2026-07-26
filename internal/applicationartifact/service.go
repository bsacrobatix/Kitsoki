// Package applicationartifact executes bounded artifact-producing operations
// through an exact registered Story Application.
package applicationartifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/effect"
)

const (
	MaxPhases       = 32
	MaxOutputs      = 16
	MaxInputBytes   = 256 * 1024
	MaxHandleBytes  = 512
	MaxIdentityByte = 256
)

var (
	identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	handlePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}:[A-Za-z0-9._~-]+$`)
	receiptPattern  = regexp.MustCompile(`^ar_[0-9a-f]{32}$`)
)

// Binding is deployment-owned authority for one artifact operation.
type Binding struct {
	ApplicationID string  `yaml:"application_id" json:"application_id"`
	Phases        []Phase `yaml:"phases" json:"phases"`
	PrimaryOutput string  `yaml:"primary_output,omitempty" json:"primary_output,omitempty"`
	Bundle        bool    `yaml:"bundle,omitempty" json:"bundle,omitempty"`
}

// Phase selects one exact exported handler or handler-backed action.
type Phase struct {
	ID              string   `yaml:"id" json:"id"`
	Handler         string   `yaml:"handler,omitempty" json:"handler,omitempty"`
	Action          string   `yaml:"action,omitempty" json:"action,omitempty"`
	ArtifactOutputs []string `yaml:"artifact_outputs" json:"artifact_outputs"`
}

// Request carries only server-constructed typed input. Actor, session,
// transport, idempotency keys, and the binding are Service-owned.
type Request struct {
	CallerApplicationID string
	Operation           string
	Input               json.RawMessage
}

// Result intentionally excludes raw application output and runtime locations.
type Result struct {
	ReceiptIDs []string
	Artifacts  map[string]string
	Primary    string
}

// Service binds an exact application definition and live application service.
type Service struct {
	Definition  *app.AppDef
	Application appplatform.Service
	SessionID   string
	Actor       string
	Binding     Binding
}

// ValidateBinding verifies deployment authority against the exact registered
// application before any phase is invoked.
func ValidateBinding(def *app.AppDef, binding Binding) error {
	if def == nil {
		return fmt.Errorf("application artifact: registered application definition is required")
	}
	if err := boundedIdentity("application_id", binding.ApplicationID); err != nil {
		return err
	}
	if def.App.ID != binding.ApplicationID {
		return fmt.Errorf(
			"application artifact: registered application id %q does not match binding %q",
			def.App.ID,
			binding.ApplicationID,
		)
	}
	if def.Exports == nil {
		return fmt.Errorf("application artifact: application %q declares no exports", binding.ApplicationID)
	}
	if len(binding.Phases) == 0 || len(binding.Phases) > MaxPhases {
		return fmt.Errorf("application artifact: phase count %d is outside 1..%d", len(binding.Phases), MaxPhases)
	}
	exportedActions := map[string]struct{}{}
	if def.Exports.Application != nil {
		for _, actionID := range def.Exports.Application.Actions {
			exportedActions[actionID] = struct{}{}
		}
	}
	phaseIDs := map[string]struct{}{}
	outputs := map[string]struct{}{}
	for _, phase := range binding.Phases {
		if err := boundedIdentity("phase id", phase.ID); err != nil {
			return err
		}
		if _, exists := phaseIDs[phase.ID]; exists {
			return fmt.Errorf("application artifact: duplicate phase id %q", phase.ID)
		}
		phaseIDs[phase.ID] = struct{}{}
		handlerID, err := phaseHandler(def, binding.ApplicationID, exportedActions, phase)
		if err != nil {
			return err
		}
		handler := def.Exports.Handlers[handlerID]
		if handler == nil {
			return fmt.Errorf(
				"application artifact: phase %q handler %q is not exported by application %q",
				phase.ID,
				handlerID,
				binding.ApplicationID,
			)
		}
		if !contains(handler.Expose, string(appplatform.TransportJSONRPC)) {
			return fmt.Errorf("application artifact: phase %q handler %q is not exposed over jsonrpc", phase.ID, handlerID)
		}
		if handler.Effect == effect.Write || handler.Effect == effect.External {
			if handler.Idempotency == nil ||
				strings.TrimSpace(handler.Idempotency.Key) == "" ||
				handler.Idempotency.Scope != "application" {
				return fmt.Errorf(
					"application artifact: phase %q effectful handler %q requires idempotency with scope application",
					phase.ID,
					handlerID,
				)
			}
		}
		if len(phase.ArtifactOutputs) == 0 || len(phase.ArtifactOutputs) > MaxOutputs {
			return fmt.Errorf(
				"application artifact: phase %q output count %d is outside 1..%d",
				phase.ID,
				len(phase.ArtifactOutputs),
				MaxOutputs,
			)
		}
		for _, output := range phase.ArtifactOutputs {
			if err := boundedIdentity("artifact output", output); err != nil {
				return err
			}
			if _, exists := outputs[output]; exists {
				return fmt.Errorf("application artifact: duplicate artifact output %q", output)
			}
			outputs[output] = struct{}{}
		}
	}
	if binding.PrimaryOutput != "" {
		if _, ok := outputs[binding.PrimaryOutput]; !ok {
			return fmt.Errorf(
				"application artifact: primary output %q is not declared by a phase",
				binding.PrimaryOutput,
			)
		}
	}
	return nil
}

// Execute invokes every validated phase and retains only declared opaque
// handles and canonical receipts.
func (s Service) Execute(ctx context.Context, request Request) (Result, error) {
	if err := ValidateBinding(s.Definition, s.Binding); err != nil {
		return Result{}, err
	}
	if s.Application.Registry == nil {
		return Result{}, fmt.Errorf("application artifact: application service is unavailable")
	}
	if strings.TrimSpace(s.SessionID) == "" || strings.TrimSpace(s.Actor) == "" {
		return Result{}, fmt.Errorf("application artifact: server session and actor are required")
	}
	if err := boundedIdentity("caller application id", request.CallerApplicationID); err != nil {
		return Result{}, err
	}
	if err := boundedIdentity("operation", request.Operation); err != nil {
		return Result{}, err
	}
	input, err := appplatform.NormalizeJSON(request.Input)
	if err != nil {
		return Result{}, err
	}
	if len(input) > MaxInputBytes {
		return Result{}, fmt.Errorf(
			"application artifact: input is %d bytes, exceeds %d; refusing to truncate",
			len(input),
			MaxInputBytes,
		)
	}
	inputDigest, err := appplatform.DigestJSON(input)
	if err != nil {
		return Result{}, err
	}
	result := Result{Artifacts: map[string]string{}}
	for _, phase := range s.Binding.Phases {
		handlerID, err := resolvedPhaseHandler(s.Definition, phase)
		if err != nil {
			return Result{}, err
		}
		outcome, err := s.Application.Call(ctx, appplatform.TransportJSONRPC, appplatform.CallRequest{
			Handler:        handlerID,
			Input:          input,
			SessionID:      s.SessionID,
			Actor:          s.Actor,
			IdempotencyKey: phaseKey(request, s.Binding.ApplicationID, phase.ID, inputDigest),
		})
		if err != nil {
			return Result{}, fmt.Errorf("application artifact: phase %q: %w", phase.ID, err)
		}
		if outcome.Error != nil {
			return Result{}, fmt.Errorf(
				"application artifact: phase %q application error %q: %s",
				phase.ID,
				outcome.Error.Code,
				outcome.Error.Message,
			)
		}
		if outcome.Outcome != "ok" {
			return Result{}, fmt.Errorf("application artifact: phase %q returned outcome %q", phase.ID, outcome.Outcome)
		}
		if outcome.Receipt.Schema != appplatform.ReceiptSchema || !receiptPattern.MatchString(outcome.Receipt.ID) {
			return Result{}, fmt.Errorf("application artifact: phase %q returned a non-canonical receipt", phase.ID)
		}
		var output map[string]any
		if err := json.Unmarshal(outcome.Output, &output); err != nil {
			return Result{}, fmt.Errorf("application artifact: phase %q output is not an object: %w", phase.ID, err)
		}
		for _, field := range phase.ArtifactOutputs {
			handle, ok := output[field].(string)
			if !ok || !validHandle(handle) {
				return Result{}, fmt.Errorf(
					"application artifact: phase %q output %q is not an opaque artifact handle",
					phase.ID,
					field,
				)
			}
			result.Artifacts[field] = handle
		}
		result.ReceiptIDs = append(result.ReceiptIDs, outcome.Receipt.ID)
	}
	if s.Binding.PrimaryOutput != "" {
		result.Primary = result.Artifacts[s.Binding.PrimaryOutput]
	}
	return result, nil
}

func phaseHandler(
	def *app.AppDef,
	applicationID string,
	exportedActions map[string]struct{},
	phase Phase,
) (string, error) {
	if (phase.Handler == "") == (phase.Action == "") {
		return "", fmt.Errorf("application artifact: phase %q must declare exactly one handler or action", phase.ID)
	}
	if phase.Handler != "" {
		if err := boundedIdentity("handler", phase.Handler); err != nil {
			return "", err
		}
		return phase.Handler, nil
	}
	if err := boundedIdentity("action", phase.Action); err != nil {
		return "", err
	}
	if _, ok := exportedActions[phase.Action]; !ok {
		return "", fmt.Errorf(
			"application artifact: phase %q action %q is not exported by application %q",
			phase.ID,
			phase.Action,
			applicationID,
		)
	}
	if def.Application == nil || def.Application.Actions[phase.Action] == nil {
		return "", fmt.Errorf(
			"application artifact: phase %q action %q is not declared by application %q",
			phase.ID,
			phase.Action,
			applicationID,
		)
	}
	action := def.Application.Actions[phase.Action]
	if action.Intent != "" || action.Handler == "" {
		return "", fmt.Errorf("application artifact: phase %q action %q must be handler-backed", phase.ID, phase.Action)
	}
	return action.Handler, nil
}

func resolvedPhaseHandler(def *app.AppDef, phase Phase) (string, error) {
	if phase.Handler != "" {
		return phase.Handler, nil
	}
	if def == nil || def.Application == nil || def.Application.Actions[phase.Action] == nil {
		return "", fmt.Errorf("application artifact: phase %q action is unavailable", phase.ID)
	}
	return def.Application.Actions[phase.Action].Handler, nil
}

func phaseKey(request Request, applicationID, phaseID, inputDigest string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"story-application-artifact/v1",
		request.CallerApplicationID,
		request.Operation,
		applicationID,
		phaseID,
		inputDigest,
	}, "\x00")))
	return "application-artifact:" + hex.EncodeToString(sum[:])
}

func boundedIdentity(label, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxIdentityByte || !identityPattern.MatchString(value) {
		return fmt.Errorf("application artifact: %s is empty, malformed, or exceeds %d bytes", label, MaxIdentityByte)
	}
	return nil
}

func validHandle(handle string) bool {
	return len(handle) <= MaxHandleBytes &&
		handlePattern.MatchString(handle) &&
		!strings.Contains(handle, "..") &&
		!strings.Contains(handle, "://") &&
		!strings.ContainsAny(handle, `/\`)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
