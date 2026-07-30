package appdef_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/appdef"
)

func TestMemStorage_PutIdempotentAndConflict(t *testing.T) {
	ctx := context.Background()
	store := appdef.NewMemStorage()

	rev := appdef.Revision{
		Schema:    appdef.RevisionSchema,
		Digest:    "sha256:aaaa",
		Entry:     "app.yaml",
		Files:     map[string][]byte{"app.yaml": []byte("v1")},
		Source:    appdef.SourceCapture,
		CreatedAt: time.Unix(100, 0),
	}

	require.NoError(t, store.Put(ctx, rev))

	got, ok, err := store.Get(ctx, rev.Digest)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, rev.Files, got.Files)
	require.Equal(t, rev.Digest, got.Digest)

	// Putting the SAME digest with byte-identical content again is a no-op
	// success — idempotent for identical content.
	require.NoError(t, store.Put(ctx, rev))

	// Putting the SAME digest with DIFFERENT content is refused: a digest
	// collision with different bytes is a bug or an attack, never an
	// overwrite.
	conflicting := rev
	conflicting.Files = map[string][]byte{"app.yaml": []byte("v2 - different bytes")}
	err = store.Put(ctx, conflicting)
	require.ErrorIs(t, err, appdef.ErrDigestConflict)

	// The conflict must not have mutated the stored revision.
	still, ok, err := store.Get(ctx, rev.Digest)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, rev.Files, still.Files)

	// Get of the mutated copy must not affect the store either: mutating
	// what Get returned must not leak back into storage.
	got.Files["app.yaml"][0] = 'X'
	still2, ok, err := store.Get(ctx, rev.Digest)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []byte("v1"), still2.Files["app.yaml"])

	// Unknown digest.
	_, ok, err = store.Get(ctx, "sha256:does-not-exist")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestMemStorage_ListOrdersNewestFirstAndFiltersByApp(t *testing.T) {
	ctx := context.Background()
	store := appdef.NewMemStorage()

	base := time.Unix(1000, 0)
	revs := []appdef.Revision{
		{Digest: "sha256:1", AppID: "app-a", Entry: "app.yaml", Files: map[string][]byte{"app.yaml": []byte("1")}, CreatedAt: base},
		{Digest: "sha256:2", AppID: "app-b", Entry: "app.yaml", Files: map[string][]byte{"app.yaml": []byte("2")}, CreatedAt: base}, // same CreatedAt as #1: tie broken by insertion order
		{Digest: "sha256:3", AppID: "app-a", Entry: "app.yaml", Files: map[string][]byte{"app.yaml": []byte("3")}, CreatedAt: base.Add(time.Second)},
	}
	for _, r := range revs {
		require.NoError(t, store.Put(ctx, r))
	}

	all, err := store.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, all, 3)
	// Newest CreatedAt first; among equal CreatedAt, most-recently-inserted
	// first.
	require.Equal(t, "sha256:3", all[0].Digest)
	require.Equal(t, "sha256:2", all[1].Digest)
	require.Equal(t, "sha256:1", all[2].Digest)

	onlyA, err := store.List(ctx, "app-a")
	require.NoError(t, err)
	require.Len(t, onlyA, 2)
	require.Equal(t, "sha256:3", onlyA[0].Digest)
	require.Equal(t, "sha256:1", onlyA[1].Digest)
}
