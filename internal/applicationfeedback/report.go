package applicationfeedback

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"kitsoki/internal/app"
	"kitsoki/internal/application"
	"kitsoki/internal/host"
)

const (
	AttachmentSchema = "application-feedback/v1"
	ReportSchema     = "kitsoki.feedback.report.v1"
)

// Attachment is the privacy-safe, transport-neutral context an application
// adapter contributes to the existing reviewed feedback report pipeline.
type Attachment struct {
	Schema        string                `json:"schema"`
	ApplicationID string                `json:"application_id"`
	FrameRevision uint64                `json:"frame_revision"`
	Anchor        host.AnnotationAnchor `json:"anchor"`
}

// ReportRequest contains reviewed operator input. Adapters may add surface
// evidence around the returned report, but semantic identity always comes from
// the canonical frame.
type ReportRequest struct {
	Ref            string
	Kind           string
	Instruction    string
	IdempotencyKey string
}

// Report is accepted without translation by POST /api/feedback/local. It is a
// deliberately small subset of the generic report contract: the report path
// can enrich it with evidence, privacy review, and sink routing later.
type Report struct {
	Schema         string                `json:"schema"`
	IdempotencyKey string                `json:"idempotencyKey"`
	App            string                `json:"app"`
	Producer       string                `json:"producer"`
	Kind           string                `json:"kind"`
	UserText       string                `json:"userText"`
	Reviewed       bool                  `json:"reviewed"`
	Attachment     Attachment            `json:"application"`
	Anchor         host.AnnotationAnchor `json:"anchor"`
}

// BuildAttachment resolves one semantic ref without reading component props,
// element values, world state, credentials, or host paths.
func BuildAttachment(
	frame application.Frame,
	ref string,
	policy *app.ApplicationFeedbackPolicy,
) (Attachment, error) {
	anchor, err := Anchor(frame, strings.TrimSpace(ref), policy)
	if err != nil {
		return Attachment{}, err
	}
	return Attachment{
		Schema: AttachmentSchema, ApplicationID: frame.ApplicationID,
		FrameRevision: frame.Revision, Anchor: anchor,
	}, nil
}

// BuildReport wraps an attachment in the existing reviewed feedback intake
// contract. A missing idempotency key is derived from stable reviewed content,
// so transport retries coalesce at the sink.
func BuildReport(
	frame application.Frame,
	policy *app.ApplicationFeedbackPolicy,
	request ReportRequest,
) (Report, error) {
	instruction := strings.TrimSpace(request.Instruction)
	if instruction == "" {
		return Report{}, errors.New("application feedback: instruction is required")
	}
	attachment, err := BuildAttachment(frame, request.Ref, policy)
	if err != nil {
		return Report{}, err
	}
	kind := strings.TrimSpace(request.Kind)
	if kind == "" {
		kind = "bug"
	}
	key := strings.TrimSpace(request.IdempotencyKey)
	if key == "" {
		key = reportKey(frame, attachment.Anchor, kind, instruction)
	}
	return Report{
		Schema: ReportSchema, IdempotencyKey: key, App: frame.ApplicationID,
		Producer: Plugin, Kind: kind, UserText: instruction, Reviewed: true,
		Attachment: attachment, Anchor: attachment.Anchor,
	}, nil
}

func reportKey(frame application.Frame, anchor host.AnnotationAnchor, kind, instruction string) string {
	raw, _ := json.Marshal(struct {
		Application string                `json:"application"`
		Session     string                `json:"session"`
		Revision    uint64                `json:"revision"`
		Anchor      host.AnnotationAnchor `json:"anchor"`
		Kind        string                `json:"kind"`
		Instruction string                `json:"instruction"`
	}{
		Application: frame.ApplicationID, Session: frame.SessionID,
		Revision: frame.Revision, Anchor: anchor, Kind: kind, Instruction: instruction,
	})
	sum := sha256.Sum256(raw)
	return "application-feedback-" + hex.EncodeToString(sum[:12])
}
