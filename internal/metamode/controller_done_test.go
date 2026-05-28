package metamode

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestController_Done_ArchivesAndReturnsID drives the happy path:
// Enter to get a session, Done returns the chat ID, and the fake
// store records the archive call.
func TestController_Done_ArchivesAndReturnsID(t *testing.T) {
	c, store, _ := newTestController(t)
	s, err := c.Enter(context.Background(), makeSnapshot("foyer"), "story")
	require.NoError(t, err)
	require.NotEmpty(t, s.Chat.ID())

	id, err := c.Done(context.Background(), s)
	require.NoError(t, err)
	require.Equal(t, s.Chat.ID(), id, "Done must return the archived chat's id")
	require.Equal(t, []string{s.Chat.ID()}, store.archivedIDs,
		"the fake store must have observed exactly one ArchiveMeta call for this chat")
}

// TestController_Done_NilSession rejects nil-session callers — Done
// is only meaningful with an active Session.
func TestController_Done_NilSession(t *testing.T) {
	c, _, _ := newTestController(t)
	_, err := c.Done(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no active session")
}

// TestController_Done_NilControllerSafe asserts the typical
// defensive guards on the receiver.
func TestController_Done_NilControllerSafe(t *testing.T) {
	var c *Controller
	_, err := c.Done(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil controller")
}
