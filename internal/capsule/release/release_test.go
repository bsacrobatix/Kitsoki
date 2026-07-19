package release

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func candidate() Candidate {
	return Candidate{Version: "1.2.3", Repositories: map[string]Repository{
		"kitsoki": {Base: "base-k", Prospective: "next-k", Tree: "tree-k"},
		"pog":     {Base: "base-p", Prospective: "next-p", Tree: "tree-p"},
	}, RuntimeDefinitionDigest: "sha256:runtime", Rollback: Rollback{Target: "v1.2.3"}}
}

func TestPlanCreateVerifyAndPublish(t *testing.T) {
	plan, err := BuildPlan(PlanInput{Candidate: candidate(), Impacts: []Impact{ImpactPatch, ImpactMinor}})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || plan.Candidate.ID == "" || plan.Candidate.Compatibility.Proposed != "1.3.0" {
		t.Fatalf("bad plan: %#v", plan)
	}
	store := FileStore{Root: filepath.Join(t.TempDir(), "releases")}
	c, err := Create(context.Background(), store, plan)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), plan.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID {
		t.Fatalf("stored id = %q, want %q", got.ID, c.ID)
	}
	v, err := Verify(context.Background(), c, fakeInspector{digest: plan.Digest}, func() time.Time { return time.Unix(1, 0) })
	if err != nil {
		t.Fatal(err)
	}
	if !v.Complete {
		t.Fatalf("verification incomplete: %#v", v)
	}
	shipped, err := Publish(context.Background(), true, c, v, fakePublisher{})
	if err != nil {
		t.Fatal(err)
	}
	if shipped.Schema != ShippedSchema || len(shipped.Published.Tags) != 2 {
		t.Fatalf("bad shipped release: %#v", shipped)
	}
}

func TestPartialLandingCannotPublish(t *testing.T) {
	plan, err := BuildPlan(PlanInput{Candidate: candidate()})
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(context.Background(), plan.Candidate, fakeInspector{digest: plan.Digest, partial: "pog"}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Complete {
		t.Fatal("partial landing reported complete")
	}
	_, err = Publish(context.Background(), true, plan.Candidate, v, fakePublisher{})
	if !errors.Is(err, ErrPartial) {
		t.Fatalf("Publish error = %v, want ErrPartial", err)
	}
}

func TestOverrideRequiresAuditAndDigestIsOrderIndependent(t *testing.T) {
	c := candidate()
	c.Compatibility.Proposed = "2.0.0"
	if _, err := BuildPlan(PlanInput{Candidate: c, Impacts: []Impact{ImpactPatch}}); err == nil {
		t.Fatal("unaudited override was accepted")
	}
	c.Compatibility.Override = &Override{Version: "2.0.0", Actor: "steward", Rationale: "coordinated public API break"}
	p, err := BuildPlan(PlanInput{Candidate: c, Impacts: []Impact{ImpactPatch}})
	if err != nil {
		t.Fatal(err)
	}
	c2 := p.Candidate
	c2.Waves = []Wave{{ID: "b", Digest: "2"}, {ID: "a", Digest: "1"}}
	c2.RequiredReceipts = []string{"z", "a"}
	c3 := p.Candidate
	c3.Waves = []Wave{{ID: "a", Digest: "1"}, {ID: "b", Digest: "2"}}
	c3.RequiredReceipts = []string{"a", "z"}
	d2, _ := Digest(c2)
	d3, _ := Digest(c3)
	if d2 != d3 {
		t.Fatalf("digest depends on ordering: %s != %s", d2, d3)
	}
}

type fakeInspector struct{ digest, partial string }

func (f fakeInspector) Inspect(_ context.Context, name string, repo Repository) (LandingEvidence, error) {
	return LandingEvidence{Landed: name != f.partial, Ancestry: name != f.partial, Tree: repo.Tree, ManifestDigest: f.digest}, nil
}

type fakePublisher struct{}

func (fakePublisher) Publish(_ context.Context, c Candidate, _ Verification) (Published, error) {
	return Published{Tags: map[string]string{"kitsoki": "v" + c.Compatibility.Proposed, "pog": "v" + c.Compatibility.Proposed}, Actor: "steward"}, nil
}
