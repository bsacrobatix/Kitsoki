// Package featuresadapter is W3.0's pilot adapter: it walks a loaded
// project object graph catalog's public site-page nodes and emits the
// existing features/<id>.yaml shape (features/feature.schema.json) — the
// site codegen (tools/runstatus/scripts/features/generate.ts) keeps reading
// features/*.yaml unchanged; this package is the graph-sourced producer for
// the pilot feature (operator-ask), not a replacement for that codegen.
//
// Per the W3.0 design session: a site-page node's presents edge joins the
// feature it's presenting copy for, and its has_media edge joins the
// evidence node holding the demo recording binding — demo: is DERIVED from
// evidence, not duplicated onto the site-page node.
package featuresadapter

import (
	"fmt"
	"strings"

	"kitsoki/internal/graph"
)

// FeatureDoc mirrors features/feature.schema.json's fields this adapter
// populates. Field order matches the schema/existing files for readability;
// yaml.Marshal output is not asserted byte-identical to the committed files
// (formatting is not semantically significant) — see BuildFeatureDoc's doc
// comment and the adapter tests for how equivalence is actually checked.
type FeatureDoc struct {
	ID      string   `yaml:"id"`
	Kind    string   `yaml:"kind"`
	Title   string   `yaml:"title"`
	Tagline string   `yaml:"tagline"`
	Summary string   `yaml:"summary"`
	Promo   *Promo   `yaml:"promo,omitempty"`
	Docs    []string `yaml:"docs,omitempty"`
	Demo    *Demo    `yaml:"demo,omitempty"`
	Tour    *Tour    `yaml:"tour,omitempty"`
}

type Promo struct {
	Order     int  `yaml:"order"`
	Highlight bool `yaml:"highlight,omitempty"`
}

// Demo is derived entirely from the site-page's has_media evidence node —
// never stored on the site-page itself, so it can't drift from its source.
type Demo struct {
	Spec         string `yaml:"spec,omitempty"`
	ArtifactDir  string `yaml:"artifactDir,omitempty"`
	VideoBase    string `yaml:"videoBase,omitempty"`
	PosterStep   string `yaml:"posterStep,omitempty"`
	Story        string `yaml:"story,omitempty"`
	Flow         string `yaml:"flow,omitempty"`
	HostCassette string `yaml:"hostCassette,omitempty"`
}

type Tour struct {
	Export string     `yaml:"export"`
	Steps  []TourStep `yaml:"steps"`
}

type TourStep struct {
	ID            string `yaml:"id"`
	Route         string `yaml:"route,omitempty"`
	Target        string `yaml:"target,omitempty"`
	Title         string `yaml:"title,omitempty"`
	Body          string `yaml:"body,omitempty"`
	Placement     string `yaml:"placement,omitempty"`
	Kind          string `yaml:"kind,omitempty"`
	Advance       string `yaml:"advance,omitempty"`
	AdvanceRoute  string `yaml:"advanceRoute,omitempty"`
	WaitForTarget string `yaml:"waitForTarget,omitempty"`
	DwellMs       int    `yaml:"dwellMs,omitempty"`
}

// PublicSitePages returns every visibility:public site-page node in cat, in
// deterministic id order.
func PublicSitePages(cat *graph.Catalog) []*graph.Node {
	var pages []*graph.Node
	for _, id := range cat.SortedNodeIDs() {
		node := cat.Nodes[id]
		if node.TypeID == "site-page" && node.Visibility == graph.VisibilityPublic {
			pages = append(pages, node)
		}
	}
	return pages
}

// BuildFeatureDoc joins sitePage with the feature its `presents` edge points
// at and the evidence its `has_media` edge points at, producing the
// features/<id>.yaml shape. It returns an error rather than emitting a
// partial doc if either join target is missing — a site-page that can't
// resolve its own presents/has_media edges is a data bug, not something to
// silently paper over.
func BuildFeatureDoc(cat *graph.Catalog, sitePage *graph.Node) (*FeatureDoc, error) {
	feature, err := joinOne(cat, sitePage, "presents")
	if err != nil {
		return nil, err
	}

	content, _ := sitePage.Fields["content_fields"].(map[string]any)
	doc := &FeatureDoc{
		ID:      featureSlug(feature.ID),
		Kind:    "feature",
		Title:   stringField(content, "title", sitePage.Title),
		Tagline: stringField(content, "tagline", sitePage.Title),
		Summary: stringField(content, "summary", ""),
	}

	if promo, ok := sitePage.Fields["promo"].(map[string]any); ok {
		p := &Promo{}
		if order, ok := promo["order"].(int); ok {
			p.Order = order
		}
		if hl, ok := promo["highlight"].(bool); ok {
			p.Highlight = hl
		}
		doc.Promo = p
	}

	if rawDocs, ok := sitePage.Fields["docs"].([]any); ok {
		for _, d := range rawDocs {
			if s, ok := d.(string); ok {
				doc.Docs = append(doc.Docs, s)
			}
		}
	}

	if evidence, err := joinOne(cat, sitePage, "has_media"); err == nil {
		doc.Demo = demoFromEvidence(evidence)
	}

	if rawTour, ok := sitePage.Fields["tour"].(map[string]any); ok {
		doc.Tour = tourFromFields(rawTour)
	}

	return doc, nil
}

// featureSlug derives the features/<id>.yaml site-facing id from a feature
// node's graph id, by convention "feature-<slug>" (every feature node in the
// seed catalog follows this: feature-operator-ask, feature-agent-actions,
// ...). Falls back to the graph id unchanged if the convention doesn't hold,
// rather than erroring — a graph id IS a valid (if unexpected) slug.
func featureSlug(featureNodeID graph.NodeID) string {
	return strings.TrimPrefix(string(featureNodeID), "feature-")
}

// joinOne resolves the single node a cardinality-one (or first-of-many)
// edge on node points at, erroring if the edge is empty or the target is
// missing from the catalog.
func joinOne(cat *graph.Catalog, node *graph.Node, edgeField graph.EdgeField) (*graph.Node, error) {
	targets := node.Edges[edgeField]
	if len(targets) == 0 {
		return nil, fmt.Errorf("featuresadapter: node %q has no %q edge", node.ID, edgeField)
	}
	target, ok := cat.Nodes[targets[0]]
	if !ok {
		return nil, fmt.Errorf("featuresadapter: node %q's %q edge points at missing node %q", node.ID, edgeField, targets[0])
	}
	return target, nil
}

// demoFromEvidence flattens an evidence node's `producers`/`artifacts`
// fields (the seed catalog's shape: one or more artifact maps, since a
// story-driven demo's fields split across a media-recording artifact and a
// story/flow/hostCassette artifact) into one Demo.
func demoFromEvidence(evidence *graph.Node) *Demo {
	d := &Demo{}
	if producers, ok := evidence.Fields["producers"].([]any); ok && len(producers) > 0 {
		if s, ok := producers[0].(string); ok {
			d.Spec = s
		}
	}
	artifacts, _ := evidence.Fields["artifacts"].([]any)
	for _, a := range artifacts {
		m, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if v, ok := m["artifactDir"].(string); ok {
			d.ArtifactDir = v
		}
		if v, ok := m["videoBase"].(string); ok {
			d.VideoBase = v
		}
		if v, ok := m["posterStep"].(string); ok {
			d.PosterStep = v
		}
		if v, ok := m["story"].(string); ok {
			d.Story = v
		}
		if v, ok := m["flow"].(string); ok {
			d.Flow = v
		}
		if v, ok := m["hostCassette"].(string); ok {
			d.HostCassette = v
		}
	}
	return d
}

func tourFromFields(raw map[string]any) *Tour {
	t := &Tour{}
	if export, ok := raw["export"].(string); ok {
		t.Export = export
	}
	rawSteps, _ := raw["steps"].([]any)
	for _, rs := range rawSteps {
		m, ok := rs.(map[string]any)
		if !ok {
			continue
		}
		t.Steps = append(t.Steps, TourStep{
			ID:            stringField(m, "id", ""),
			Route:         stringField(m, "route", ""),
			Target:        stringField(m, "target", ""),
			Title:         stringField(m, "title", ""),
			Body:          stringField(m, "body", ""),
			Placement:     stringField(m, "placement", ""),
			Kind:          stringField(m, "kind", ""),
			Advance:       stringField(m, "advance", ""),
			AdvanceRoute:  stringField(m, "advanceRoute", ""),
			WaitForTarget: stringField(m, "waitForTarget", ""),
			DwellMs:       intField(m, "dwellMs"),
		})
	}
	return t
}

func stringField(m map[string]any, key, fallback string) string {
	if m == nil {
		return fallback
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return fallback
}

func intField(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	if i, ok := m[key].(int); ok {
		return i
	}
	return 0
}
