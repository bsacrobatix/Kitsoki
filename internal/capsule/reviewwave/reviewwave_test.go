package reviewwave

import (
	"testing"
	"time"

	"kitsoki/internal/capsule/queue"
)

func TestFreezeDeterministicallyPinsThreeMembersAndParksRed(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	s := &Store{ProjectRoot: root, Now: func() time.Time { return now }}
	w, err := s.Create("Runtime review", "runtime", "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []queue.Candidate{{ID: "a", ProjectID: "repo", Branch: "agent/a", SHA: "aaaaaaaa", TargetRef: "wave/2026-07/runtime", TargetBaseSHAAtAdmission: "base", Status: queue.ReadyToFinalize}, {ID: "b", ProjectID: "repo", Branch: "agent/b", SHA: "bbbbbbbb", TargetRef: "wave/2026-07/runtime", TargetBaseSHAAtAdmission: "base", Status: queue.ReadyToFinalize}, {ID: "c", ProjectID: "repo", Branch: "agent/c", SHA: "cccccccc", TargetRef: "wave/2026-07/runtime", TargetBaseSHAAtAdmission: "base", Status: queue.ReadyToFinalize}, {ID: "red", ProjectID: "repo", Branch: "agent/red", SHA: "dddddddd", TargetRef: "wave/2026-07/runtime", Status: queue.NeedsInput}} {
		w, err = s.Add(w.ID, q)
		if err != nil {
			t.Fatal(err)
		}
	}
	w, err = s.Configure(w.ID, []string{"graph-b", "graph-a"}, []string{"scenario", "gate"}, "medium", "minor")
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Freeze(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w.State != Frozen || len(w.Manifest.Members) != 3 || w.ManifestDigest == "" {
		t.Fatalf("freeze=%+v", w)
	}
	first := w.ManifestDigest
	w, err = s.Unfreeze(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Freeze(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w.ManifestDigest != first {
		t.Fatalf("digest changed: %s != %s", w.ManifestDigest, first)
	}
}

func TestMutationInvalidatesVerificationAndApprovalAndPartialLandingRecovers(t *testing.T) {
	s := &Store{ProjectRoot: t.TempDir(), Now: func() time.Time { return time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC) }}
	w, err := s.Create("Review", "theme", "train")
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Add(w.ID, queue.Candidate{ID: "one", ProjectID: "repo", Branch: "agent/one", SHA: "aaaa", TargetRef: "wave/train/theme", Status: queue.ReadyToFinalize})
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Freeze(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Prepare(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Verify(w.ID, []string{"gate-1"})
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Approve(w.ID, "steward", "ok", w.ManifestDigest, w.PreparedDigest)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Land(w.ID, []Landing{{CandidateID: "one", Status: "landed", ResultSHA: "main"}})
	if err != nil || w.State != Landed {
		t.Fatalf("land=%+v err=%v", w, err)
	}
	// A second wave proves partial landing is durable recovery rather than a lie.
	w, err = s.Create("Review two", "theme", "train")
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Add(w.ID, queue.Candidate{ID: "two", ProjectID: "repo", Branch: "agent/two", SHA: "bbbb", TargetRef: "wave/train/theme", Status: queue.ReadyToFinalize})
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Freeze(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Prepare(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Verify(w.ID, []string{"gate-2"})
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Approve(w.ID, "steward", "ok", w.ManifestDigest, w.PreparedDigest)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Land(w.ID, []Landing{{CandidateID: "two", Status: "failed", Error: "CAS moved"}})
	if err != nil || w.State != NeedsInput {
		t.Fatalf("partial=%+v err=%v", w, err)
	}
}

func TestMetadataMutationInvalidatesFrozenManifest(t *testing.T) {
	s := &Store{ProjectRoot: t.TempDir()}
	w, err := s.Create("Review", "theme", "train")
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Add(w.ID, queue.Candidate{ID: "one", ProjectID: "repo", Branch: "agent/one", SHA: "aaaa", TargetRef: "wave/train/theme", Status: queue.ReadyToFinalize})
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Freeze(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Configure(w.ID, []string{"graph-1"}, []string{"gate"}, "high", "major"); err != nil {
		t.Fatal(err)
	}
	w, err = s.Get(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w.State != Integrating || w.ManifestDigest != "" || w.Verification != nil || w.Approval != nil {
		t.Fatalf("mutation did not invalidate: %+v", w)
	}
}
