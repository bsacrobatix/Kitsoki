package host

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRegisterPendingSeedFromTaskArgs_ItemManifest proves the auto-registration
// path host.agent.task drives before spawning the maker: a task whose
// context.args carry the punch-list item manifest (item.{story, world_in})
// registers a pending seed keyed by the parent session id (read from ctx), which
// the studio server then consumes for exactly that story. No LLM / no subprocess.
func TestRegisterPendingSeedFromTaskArgs_ItemManifest(t *testing.T) {
	t.Setenv("KITSOKI_PENDING_SEED_DIR", t.TempDir())
	const parentSID = "goal-seeker-1"

	story := "stories/bugfix/app.yaml"
	world := map[string]any{"ticket_id": "0.2", "ticket_title": "Fix the thing"}
	args := map[string]any{
		"agent": "driver",
		"context": map[string]any{
			"prompt": "prompts/drive_item.md",
			"args": map[string]any{
				"item": map[string]any{
					"story":    story,
					"world_in": world,
				},
			},
		},
	}

	// The parent session id rides on ctx (WithKitsokiSessionID), exactly as
	// agent-serve sets it per-RPC — not the process-global env.
	ctx := WithKitsokiSessionID(context.Background(), parentSID)
	RegisterPendingSeedFromTaskArgs(ctx, args)

	got, ok := TakePendingSeed(parentSID, story)
	require.True(t, ok, "a pending seed must be registered from item.{story, world_in}")
	require.Equal(t, "0.2", got["ticket_id"], "period-id ticket must survive verbatim")
	require.Equal(t, "Fix the thing", got["ticket_title"])

	// Consume-once: a second take on the same key finds nothing.
	_, ok2 := TakePendingSeed(parentSID, story)
	require.False(t, ok2, "the seed is consume-once")
}

// TestRegisterPendingSeedFromTaskArgs_NoSeedIsNoop confirms the guardrails: no
// session lineage, no story, or no world all leave the store empty (today's
// behaviour), so an ordinary task without a seed is unaffected.
func TestRegisterPendingSeedFromTaskArgs_NoSeedIsNoop(t *testing.T) {
	t.Setenv("KITSOKI_PENDING_SEED_DIR", t.TempDir())

	// No session id on ctx ⇒ no-op even with a full item.
	args := map[string]any{
		"context": map[string]any{
			"args": map[string]any{
				"item": map[string]any{
					"story":    "stories/bugfix/app.yaml",
					"world_in": map[string]any{"ticket_id": "0.2"},
				},
			},
		},
	}
	RegisterPendingSeedFromTaskArgs(context.Background(), args)
	_, ok := TakePendingSeed("", "stories/bugfix/app.yaml")
	require.False(t, ok)

	// Session id present but no world ⇒ still a no-op.
	ctx := WithKitsokiSessionID(context.Background(), "sid-x")
	RegisterPendingSeedFromTaskArgs(ctx, map[string]any{
		"context": map[string]any{
			"args": map[string]any{
				"item": map[string]any{"story": "stories/bugfix/app.yaml"},
			},
		},
	})
	_, ok = TakePendingSeed("sid-x", "stories/bugfix/app.yaml")
	require.False(t, ok, "no world_in ⇒ nothing registered")
}

// TestRegisterPendingSeedFromTaskArgs_FlatShape covers the generic fallback
// shape (context.args.{story, world_in}) for a task that is not the punch-list
// item manifest.
func TestRegisterPendingSeedFromTaskArgs_FlatShape(t *testing.T) {
	t.Setenv("KITSOKI_PENDING_SEED_DIR", t.TempDir())
	const sid = "sid-flat"
	story := "stories/delivery/app.yaml"
	args := map[string]any{
		"context": map[string]any{
			"args": map[string]any{
				"story":         story,
				"initial_world": map[string]any{"ticket_id": "9"},
			},
		},
	}
	RegisterPendingSeedFromTaskArgs(WithKitsokiSessionID(context.Background(), sid), args)
	got, ok := TakePendingSeed(sid, story)
	require.True(t, ok)
	require.Equal(t, "9", got["ticket_id"])
}

// TestTakePendingSeed_SessionlessConsumerFallsBack is the codex path: the parent
// registers a seed tagged with its own session id, but the maker's studio server
// is a codex-spawned `kitsoki mcp` that never inherited KITSOKI_SESSION_ID, so it
// consumes with an EMPTY session id. The oldest-fallback must still hand it the
// seed — otherwise every gpt-5.5/codex maker strands with an empty ticket_id.
func TestTakePendingSeed_SessionlessConsumerFallsBack(t *testing.T) {
	t.Setenv("KITSOKI_PENDING_SEED_DIR", t.TempDir())
	story := "stories/implementation/app.yaml"
	require.NoError(t, RegisterPendingSeed("goal-seeker-7", story, map[string]any{"ticket_id": "0.2"}))

	got, ok := TakePendingSeed("", story) // codex consumer: no forwarded session id
	require.True(t, ok, "a session-less consumer must still resolve the story's seed")
	require.Equal(t, "0.2", got["ticket_id"])

	_, ok2 := TakePendingSeed("", story)
	require.False(t, ok2, "still consume-once")
}

// TestTakePendingSeed_PrefersMatchingLineage proves a session-aware consumer
// (claude/GLM maker whose env forwarded the parent id) picks its OWN seed even
// when another parent's seed for the same story sits ahead of it in the FIFO —
// preserving cross-parent isolation the story-only key would otherwise lose.
func TestTakePendingSeed_PrefersMatchingLineage(t *testing.T) {
	t.Setenv("KITSOKI_PENDING_SEED_DIR", t.TempDir())
	story := "stories/implementation/app.yaml"
	require.NoError(t, RegisterPendingSeed("parent-A", story, map[string]any{"ticket_id": "A"}))
	require.NoError(t, RegisterPendingSeed("parent-B", story, map[string]any{"ticket_id": "B"}))

	// Consumer B matches the second (younger) entry despite A being oldest.
	got, ok := TakePendingSeed("parent-B", story)
	require.True(t, ok)
	require.Equal(t, "B", got["ticket_id"], "must prefer the lineage-matching entry, not the oldest")

	// A's seed is untouched and still resolvable.
	gotA, okA := TakePendingSeed("parent-A", story)
	require.True(t, okA)
	require.Equal(t, "A", gotA["ticket_id"])
}
