package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ApplicationGraphWriteRead    = "read"
	ApplicationGraphWritePropose = "propose"
	ApplicationGraphWriteSteward = "steward"
	MaxApplicationGraphNodes     = 10_000
	MaxApplicationGraphBytes     = 8 << 20
	applicationGraphActorPrefix  = "kitsoki.application:"
)

type ApplicationGraphBinding struct {
	ApplicationID string
	ProjectRoot   string
	CatalogPath   string
	OverlayPath   string
	MaxNodes      int
	MaxBytes      int
	WritePolicy   string
}

type ApplicationGraphHandlers struct {
	Prefix     Handler
	Operations map[string]Handler
}

func ValidApplicationGraphWritePolicy(policy string) bool {
	return policy == ApplicationGraphWriteRead ||
		policy == ApplicationGraphWritePropose ||
		policy == ApplicationGraphWriteSteward
}

// NewApplicationGraphHandlers closes over all graph authority selected during
// session construction. Exact leaf handlers fix the operation as well as the
// paths; the prefix fails closed so args.op cannot retarget a public call.
func NewApplicationGraphHandlers(binding ApplicationGraphBinding) (ApplicationGraphHandlers, error) {
	if !applicationGraphIdentity(binding.ApplicationID) {
		return ApplicationGraphHandlers{}, fmt.Errorf("application graph binding requires an opaque application id")
	}
	if !filepath.IsAbs(binding.ProjectRoot) || !filepath.IsAbs(binding.CatalogPath) {
		return ApplicationGraphHandlers{}, fmt.Errorf("application graph binding requires resolved server paths")
	}
	if binding.OverlayPath != "" && !filepath.IsAbs(binding.OverlayPath) {
		return ApplicationGraphHandlers{}, fmt.Errorf("application graph binding requires a resolved overlay path")
	}
	if binding.MaxNodes < 1 || binding.MaxNodes > MaxApplicationGraphNodes {
		return ApplicationGraphHandlers{}, fmt.Errorf("application graph binding max_nodes is outside its bound")
	}
	if binding.MaxBytes < 1 || binding.MaxBytes > MaxApplicationGraphBytes {
		return ApplicationGraphHandlers{}, fmt.Errorf("application graph binding max_bytes is outside its bound")
	}
	if !ValidApplicationGraphWritePolicy(binding.WritePolicy) {
		return ApplicationGraphHandlers{}, fmt.Errorf("application graph binding write policy is invalid")
	}

	core := func(ctx context.Context, args map[string]any) (Result, error) {
		raw, err := json.Marshal(args)
		if err != nil {
			return Result{}, fmt.Errorf("host.graph: application input must be JSON-compatible")
		}
		if len(raw) > binding.MaxBytes {
			return Result{}, fmt.Errorf("host.graph: application input exceeds configured max_bytes")
		}
		var input map[string]any
		if err := json.Unmarshal(raw, &input); err != nil {
			return Result{}, fmt.Errorf("host.graph: normalize application input: %w", err)
		}
		if err := rejectApplicationGraphAuthority(input, "input"); err != nil {
			return Result{}, err
		}
		op, _ := input["op"].(string)
		allowed, ok := applicationGraphPublicInputs[op]
		if !ok {
			return Result{}, fmt.Errorf("host.graph: operation %q is not exposed to Story Applications", op)
		}
		for key := range input {
			if key != "op" && !allowed[key] {
				return Result{}, fmt.Errorf("host.graph.%s: input %q is not part of the application contract", op, key)
			}
		}
		if !applicationGraphOperationAllowed(binding.WritePolicy, op, graphBoolArg(input, "dry_run")) {
			return Result{}, fmt.Errorf(
				"host.graph.%s: configured write policy %q does not authorize this operation",
				op, binding.WritePolicy,
			)
		}
		if err := validateApplicationGraphFile(binding.ProjectRoot, binding.CatalogPath, binding.MaxBytes); err != nil {
			return Result{}, fmt.Errorf("host.graph.%s: configured catalog is unavailable: %w", op, err)
		}
		input["catalog_path"] = binding.CatalogPath
		if binding.OverlayPath != "" && applicationGraphReadOperation(op) {
			if err := validateApplicationGraphFile(binding.ProjectRoot, binding.OverlayPath, binding.MaxBytes); err != nil {
				return Result{}, fmt.Errorf("host.graph.%s: configured overlay is unavailable: %w", op, err)
			}
			input["overlay_path"] = binding.OverlayPath
		}
		if op == "snapshot" {
			input["max_nodes"] = binding.MaxNodes
		}

		ctx = WithActor(ctx, applicationGraphActorPrefix+binding.ApplicationID)
		if binding.WritePolicy == ApplicationGraphWriteSteward {
			ctx = WithSteward(ctx, true)
		}
		result, err := GraphHandler(ctx, input)
		if err != nil {
			return Result{}, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return Result{}, fmt.Errorf("host.graph.%s: encode bounded result: %w", op, err)
		}
		if len(encoded) > binding.MaxBytes {
			return Result{}, fmt.Errorf("host.graph.%s: result exceeds configured max_bytes", op)
		}
		return result, nil
	}
	handlers := ApplicationGraphHandlers{
		Prefix: func(context.Context, map[string]any) (Result, error) {
			return Result{}, fmt.Errorf("host.graph: configured Story Application must invoke an exposed operation")
		},
		Operations: make(map[string]Handler, len(applicationGraphPublicInputs)),
	}
	for op := range applicationGraphPublicInputs {
		fixedOp := op
		handlers.Operations[fixedOp] = func(ctx context.Context, args map[string]any) (Result, error) {
			if _, supplied := args["op"]; supplied {
				return Result{}, fmt.Errorf("host.graph.%s: input %q carries forbidden caller authority", fixedOp, "op")
			}
			bound := make(map[string]any, len(args)+1)
			for key, value := range args {
				bound[key] = value
			}
			bound["op"] = fixedOp
			return core(ctx, bound)
		}
	}
	return handlers, nil
}

var applicationGraphPublicInputs = map[string]map[string]bool{
	"snapshot": {
		"audience": true, "fields": true,
	},
	"get": {
		"ids": true, "fields": true,
	},
	"changeset": {
		"action": true, "changeset_id": true, "node_id": true,
	},
	"project": {
		"graph_id": true,
	},
	"propose": {
		"title": true, "operations": true, "visibility": true, "validate_only": true,
	},
	"authorize": {
		"changeset_id": true,
	},
	"withdraw": {
		"changeset_id": true,
	},
	"rebase": {
		"changeset_id": true,
	},
	"apply": {
		"changeset_id": true, "dry_run": true,
	},
}

func applicationGraphReadOperation(op string) bool {
	return op == "snapshot" || op == "get" || op == "changeset" || op == "project"
}

func applicationGraphOperationAllowed(policy, op string, dryRun bool) bool {
	if applicationGraphReadOperation(op) {
		return true
	}
	switch policy {
	case ApplicationGraphWritePropose:
		return op == "propose" || op == "withdraw" || op == "rebase" ||
			(op == "apply" && dryRun)
	case ApplicationGraphWriteSteward:
		return op == "propose" || op == "authorize" || op == "withdraw" ||
			op == "rebase" || op == "apply"
	default:
		return false
	}
}

func rejectApplicationGraphAuthority(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			if applicationGraphAuthorityKey(normalized) {
				return fmt.Errorf("host.graph: %s.%s carries forbidden caller authority", path, key)
			}
			if err := rejectApplicationGraphAuthority(child, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := rejectApplicationGraphAuthority(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func applicationGraphAuthorityKey(key string) bool {
	switch key {
	case "catalog", "catalog_path", "overlay", "overlay_path", "project_root",
		"url", "command", "cmd", "provider", "profile", "actor",
		"session", "session_id", "transport":
		return true
	}
	return strings.HasSuffix(key, "_url") ||
		strings.HasSuffix(key, "_command") ||
		strings.HasSuffix(key, "_provider") ||
		strings.HasSuffix(key, "_profile") ||
		strings.HasSuffix(key, "_actor") ||
		strings.HasSuffix(key, "_session") ||
		strings.HasSuffix(key, "_session_id") ||
		strings.HasSuffix(key, "_transport")
}

func validateApplicationGraphFile(root, path string, maxBytes int) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	pathReal, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootReal, pathReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("resolved file escapes configured project root")
	}
	info, err := os.Stat(pathReal)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("resolved path is not a regular file")
	}
	if info.Size() > int64(maxBytes) {
		return fmt.Errorf("configured file exceeds max_bytes")
	}
	return nil
}

func applicationGraphIdentity(value string) bool {
	if value == "" || len(value) > 180 || value != strings.TrimSpace(value) {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == ':':
		default:
			return false
		}
	}
	return true
}
