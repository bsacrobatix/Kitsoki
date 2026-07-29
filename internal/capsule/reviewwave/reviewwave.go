// Package reviewwave owns durable, front-door-neutral review-wave records.
// A wave composes target-bound queue candidates; it never performs a hidden
// protected ref update itself.
package reviewwave

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kitsoki/internal/atomicfile"
	"kitsoki/internal/capsule/queue"
)

const Schema = "kitsoki/review-wave/v1"

type State string

const (
	Draft        State = "draft"
	Integrating  State = "integrating"
	Reviewable   State = "reviewable"
	Frozen       State = "frozen"
	Verifying    State = "verifying"
	Approved     State = "approved"
	LandingState State = "landing"
	Landed       State = "landed"
	NeedsInput   State = "needs_input"
	Abandoned    State = "abandoned"
)

type Member struct {
	Repository    string `json:"repository"`
	CandidateID   string `json:"candidate_id"`
	SourceRef     string `json:"source_ref"`
	SourceSHA     string `json:"source_sha"`
	TargetRef     string `json:"target_ref"`
	TargetBaseSHA string `json:"target_base_sha,omitempty"`
	Parked        bool   `json:"parked,omitempty"`
}
type Target struct {
	Repository  string `json:"repository"`
	WaveRef     string `json:"wave_ref"`
	BaselineSHA string `json:"baseline_sha,omitempty"`
}
type Manifest struct {
	Schema              string   `json:"schema"`
	WaveID              string   `json:"wave_id"`
	Members             []Member `json:"members"`
	Targets             []Target `json:"targets"`
	Drives              []string `json:"drives,omitempty"`
	TestPlan            []string `json:"test_plan,omitempty"`
	CompatibilityImpact string   `json:"compatibility_impact,omitempty"`
}
type Verification struct {
	At             time.Time `json:"at"`
	Receipts       []string  `json:"receipts"`
	PreparedDigest string    `json:"prepared_digest"`
}
type Approval struct {
	Actor          string    `json:"actor"`
	Reason         string    `json:"reason"`
	At             time.Time `json:"at"`
	ManifestDigest string    `json:"manifest_digest"`
	PreparedDigest string    `json:"prepared_digest"`
}
type Landing struct {
	CandidateID string `json:"candidate_id"`
	Status      string `json:"status"`
	ResultSHA   string `json:"result_sha,omitempty"`
	Error       string `json:"error,omitempty"`
}
type Wave struct {
	Schema              string        `json:"schema"`
	ID                  string        `json:"id"`
	Title               string        `json:"title"`
	Theme               string        `json:"theme"`
	Train               string        `json:"train"`
	State               State         `json:"state"`
	Members             []Member      `json:"members"`
	Targets             []Target      `json:"targets"`
	Drives              []string      `json:"drives,omitempty"`
	TestPlan            []string      `json:"test_plan,omitempty"`
	Risk                string        `json:"risk,omitempty"`
	CompatibilityImpact string        `json:"compatibility_impact,omitempty"`
	ManifestDigest      string        `json:"manifest_digest,omitempty"`
	Manifest            Manifest      `json:"manifest,omitempty"`
	PreparedDigest      string        `json:"prepared_digest,omitempty"`
	Verification        *Verification `json:"verification,omitempty"`
	Approval            *Approval     `json:"approval,omitempty"`
	Landings            []Landing     `json:"landings,omitempty"`
	Evidence            []string      `json:"evidence,omitempty"`
	CreatedAt           time.Time     `json:"created_at"`
	UpdatedAt           time.Time     `json:"updated_at"`
}
type document struct {
	Schema string `json:"schema"`
	Waves  []Wave `json:"waves"`
}
type Store struct {
	ProjectRoot string
	Now         func() time.Time
	mu          sync.Mutex
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *Store) path() (string, error) {
	root, err := filepath.Abs(s.ProjectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".kitsoki", "review-waves.json"), nil
}
func (s *Store) read() (document, error) {
	p, err := s.path()
	if err != nil {
		return document{}, err
	}
	raw, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return document{Schema: Schema}, nil
	}
	if err != nil {
		return document{}, err
	}
	var d document
	if err = json.Unmarshal(raw, &d); err != nil {
		return document{}, fmt.Errorf("review wave: parse state: %w", err)
	}
	if d.Schema != "" && d.Schema != Schema {
		return document{}, fmt.Errorf("review wave: unsupported schema %q", d.Schema)
	}
	d.Schema = Schema
	return d, nil
}
func (s *Store) write(d document) error {
	p, err := s.path()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(p, raw, 0o644, 0o755)
}
func (s *Store) lock() (func(), error) {
	p, err := s.path()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			return func() { _ = f.Close(); _ = os.Remove(p + ".lock") }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("review wave: acquire serializer: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("review wave: serializer busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func (s *Store) mutate(id string, fn func(*Wave) error) (Wave, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return Wave{}, err
	}
	defer unlock()
	d, err := s.read()
	if err != nil {
		return Wave{}, err
	}
	for i := range d.Waves {
		if d.Waves[i].ID == id {
			if err := fn(&d.Waves[i]); err != nil {
				return Wave{}, err
			}
			d.Waves[i].UpdatedAt = s.now()
			if err := s.write(d); err != nil {
				return Wave{}, err
			}
			return d.Waves[i], nil
		}
	}
	return Wave{}, fmt.Errorf("review wave: %q not found", id)
}
func (s *Store) Create(title, theme, train string) (Wave, error) {
	if strings.TrimSpace(title) == "" || strings.TrimSpace(theme) == "" || strings.TrimSpace(train) == "" {
		return Wave{}, fmt.Errorf("review wave: title, theme, and train are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return Wave{}, err
	}
	defer unlock()
	d, err := s.read()
	if err != nil {
		return Wave{}, err
	}
	now := s.now()
	sum := sha256.Sum256([]byte(title + "\000" + theme + "\000" + train + "\000" + now.Format(time.RFC3339Nano)))
	w := Wave{Schema: Schema, ID: "rw-" + hex.EncodeToString(sum[:])[:12], Title: title, Theme: theme, Train: train, State: Draft, CreatedAt: now, UpdatedAt: now}
	d.Waves = append(d.Waves, w)
	if err := s.write(d); err != nil {
		return Wave{}, err
	}
	return w, nil
}
func (s *Store) Get(id string) (Wave, error) {
	d, err := s.read()
	if err != nil {
		return Wave{}, err
	}
	for _, w := range d.Waves {
		if w.ID == id {
			return w, nil
		}
	}
	return Wave{}, fmt.Errorf("review wave: %q not found", id)
}

// Configure records the metadata that is pinned into a frozen manifest.
// Changing it invalidates any preparation, verification, or approval.
func (s *Store) Configure(id string, drives, testPlan []string, risk, compatibility string) (Wave, error) {
	return s.mutate(id, func(w *Wave) error {
		if w.State == Abandoned || w.State == Landed {
			return fmt.Errorf("review wave: cannot mutate %s wave", w.State)
		}
		w.Drives = sorted(drives)
		w.TestPlan = sorted(testPlan)
		w.Risk = risk
		w.CompatibilityImpact = compatibility
		invalidate(w, "review metadata updated")
		if len(w.Members) > 0 {
			w.State = Integrating
		}
		return nil
	})
}
func (s *Store) List() ([]Wave, error) {
	d, err := s.read()
	if err != nil {
		return nil, err
	}
	sort.Slice(d.Waves, func(i, j int) bool { return d.Waves[i].ID < d.Waves[j].ID })
	return d.Waves, nil
}

// Add records an already-admitted queue candidate. The queue is authoritative
// for admission and target identity; parked/red candidates remain visible but
// do not prevent the rest of a wave becoming reviewable.
func (s *Store) Add(id string, q queue.Candidate) (Wave, error) {
	return s.mutate(id, func(w *Wave) error {
		if w.State == Abandoned || w.State == Landed {
			return fmt.Errorf("review wave: cannot mutate %s wave", w.State)
		}
		for _, m := range w.Members {
			if m.CandidateID == q.ID {
				return nil
			}
		}
		m := Member{Repository: q.ProjectID, CandidateID: q.ID, SourceRef: q.Branch, SourceSHA: q.SHA, TargetRef: q.TargetRef, TargetBaseSHA: q.TargetBaseSHAAtAdmission, Parked: parked(q.Status)}
		w.Members = append(w.Members, m)
		w.Targets = targets(w.Members)
		invalidate(w, "member added: "+q.ID)
		w.State = Integrating
		return nil
	})
}
func parked(status queue.Status) bool {
	return status == queue.NeedsInput || status == queue.NeedsConflictInput || status == queue.NeedsHuman || status == queue.Rejected
}
func targets(ms []Member) []Target {
	seen := map[string]Target{}
	for _, m := range ms {
		if m.Parked {
			continue
		}
		k := m.Repository + "\000" + m.TargetRef
		seen[k] = Target{Repository: m.Repository, WaveRef: m.TargetRef, BaselineSHA: m.TargetBaseSHA}
	}
	out := make([]Target, 0, len(seen))
	for _, t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repository+out[i].WaveRef < out[j].Repository+out[j].WaveRef })
	return out
}
func (s *Store) Remove(id, candidateID string) (Wave, error) {
	return s.mutate(id, func(w *Wave) error {
		out := w.Members[:0]
		found := false
		for _, m := range w.Members {
			if m.CandidateID == candidateID {
				found = true
				continue
			}
			out = append(out, m)
		}
		if !found {
			return fmt.Errorf("review wave: member %q not found", candidateID)
		}
		w.Members = out
		w.Targets = targets(out)
		invalidate(w, "member removed: "+candidateID)
		w.State = Integrating
		return nil
	})
}

// Split creates a sibling wave and moves the named members to it atomically.
func (s *Store) Split(id, title, theme, train string, candidateIDs []string) (Wave, error) {
	if len(candidateIDs) == 0 {
		return Wave{}, fmt.Errorf("review wave: split requires members")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return Wave{}, err
	}
	defer unlock()
	d, err := s.read()
	if err != nil {
		return Wave{}, err
	}
	var source *Wave
	for i := range d.Waves {
		if d.Waves[i].ID == id {
			source = &d.Waves[i]
			break
		}
	}
	if source == nil {
		return Wave{}, fmt.Errorf("review wave: %q not found", id)
	}
	want := map[string]bool{}
	for _, candidateID := range candidateIDs {
		want[candidateID] = true
	}
	moved := []Member{}
	kept := []Member{}
	for _, m := range source.Members {
		if want[m.CandidateID] {
			moved = append(moved, m)
			delete(want, m.CandidateID)
		} else {
			kept = append(kept, m)
		}
	}
	if len(want) > 0 {
		return Wave{}, fmt.Errorf("review wave: split member not found")
	}
	if len(moved) == 0 {
		return Wave{}, fmt.Errorf("review wave: split requires members")
	}
	now := s.now()
	sum := sha256.Sum256([]byte(title + "\000" + theme + "\000" + train + "\000" + now.Format(time.RFC3339Nano)))
	created := Wave{Schema: Schema, ID: "rw-" + hex.EncodeToString(sum[:])[:12], Title: title, Theme: theme, Train: train, State: Integrating, Members: moved, Targets: targets(moved), Drives: append([]string(nil), source.Drives...), TestPlan: append([]string(nil), source.TestPlan...), Risk: source.Risk, CompatibilityImpact: source.CompatibilityImpact, CreatedAt: now, UpdatedAt: now, Evidence: []string{"split from: " + source.ID}}
	source.Members = kept
	source.Targets = targets(kept)
	invalidate(source, "members split to: "+created.ID)
	source.State = Integrating
	source.UpdatedAt = now
	d.Waves = append(d.Waves, created)
	if err := s.write(d); err != nil {
		return Wave{}, err
	}
	return created, nil
}
func invalidate(w *Wave, evidence string) {
	w.ManifestDigest = ""
	w.Manifest = Manifest{}
	w.PreparedDigest = ""
	w.Verification = nil
	w.Approval = nil
	w.Evidence = append(w.Evidence, evidence)
}
func (s *Store) Freeze(id string) (Wave, error) {
	return s.mutate(id, func(w *Wave) error {
		if w.State == Abandoned {
			return fmt.Errorf("review wave: abandoned")
		}
		members := append([]Member(nil), w.Members...)
		sort.Slice(members, func(i, j int) bool {
			return members[i].Repository+members[i].CandidateID < members[j].Repository+members[j].CandidateID
		})
		active := members[:0]
		for _, m := range members {
			if !m.Parked {
				active = append(active, m)
			}
		}
		if len(active) == 0 {
			return fmt.Errorf("review wave: no unparked members to freeze")
		}
		manifest := Manifest{Schema: Schema, WaveID: w.ID, Members: active, Targets: targets(active), Drives: sorted(w.Drives), TestPlan: sorted(w.TestPlan), CompatibilityImpact: w.CompatibilityImpact}
		raw, _ := json.Marshal(manifest)
		sum := sha256.Sum256(raw)
		w.Manifest = manifest
		w.ManifestDigest = "sha256:" + hex.EncodeToString(sum[:])
		w.Verification = nil
		w.Approval = nil
		w.PreparedDigest = ""
		w.State = Frozen
		w.Evidence = append(w.Evidence, "frozen: "+w.ManifestDigest)
		return nil
	})
}
func sorted(in []string) []string { out := append([]string(nil), in...); sort.Strings(out); return out }
func (s *Store) Unfreeze(id string) (Wave, error) {
	return s.mutate(id, func(w *Wave) error {
		if w.State == Landed {
			return fmt.Errorf("review wave: landed")
		}
		invalidate(w, "unfrozen")
		w.State = Integrating
		return nil
	})
}
func (s *Store) Abandon(id, reason string) (Wave, error) {
	return s.mutate(id, func(w *Wave) error {
		w.State = Abandoned
		w.Evidence = append(w.Evidence, "abandoned: "+reason)
		return nil
	})
}
func (s *Store) Prepare(id string) (Wave, error) {
	return s.mutate(id, func(w *Wave) error {
		if w.State != Frozen && w.State != Verifying {
			return fmt.Errorf("review wave: prepare requires frozen wave")
		}
		sum := sha256.Sum256([]byte(w.ManifestDigest + "\000" + strings.Join(targetStrings(w.Manifest.Targets), "\000")))
		w.PreparedDigest = "sha256:" + hex.EncodeToString(sum[:])
		w.Verification = nil
		w.Approval = nil
		w.State = Verifying
		w.Evidence = append(w.Evidence, "prepared: "+w.PreparedDigest)
		return nil
	})
}
func targetStrings(ts []Target) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Repository + "@" + t.WaveRef + "@" + t.BaselineSHA
	}
	return out
}
func (s *Store) Verify(id string, receipts []string) (Wave, error) {
	return s.mutate(id, func(w *Wave) error {
		if w.State != Verifying || w.PreparedDigest == "" {
			return fmt.Errorf("review wave: verify requires prepared wave")
		}
		if len(receipts) == 0 {
			return fmt.Errorf("review wave: verification receipts are required")
		}
		w.Verification = &Verification{At: s.now(), Receipts: sorted(receipts), PreparedDigest: w.PreparedDigest}
		w.State = Reviewable
		w.Evidence = append(w.Evidence, "verified: "+w.PreparedDigest)
		return nil
	})
}
func (s *Store) Approve(id, actor, reason, manifest, prepared string) (Wave, error) {
	return s.mutate(id, func(w *Wave) error {
		if w.State != Reviewable || w.Verification == nil {
			return fmt.Errorf("review wave: approval requires verified wave")
		}
		if manifest != w.ManifestDigest || prepared != w.PreparedDigest {
			return fmt.Errorf("review wave: stale manifest or prepared tree")
		}
		w.Approval = &Approval{Actor: actor, Reason: reason, At: s.now(), ManifestDigest: manifest, PreparedDigest: prepared}
		w.State = Approved
		w.Evidence = append(w.Evidence, "approved by: "+actor)
		return nil
	})
}

// Land persists per-member outcomes. Callers execute queue finalization through
// their repository-owned queue and report each durable result here. A failure
// leaves the wave in needs_input, preserving successful partial landings.
func (s *Store) Land(id string, results []Landing) (Wave, error) {
	return s.mutate(id, func(w *Wave) error {
		if w.State != Approved || w.Approval == nil {
			return fmt.Errorf("review wave: landing requires approval")
		}
		w.State = LandingState
		w.Landings = append([]Landing(nil), results...)
		for _, r := range results {
			if r.Error != "" {
				w.State = NeedsInput
				w.Evidence = append(w.Evidence, "partial landing: "+r.CandidateID)
				return nil
			}
		}
		w.State = Landed
		w.Evidence = append(w.Evidence, "landed: "+w.ManifestDigest)
		return nil
	})
}
