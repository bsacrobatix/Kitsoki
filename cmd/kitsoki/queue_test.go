package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/capsule/queue"
)

func TestQueueProcessDepsDefaultsToStagingIntegration(t *testing.T) {
	deps := queueProcessDeps("/project", "make test", "", "", "", "worker-1")

	integration, ok := deps.Integration.(queue.StagingIntegration)
	require.True(t, ok)
	require.Equal(t, "/project", integration.ProjectRoot)
	require.Equal(t, "make test", integration.GateCommand)
	require.Nil(t, deps.Finalizer)
	require.Nil(t, deps.Repairer)
	require.Equal(t, "worker-1", deps.WorkerID)
}

func TestQueueProcessDepsUsesRequestedProtectedTarget(t *testing.T) {
	deps := queueProcessDeps("/project", "make test", "release/2026.07", "resolve-conflicts", "repair-gate", "worker-1")

	integration, ok := deps.Integration.(queue.ProtectedIntegration)
	require.True(t, ok)
	require.Equal(t, "release/2026.07", integration.TargetRef)
	require.Equal(t, "resolve-conflicts", integration.ResolverCommand)
	finalizer, ok := deps.Finalizer.(queue.ProtectedFinalizer)
	require.True(t, ok)
	require.Equal(t, "release/2026.07", finalizer.TargetRef)
	repairer, ok := deps.Repairer.(queue.ShellRepairer)
	require.True(t, ok)
	require.Equal(t, "repair-gate", repairer.Command)
}
