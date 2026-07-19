package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	CandidateSchema = "kitsoki/release-candidate/v1"
	ShippedSchema   = "kitsoki/release-shipped/v1"
)

var (
	ErrUnauthorized = errors.New("release: publication is not authorized")
	ErrPartial      = errors.New("release: partial landing is not a release")
)

// Impact is the compatibility consequence declared by a change or wave.
type Impact string

const (
	ImpactNone  Impact = "none"
	ImpactPatch Impact = "patch"
	ImpactMinor Impact = "minor"
	ImpactMajor Impact = "major"
)

type Repository struct {
	Base        string `json:"base"`
	Prospective string `json:"prospective"`
	Tree        string `json:"tree"`
}
type Wave struct{ ID, Digest string }
type Artifact struct{ Name, Digest, URI string }
type Migration struct{ ID, Digest string }
type Rollback struct{ Target, InstructionsDigest string }
type Compatibility struct {
	Required Impact    `json:"required"`
	Proposed string    `json:"proposed"`
	Override *Override `json:"override,omitempty"`
}
type Override struct{ Version, Rationale, Actor string }

// Candidate is the canonical source of truth before a release lands.
type Candidate struct {
	Schema                  string                `json:"schema"`
	ID                      string                `json:"id"`
	Version                 string                `json:"version"`
	PreviousRelease         string                `json:"previous_release,omitempty"`
	Repositories            map[string]Repository `json:"repositories"`
	Catalogs                map[string]string     `json:"catalogs,omitempty"`
	Waves                   []Wave                `json:"waves,omitempty"`
	RuntimeDefinitionDigest string                `json:"runtime_definition_digest"`
	Artifacts               []Artifact            `json:"artifacts,omitempty"`
	Migrations              []Migration           `json:"migrations,omitempty"`
	Rollback                Rollback              `json:"rollback"`
	RequiredReceipts        []string              `json:"required_receipts,omitempty"`
	Compatibility           Compatibility         `json:"compatibility"`
}

// PlanInput keeps compatibility declarations separate from the immutable
// candidate, allowing callers to display a dry-run before persisting it.
type PlanInput struct {
	Candidate
	Impacts []Impact
}

type Plan struct {
	Candidate Candidate `json:"candidate"`
	Digest    string    `json:"digest"`
	DryRun    bool      `json:"dry_run"`
}

func BuildPlan(in PlanInput) (Plan, error) {
	c := in.Candidate
	if c.Schema == "" {
		c.Schema = CandidateSchema
	}
	if c.Schema != CandidateSchema {
		return Plan{}, fmt.Errorf("release: unsupported candidate schema %q", c.Schema)
	}
	if c.Version == "" {
		return Plan{}, errors.New("release: version is required")
	}
	if err := validateRepositories(c.Repositories); err != nil {
		return Plan{}, err
	}
	if c.RuntimeDefinitionDigest == "" {
		return Plan{}, errors.New("release: runtime definition digest is required")
	}
	if c.Rollback.Target == "" {
		return Plan{}, errors.New("release: rollback target is required")
	}
	required, err := highest(in.Impacts)
	if err != nil {
		return Plan{}, err
	}
	if c.Compatibility.Required != "" && c.Compatibility.Required != required {
		return Plan{}, fmt.Errorf("release: compatibility required %q conflicts with declared impacts %q", c.Compatibility.Required, required)
	}
	c.Compatibility.Required = required
	proposed, err := increment(c.Version, required)
	if err != nil {
		return Plan{}, err
	}
	if c.Compatibility.Proposed == "" {
		c.Compatibility.Proposed = proposed
	}
	if c.Compatibility.Proposed != proposed && c.Compatibility.Override == nil {
		return Plan{}, fmt.Errorf("release: proposed version %q requires audited override", c.Compatibility.Proposed)
	}
	if o := c.Compatibility.Override; o != nil && (o.Version != c.Compatibility.Proposed || o.Rationale == "" || o.Actor == "") {
		return Plan{}, errors.New("release: override needs matching version, actor, and rationale")
	}
	normalize(&c)
	digest, err := Digest(c)
	if err != nil {
		return Plan{}, err
	}
	if c.ID == "" {
		c.ID = "rc-" + strings.TrimPrefix(digest, "sha256:")[:16]
	}
	return Plan{Candidate: c, Digest: digest, DryRun: true}, nil
}

func Digest(c Candidate) (string, error) {
	copy := c
	copy.ID = "" // identity derives from content, never itself.
	normalize(&copy)
	b, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:]), nil
}

func validateRepositories(repos map[string]Repository) error {
	if len(repos) == 0 {
		return errors.New("release: at least one repository is required")
	}
	for name, r := range repos {
		if name == "" || r.Base == "" || r.Prospective == "" || r.Tree == "" {
			return fmt.Errorf("release: repository %q needs base, prospective, and tree", name)
		}
	}
	return nil
}
func highest(impacts []Impact) (Impact, error) {
	h := ImpactNone
	for _, i := range impacts {
		switch i {
		case "", ImpactNone:
		case ImpactPatch:
			if h == ImpactNone {
				h = i
			}
		case ImpactMinor:
			if h != ImpactMajor {
				h = i
			}
		case ImpactMajor:
			h = i
		default:
			return "", fmt.Errorf("release: invalid compatibility impact %q", i)
		}
	}
	return h, nil
}
func increment(version string, impact Impact) (string, error) {
	var major, minor, patch int
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); err != nil || major < 0 || minor < 0 || patch < 0 {
		return "", fmt.Errorf("release: version must be semantic x.y.z: %q", version)
	}
	switch impact {
	case ImpactMajor:
		major++
		minor = 0
		patch = 0
	case ImpactMinor:
		minor++
		patch = 0
	case ImpactPatch:
		patch++
	}
	return fmt.Sprintf("%d.%d.%d", major, minor, patch), nil
}
func normalize(c *Candidate) {
	sort.Slice(c.Waves, func(i, j int) bool { return c.Waves[i].ID < c.Waves[j].ID })
	sort.Slice(c.Artifacts, func(i, j int) bool { return c.Artifacts[i].Name < c.Artifacts[j].Name })
	sort.Slice(c.Migrations, func(i, j int) bool { return c.Migrations[i].ID < c.Migrations[j].ID })
	sort.Strings(c.RequiredReceipts)
}

type Store interface {
	Put(context.Context, Candidate, string) error
	Get(context.Context, string) (Candidate, error)
}

// Create persists a planned candidate. There is no implicit publication.
func Create(ctx context.Context, store Store, plan Plan) (Candidate, error) {
	if !plan.DryRun {
		return Candidate{}, errors.New("release: create requires a dry-run plan")
	}
	if err := store.Put(ctx, plan.Candidate, plan.Digest); err != nil {
		return Candidate{}, err
	}
	return plan.Candidate, nil
}

type LandingEvidence struct {
	Landed         bool   `json:"landed"`
	Ancestry       bool   `json:"ancestry"`
	Tree           string `json:"tree"`
	ManifestDigest string `json:"manifest_digest"`
}
type LandingInspector interface {
	Inspect(context.Context, string, Repository) (LandingEvidence, error)
}
type Verification struct {
	CandidateDigest string                     `json:"candidate_digest"`
	Repositories    map[string]LandingEvidence `json:"repositories"`
	VerifiedAt      time.Time                  `json:"verified_at"`
	Complete        bool                       `json:"complete"`
}

// Verify proves every prospective tree landed and still agrees with the exact
// manifest. A single missing repository makes the result explicitly partial.
func Verify(ctx context.Context, c Candidate, inspect LandingInspector, now func() time.Time) (Verification, error) {
	digest, err := Digest(c)
	if err != nil {
		return Verification{}, err
	}
	v := Verification{CandidateDigest: digest, Repositories: map[string]LandingEvidence{}, VerifiedAt: now()}
	v.Complete = true
	for name, repo := range c.Repositories {
		e, err := inspect.Inspect(ctx, name, repo)
		if err != nil {
			return Verification{}, err
		}
		v.Repositories[name] = e
		if !e.Landed || !e.Ancestry || e.Tree != repo.Tree || e.ManifestDigest != digest {
			v.Complete = false
		}
	}
	return v, nil
}

type Publisher interface {
	Publish(context.Context, Candidate, Verification) (Published, error)
}
type Published struct {
	Tags      map[string]string `json:"tags"`
	Artifacts []Artifact        `json:"artifacts"`
	Actor     string            `json:"actor"`
	At        time.Time         `json:"at"`
}
type Shipped struct {
	Schema          string       `json:"schema"`
	Candidate       Candidate    `json:"candidate"`
	CandidateDigest string       `json:"candidate_digest"`
	Verification    Verification `json:"verification"`
	Published       Published    `json:"published"`
}

// Publish is the explicit authorization boundary for tags and artifacts.
func Publish(ctx context.Context, authorized bool, c Candidate, v Verification, publisher Publisher) (Shipped, error) {
	if !authorized {
		return Shipped{}, ErrUnauthorized
	}
	if !v.Complete {
		return Shipped{}, ErrPartial
	}
	digest, err := Digest(c)
	if err != nil {
		return Shipped{}, err
	}
	if v.CandidateDigest != digest {
		return Shipped{}, errors.New("release: verification is for a different manifest")
	}
	p, err := publisher.Publish(ctx, c, v)
	if err != nil {
		return Shipped{}, err
	}
	return Shipped{Schema: ShippedSchema, Candidate: c, CandidateDigest: digest, Verification: v, Published: p}, nil
}
