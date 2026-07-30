package host

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"kitsoki/internal/environment"
)

// EnvironmentHandler is the Story adapter for the provider-neutral environment
// boundary. The builtin is deliberately unavailable until session construction
// injects an operator-configured reader/controller; it never constructs a
// provider from Story arguments.
func EnvironmentHandler(ctx context.Context, args map[string]any) (Result, error) {
	return NewEnvironmentHandler(nil, nil)(ctx, args)
}

func NewEnvironmentHandler(plans environment.PlanInputReader, controller environment.Host) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		op, _ := args["op"].(string)
		switch op {
		case "plan_inputs":
			if plans == nil {
				return unavailableEnvironmentResult(), nil
			}
			inputs, err := plans.PlanInputs(ctx)
			if err != nil {
				return Result{}, fmt.Errorf("host.environment.plan_inputs: %w", err)
			}
			return Result{Data: planInputsData(inputs)}, nil
		case "read":
			if controller == nil {
				return unavailableEnvironmentResult(), nil
			}
			resourceRef, _ := args["resource_ref"].(string)
			out, err := controller.Read(ctx, environment.ReadRequest{ResourceRef: resourceRef})
			if err != nil {
				return Result{}, fmt.Errorf("host.environment.read: %w", err)
			}
			return Result{Data: map[string]any{"outcome": outcomeData(out.Outcome), "resource": resourceData(out.Resource)}}, nil
		case "submit", "poll", "verify":
			// These operations have typed Go methods now but intentionally no loose
			// map decoder yet. A provider adapter must be wired with its concrete
			// topology action vocabulary before it becomes Story-callable.
			if controller == nil {
				return unavailableEnvironmentResult(), nil
			}
			return Result{Error: "host.environment: operation is not configured for this session"}, nil
		default:
			return Result{Error: fmt.Sprintf("host.environment: unknown op %q (want plan_inputs, read, submit, poll, or verify)", op)}, nil
		}
	}
}

// NewEnvironmentPlanInputsHandler binds plan-input reads to the repository
// containing the invoking Story. Request paths are relative to that fixed
// root: a Story can choose a bundle within its own repository, but cannot
// redirect the host to another checkout or to the operator's filesystem.
//
// This handler deliberately wires only plan_inputs. It grants no provider,
// remote-execution, process, or credential authority.
func NewEnvironmentPlanInputsHandler(storyRepoRoot string) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		if op, _ := args["op"].(string); op != "plan_inputs" {
			return NewEnvironmentHandler(nil, nil)(ctx, args)
		}
		loader, err := profileBundleLoaderForRequest(storyRepoRoot, args)
		if err != nil {
			outcome := environment.Blocked(environment.ReasonInvalidRequest, "environment plan-input paths must stay within the invoking Story repository", "provide relative profile_root and integrity_manifest paths beneath the Story repository", environment.Evidence{Kind: "environment_plan_inputs", Detail: err.Error()})
			return Result{Data: planInputsData(environment.PlanInputs{Integrity: outcome}), Error: outcome.Reason.Message}, nil
		}
		return NewEnvironmentHandler(loader, nil)(ctx, args)
	}
}

func profileBundleLoaderForRequest(storyRepoRoot string, args map[string]any) (environment.ProfileBundleLoader, error) {
	root, err := filepath.Abs(strings.TrimSpace(storyRepoRoot))
	if err != nil {
		return environment.ProfileBundleLoader{}, fmt.Errorf("resolve Story repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return environment.ProfileBundleLoader{}, fmt.Errorf("resolve Story repository root symlinks: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return environment.ProfileBundleLoader{}, fmt.Errorf("stat Story repository root: %w", err)
	}
	if !info.IsDir() {
		return environment.ProfileBundleLoader{}, fmt.Errorf("Story repository root is not a directory")
	}
	profileRoot, err := environmentPlanRelativePath(root, args, "profile_root")
	if err != nil {
		return environment.ProfileBundleLoader{}, err
	}
	manifest, err := environmentPlanRelativePath(root, args, "integrity_manifest")
	if err != nil {
		return environment.ProfileBundleLoader{}, err
	}
	return environment.ProfileBundleLoader{ProfileRoot: profileRoot, DigestFile: manifest, FS: os.DirFS(root)}, nil
}

func environmentPlanRelativePath(root string, args map[string]any, field string) (string, error) {
	raw, ok := args[field].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	raw = strings.TrimSpace(raw)
	if filepath.IsAbs(raw) || path.IsAbs(filepath.ToSlash(raw)) {
		return "", fmt.Errorf("%s must be relative", field)
	}
	clean := path.Clean(filepath.ToSlash(raw))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || !fs.ValidPath(clean) {
		return "", fmt.Errorf("%s escapes the Story repository", field)
	}
	// os.DirFS rejects lexical traversal but follows symlinks. Reject a resolved
	// request path that leaves the Story repository before handing it to the
	// loader. A not-yet-existing path remains a normal loader-level not-found
	// result rather than a path-validation error.
	if resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(clean))); err == nil {
		rel, relErr := filepath.Rel(root, resolved)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return "", fmt.Errorf("%s resolves outside the Story repository", field)
		}
	}
	return clean, nil
}

func unavailableEnvironmentResult() Result {
	outcome := environment.Blocked(environment.ReasonUnavailable, "environment host is unavailable outside a configured deployment session", "configure an environment plan-input reader or provider adapter")
	return Result{Data: map[string]any{"outcome": outcomeData(outcome)}, Error: outcome.Reason.Message}
}

func planInputsData(inputs environment.PlanInputs) map[string]any {
	documents := make([]any, 0, len(inputs.ProfileDocuments))
	for _, profile := range inputs.ProfileDocuments {
		documents = append(documents, map[string]any{"source": map[string]any{"path": profile.Source.Path, "digest": profile.Source.Digest}, "document": profile.Document})
	}
	return map[string]any{
		"profile_documents": documents,
		"integrity":         map[string]any{"outcome": integrityOutcome(inputs.Integrity), "verified": inputs.Integrity.Status == environment.OutcomePassed, "manifest_digest": inputs.ManifestDigest, "diagnostic": outcomeData(inputs.Integrity)},
		"observations": map[string]any{
			"dns":        map[string]any{"resolved_ips": stringsData(inputs.Observations.DNS.ResolvedIPs)},
			"deployment": map[string]any{"healthy": inputs.Observations.Deployment.Healthy, "current": inputs.Observations.Deployment.Current},
			"migration":  map[string]any{"source_configured": inputs.Observations.Migration.SourceConfigured},
		},
	}
}

func integrityOutcome(outcome environment.Outcome) string {
	if outcome.Status == environment.OutcomePassed {
		return "verified"
	}
	return "refused"
}

func outcomeData(outcome environment.Outcome) map[string]any {
	evidence := make([]any, 0, len(outcome.Evidence))
	for _, item := range outcome.Evidence {
		evidence = append(evidence, map[string]any{"kind": item.Kind, "ref": item.Ref, "detail": item.Detail})
	}
	return map[string]any{"status": string(outcome.Status), "reason": map[string]any{"code": string(outcome.Reason.Code), "message": outcome.Reason.Message, "recovery": outcome.Reason.Recovery}, "evidence": evidence}
}

func resourceData(value environment.Resource) map[string]any {
	return map[string]any{"ref": value.Ref, "kind": value.Kind, "healthy": value.Healthy, "current": value.Current, "attributes": value.Attributes}
}
func stringsData(values []string) []any {
	copy := append([]string(nil), values...)
	sort.Strings(copy)
	result := make([]any, len(copy))
	for index, value := range copy {
		result[index] = value
	}
	return result
}
