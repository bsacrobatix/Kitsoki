package browserresearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"kitsoki/internal/runstatus/harscrub"
	"kitsoki/internal/study"
)

type FailureKind string

const (
	FailureAccess  FailureKind = "access"
	FailureBot     FailureKind = "bot"
	FailureConsent FailureKind = "consent"
	FailureNetwork FailureKind = "network"
	FailureWorker  FailureKind = "worker"
)

type ApprovedRoot struct {
	URL string `json:"url"`
}
type Request struct {
	StudyID       string         `json:"study_id"`
	WaveID        string         `json:"wave_id"`
	CellID        string         `json:"cell_id"`
	AttemptID     string         `json:"attempt_id"`
	URL           string         `json:"url"`
	ApprovedRoots []ApprovedRoot `json:"approved_roots"`
}
type Fixture struct {
	URL            string      `json:"url"`
	Content        string      `json:"content,omitempty"`
	Freshness      string      `json:"freshness,omitempty"`
	Confidence     float64     `json:"confidence,omitempty"`
	Unknowns       []string    `json:"unknowns,omitempty"`
	Contradictions []string    `json:"contradictions,omitempty"`
	Failure        FailureKind `json:"failure,omitempty"`
	Interactive    bool        `json:"interactive,omitempty"`
	RRWeb          string      `json:"rrweb,omitempty"`
	Chapters       []string    `json:"chapters,omitempty"`
	Poster         string      `json:"poster,omitempty"`
}
type SourceManifest struct {
	URL            string    `json:"url"`
	CapturedAt     time.Time `json:"captured_at"`
	ContentDigest  string    `json:"content_digest,omitempty"`
	Freshness      string    `json:"freshness,omitempty"`
	Confidence     float64   `json:"confidence,omitempty"`
	Unknowns       []string  `json:"unknowns,omitempty"`
	Contradictions []string  `json:"contradictions,omitempty"`
}
type RedactionReceipt struct {
	Applied     bool     `json:"applied"`
	RRWebDigest string   `json:"rrweb_digest,omitempty"`
	Chapters    []string `json:"chapters,omitempty"`
	Poster      string   `json:"poster,omitempty"`
}
type Bundle struct {
	StudyID   string           `json:"study_id"`
	WaveID    string           `json:"wave_id"`
	CellID    string           `json:"cell_id"`
	AttemptID string           `json:"attempt_id"`
	Source    SourceManifest   `json:"source"`
	Redaction RedactionReceipt `json:"redaction"`
	Failure   FailureKind      `json:"failure,omitempty"`
	Ready     bool             `json:"ready"`
}
type Transport interface {
	Fetch(context.Context, string) (Fixture, error)
}
type FixtureTransport map[string]Fixture

func (t FixtureTransport) Fetch(_ context.Context, u string) (Fixture, error) {
	f, ok := t[u]
	if !ok {
		return Fixture{}, fmt.Errorf("fixture missing for %s", u)
	}
	return f, nil
}

type Worker struct {
	Store     study.Store
	Transport Transport
	Now       func() time.Time
}

func (w Worker) Execute(ctx context.Context, r Request) (Bundle, error) {
	if w.Store == nil || w.Transport == nil {
		return Bundle{}, fmt.Errorf("browser research requires study store and fixture transport")
	}
	if r.StudyID == "" || r.WaveID == "" || r.CellID == "" || r.AttemptID == "" {
		return Bundle{}, fmt.Errorf("browser research requires study, wave, cell, and attempt identifiers")
	}
	if !approved(r.URL, r.ApprovedRoots) {
		return Bundle{}, fmt.Errorf("source URL is outside approved roots")
	}
	f, err := w.Transport.Fetch(ctx, r.URL)
	if err != nil {
		_ = w.Store.Record(ctx, r.StudyID, r.CellID, r.AttemptID, study.Result{FailureKind: string(FailureNetwork)})
		return Bundle{}, err
	}
	if f.URL != "" && f.URL != r.URL {
		return Bundle{}, fmt.Errorf("fixture URL does not match requested source")
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	b := Bundle{StudyID: r.StudyID, WaveID: r.WaveID, CellID: r.CellID, AttemptID: r.AttemptID, Source: SourceManifest{URL: r.URL, CapturedAt: now().UTC(), Freshness: f.Freshness, Confidence: f.Confidence, Unknowns: f.Unknowns, Contradictions: f.Contradictions}}
	if f.Failure != "" {
		b.Failure = f.Failure
		_ = w.Store.Record(ctx, r.StudyID, r.CellID, r.AttemptID, study.Result{FailureKind: string(f.Failure)})
		return b, nil
	}
	scrubbed := harscrub.ScrubString(f.Content, harscrub.ScrubOptions{})
	b.Source.ContentDigest = digest(scrubbed)
	if f.Interactive {
		rr := harscrub.ScrubString(f.RRWeb, harscrub.ScrubOptions{})
		b.Redaction = RedactionReceipt{Applied: true, RRWebDigest: digest(rr), Chapters: append([]string(nil), f.Chapters...), Poster: f.Poster}
		if rr == "" || len(f.Chapters) == 0 || f.Poster == "" {
			return Bundle{}, fmt.Errorf("interactive fixture requires rrweb, chapters, and poster")
		}
	}
	b.Ready = true
	if err := w.Store.Record(ctx, r.StudyID, r.CellID, r.AttemptID, study.Result{ResultDigest: b.Source.ContentDigest}); err != nil {
		return Bundle{}, err
	}
	return b, nil
}
func approved(raw string, roots []ApprovedRoot) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" {
		return false
	}
	for _, root := range roots {
		r, e := url.Parse(root.URL)
		if e == nil && r.Scheme == u.Scheme && r.Host == u.Host && strings.HasPrefix(u.Path, strings.TrimSuffix(r.Path, "/")) {
			return true
		}
	}
	return false
}
func digest(v string) string {
	x := sha256.Sum256([]byte(v))
	return "sha256:" + hex.EncodeToString(x[:])
}
