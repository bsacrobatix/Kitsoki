package storydemo

import (
	"context"
	"fmt"
	"strings"

	"kitsoki/internal/applicationcapture"
	"kitsoki/internal/host"
)

func (c BrokerCapture) Record(ctx context.Context, _ string, manifest Manifest) (ToolResult, error) {
	if c.Broker == nil {
		return ToolResult{}, applicationcapture.ErrNoSurface
	}
	if manifest.Capture == nil {
		return ToolResult{}, fmt.Errorf("demo manifest has no typed application capture plan")
	}
	sessionID := strings.TrimSpace(host.KitsokiSessionIDFromCtx(ctx))
	actor := strings.TrimSpace(host.ActorFromContext(ctx))
	if sessionID == "" || actor == "" {
		return ToolResult{}, fmt.Errorf("application capture requires authenticated session and actor context")
	}
	if strings.TrimSpace(manifest.Capture.ApplicationID) == "" {
		return ToolResult{}, fmt.Errorf("demo manifest capture plan has no target application_id")
	}
	captured, err := c.Broker.Request(ctx, manifest.Capture.ApplicationID, sessionID, actor, applicationcapture.Plan{
		ScenarioRef: manifest.Capture.ScenarioRef,
		ActionIDs:   append([]string(nil), manifest.Capture.ActionIDs...),
	}, manifest.Path)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{
		Primary:   Artifact{Kind: "rrweb", Path: captured.ArtifactPath},
		Artifacts: []Artifact{{Kind: "rrweb", Path: captured.ArtifactPath}},
		Summary:   captured.ArtifactRef,
	}, nil
}
