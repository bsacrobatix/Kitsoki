package appdef_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/appdef"
)

func TestGate_ReloadRefusedWhileTurnInFlight(t *testing.T) {
	g := appdef.NewGate()

	g.BeginTurn()
	require.Equal(t, 1, g.TurnsActive())

	err := g.BeginReload()
	require.ErrorIs(t, err, appdef.ErrTurnInFlight)

	g.EndTurn()
	require.Equal(t, 0, g.TurnsActive())

	require.NoError(t, g.BeginReload())
	g.EndReload()
}

func TestGate_ReloadRefusedWhileAnotherReloadInProgress(t *testing.T) {
	g := appdef.NewGate()

	require.NoError(t, g.BeginReload())
	err := g.BeginReload()
	require.ErrorIs(t, err, appdef.ErrReloadInProgress)

	g.EndReload()
	require.NoError(t, g.BeginReload())
	g.EndReload()
}

// TestGate_TurnWaitsForReload proves the asymmetric design: a turn BLOCKS
// on an in-progress swap instead of being refused, and proceeds the moment
// the swap ends.
func TestGate_TurnWaitsForReload(t *testing.T) {
	g := appdef.NewGate()
	require.NoError(t, g.BeginReload())

	proceeded := make(chan struct{})
	go func() {
		g.BeginTurn()
		close(proceeded)
	}()

	select {
	case <-proceeded:
		t.Fatal("BeginTurn returned while a reload was still in progress")
	case <-time.After(100 * time.Millisecond):
	}

	g.EndReload()

	select {
	case <-proceeded:
	case <-time.After(2 * time.Second):
		t.Fatal("BeginTurn did not proceed after EndReload")
	}

	g.EndTurn()
}

// TestGate_ZeroValueUsable proves a zero-value Gate (no NewGate call) works,
// per the doc comment's promise that an embedder never has to remember it.
func TestGate_ZeroValueUsable(t *testing.T) {
	var g appdef.Gate
	g.BeginTurn()
	g.EndTurn()
	require.NoError(t, g.BeginReload())
	g.EndReload()
}
