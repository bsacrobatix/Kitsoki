package trace

import "testing"

func TestValidateDocumentRequiresKnownSchemaAndEventFacts(t *testing.T) {
	doc := NewDocument(
		Event{Kind: KindCIStarted, JobID: "job", EnvelopeDigest: "sha256:env"},
		Event{Kind: KindSyncPlanned, PlanDigest: "sha256:plan", Operation: "publish", TargetRef: "main"},
		Event{Kind: KindWorkspaceReady, InstanceID: "workspace", Generation: 2},
	)
	if err := ValidateDocument(doc); err != nil {
		t.Fatal(err)
	}
	doc.Schema = "other"
	if err := ValidateDocument(doc); err == nil {
		t.Fatal("unsupported schema accepted")
	}
}

func TestValidateEventRejectsIncompleteKnownFacts(t *testing.T) {
	cases := []Event{
		{Kind: KindCIStarted, JobID: "job"},
		{Kind: KindSyncApplied, PlanDigest: "sha256:plan", Operation: "promote"},
		{Kind: KindWorkspaceCommitted},
		{Kind: "capsule.unknown"},
	}
	for _, tc := range cases {
		if err := ValidateEvent(tc); err == nil {
			t.Fatalf("event accepted: %#v", tc)
		}
	}
}
