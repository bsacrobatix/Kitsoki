package campaign

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kitsoki/internal/graph"
)

// CatalogSource loads one operator-configured project graph. AppID is a
// mandatory node field and is fixed by the host wiring, so a story cannot
// discover another application's campaigns by changing host arguments.
type CatalogSource struct {
	Path           string
	TypeID         string
	MaxDefinitions int
	MaxBytes       int
}

func (s CatalogSource) Discover(_ context.Context, appID string) ([]Definition, error) {
	if strings.TrimSpace(appID) == "" {
		return nil, fmt.Errorf("campaign: application id is required")
	}
	if strings.TrimSpace(s.Path) == "" {
		return nil, fmt.Errorf("campaign: graph catalog is not configured")
	}
	path, err := filepath.Abs(s.Path)
	if err != nil {
		return nil, fmt.Errorf("campaign: resolve graph catalog: %w", err)
	}
	catalog, err := graph.LoadCatalog(path)
	if err != nil {
		return nil, fmt.Errorf("campaign: load graph catalog: %w", err)
	}
	typeID := s.TypeID
	if typeID == "" {
		typeID = DefaultTypeID
	}
	maxDefinitions := s.MaxDefinitions
	if maxDefinitions <= 0 {
		maxDefinitions = DefaultMaxDefinitions
	}
	maxBytes := s.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	var out []Definition
	for _, node := range catalog.Nodes {
		if node.TypeID != typeID {
			continue
		}
		nodeAppID, _ := node.Fields["application_id"].(string)
		if nodeAppID != appID {
			continue
		}
		def, err := definitionFromNode(node, appID, catalog.ContentDigest)
		if err != nil {
			return nil, err
		}
		out = append(out, def)
		if len(out) > maxDefinitions {
			return nil, fmt.Errorf(
				"campaign: app %q selected more than %d definitions; refusing to truncate",
				appID, maxDefinitions,
			)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("campaign: encode discovery size check: %w", err)
	}
	if len(encoded) > maxBytes {
		return nil, fmt.Errorf(
			"campaign: app %q definitions encode to %d bytes, exceeding %d; refusing to truncate",
			appID, len(encoded), maxBytes,
		)
	}
	return out, nil
}

func definitionFromNode(node *graph.Node, appID, sourceDigest string) (Definition, error) {
	prefix := fmt.Sprintf("campaign: node %q", node.ID)
	if node.Status != "active" {
		return Definition{}, fmt.Errorf("%s status must be active, got %q", prefix, node.Status)
	}
	enabled, err := requiredBool(node.Fields, "enabled")
	if err != nil {
		return Definition{}, fmt.Errorf("%s: %w", prefix, err)
	}
	paused, err := optionalBool(node.Fields, "paused")
	if err != nil {
		return Definition{}, fmt.Errorf("%s: %w", prefix, err)
	}
	cadenceSeconds, err := requiredPositiveInt(node.Fields, "cadence_seconds")
	if err != nil {
		return Definition{}, fmt.Errorf("%s: %w", prefix, err)
	}
	budgetMap, err := requiredObject(node.Fields, "budget")
	if err != nil {
		return Definition{}, fmt.Errorf("%s: %w", prefix, err)
	}
	maxTicks, err := requiredPositiveInt(budgetMap, "max_ticks_per_day")
	if err != nil {
		return Definition{}, fmt.Errorf("%s budget: %w", prefix, err)
	}
	maxConcurrency, err := requiredPositiveInt(budgetMap, "max_concurrency")
	if err != nil {
		return Definition{}, fmt.Errorf("%s budget: %w", prefix, err)
	}
	actionMap, err := requiredObject(node.Fields, "action")
	if err != nil {
		return Definition{}, fmt.Errorf("%s: %w", prefix, err)
	}
	kind, _ := actionMap["kind"].(string)
	if kind != "story-intent" {
		return Definition{}, fmt.Errorf("%s action.kind must be %q, got %q", prefix, "story-intent", kind)
	}
	story, err := requiredString(actionMap, "story")
	if err != nil {
		return Definition{}, fmt.Errorf("%s action: %w", prefix, err)
	}
	intent, err := requiredString(actionMap, "intent")
	if err != nil {
		return Definition{}, fmt.Errorf("%s action: %w", prefix, err)
	}
	input := map[string]any{}
	if raw, ok := actionMap["input"]; ok {
		input, ok = stringObject(raw)
		if !ok {
			return Definition{}, fmt.Errorf("%s action.input must be an object, got %T", prefix, raw)
		}
	}
	def := Definition{
		ID:           string(node.ID),
		AppID:        appID,
		Title:        node.Title,
		Enabled:      enabled,
		Paused:       paused,
		Cadence:      time.Duration(cadenceSeconds) * time.Second,
		Budget:       Budget{MaxTicksPerDay: maxTicks, MaxConcurrency: maxConcurrency},
		Action:       Action{Story: story, Intent: intent, Input: input},
		SourceDigest: sourceDigest,
	}
	hashBytes, err := json.Marshal(definitionHashInput(def))
	if err != nil {
		return Definition{}, fmt.Errorf("%s: encode definition digest: %w", prefix, err)
	}
	sum := sha256.Sum256(hashBytes)
	def.DefinitionHash = hex.EncodeToString(sum[:])
	return def, nil
}

func definitionHashInput(def Definition) any {
	return struct {
		ID           string
		AppID        string
		Enabled      bool
		Paused       bool
		CadenceNanos int64
		Budget       Budget
		Action       Action
	}{
		ID: def.ID, AppID: def.AppID, Enabled: def.Enabled, Paused: def.Paused,
		CadenceNanos: int64(def.Cadence), Budget: def.Budget, Action: def.Action,
	}
}

func requiredObject(values map[string]any, key string) (map[string]any, error) {
	raw, ok := values[key]
	if !ok {
		return nil, fmt.Errorf("%s is required", key)
	}
	value, ok := stringObject(raw)
	if !ok {
		return nil, fmt.Errorf("%s must be an object, got %T", key, raw)
	}
	return value, nil
}

func stringObject(raw any) (map[string]any, bool) {
	switch value := raw.(type) {
	case map[string]any:
		return value, true
	case map[any]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			name, ok := key.(string)
			if !ok {
				return nil, false
			}
			out[name] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func requiredString(values map[string]any, key string) (string, error) {
	value, ok := values[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", key)
	}
	return value, nil
}

func requiredBool(values map[string]any, key string) (bool, error) {
	value, ok := values[key].(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a bool", key)
	}
	return value, nil
}

func optionalBool(values map[string]any, key string) (bool, error) {
	raw, ok := values[key]
	if !ok {
		return false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a bool", key)
	}
	return value, nil
}

func requiredPositiveInt(values map[string]any, key string) (int, error) {
	raw, ok := values[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	var value int
	switch typed := raw.(type) {
	case int:
		value = typed
	case int64:
		if int64(int(typed)) != typed {
			return 0, fmt.Errorf("%s is outside the supported integer range", key)
		}
		value = int(typed)
	case float64:
		if math.Trunc(typed) != typed || typed > float64(math.MaxInt) || typed < float64(math.MinInt) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		value = int(typed)
	default:
		return 0, fmt.Errorf("%s must be an integer, got %T", key, raw)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %d", key, value)
	}
	return value, nil
}
