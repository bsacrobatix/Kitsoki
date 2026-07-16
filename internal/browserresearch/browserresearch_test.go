package browserresearch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"kitsoki/internal/study"
)

func setup(t *testing.T) (*study.MemoryStore, study.Study, study.Attempt) {
	t.Helper()
	s := study.NewMemoryStore()
	st, _, err := s.Submit(context.Background(), study.SubmitRequest{IdempotencyKey: "one", Plan: study.Plan{Revision: "r", Digest: "sha256:p", Waves: []study.Wave{{ID: "one", Order: 1, Cells: []study.CellPlan{{ID: "cell"}}}}}})
	require.NoError(t, err)
	snap, err := s.Get(context.Background(), st.ID)
	require.NoError(t, err)
	return s, st, snap.Cells[0].Attempts[0]
}
func TestFixtureWorkerProducesRedactedDigestBundle(t *testing.T) {
	s, st, a := setup(t)
	w := Worker{Store: s, Now: func() time.Time { return time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC) }, Transport: FixtureTransport{"https://public.example/reports": {Content: "report token=secret", Interactive: true, RRWeb: "<html>token=secret</html>", Chapters: []string{"open", "capture"}, Poster: "poster.png"}}}
	b, err := w.Execute(context.Background(), Request{StudyID: st.ID, WaveID: "one", CellID: "cell", AttemptID: a.ID, URL: "https://public.example/reports", ApprovedRoots: []ApprovedRoot{{URL: "https://public.example/"}}})
	require.NoError(t, err)
	require.True(t, b.Ready)
	require.NotEmpty(t, b.Source.ContentDigest)
	require.True(t, b.Redaction.Applied)
	require.NotEmpty(t, b.Redaction.RRWebDigest)
}
func TestFixtureWorkerKeepsAccessFailureTyped(t *testing.T) {
	s, st, a := setup(t)
	w := Worker{Store: s, Transport: FixtureTransport{"https://public.example/private": {Failure: FailureAccess}}}
	b, err := w.Execute(context.Background(), Request{StudyID: st.ID, WaveID: "one", CellID: "cell", AttemptID: a.ID, URL: "https://public.example/private", ApprovedRoots: []ApprovedRoot{{URL: "https://public.example/"}}})
	require.NoError(t, err)
	require.False(t, b.Ready)
	require.Equal(t, FailureAccess, b.Failure)
	snap, err := s.Get(context.Background(), st.ID)
	require.NoError(t, err)
	require.Equal(t, study.CellFailed, snap.Cells[0].Phase)
}
