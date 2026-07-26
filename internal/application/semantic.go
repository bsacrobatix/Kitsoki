package application

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type SemanticState struct {
	FrameRevision uint64    `json:"frame_revision"`
	State         NodeState `json:"state,omitempty"`
}

type SemanticInspection struct {
	Node     SemanticNode   `json:"node"`
	Current  SemanticState  `json:"current"`
	Incoming []Relationship `json:"incoming,omitempty"`
	Outgoing []Relationship `json:"outgoing,omitempty"`
}

type semanticEntry struct {
	node  SemanticNode
	state NodeState
}

func (f Frame) Validate() error {
	if f.Schema != FrameSchema {
		return fmt.Errorf("application: frame schema %q, want %q", f.Schema, FrameSchema)
	}
	if strings.TrimSpace(f.ApplicationID) == "" {
		return fmt.Errorf("application: application_id is required")
	}
	if strings.TrimSpace(f.SessionID) == "" {
		return fmt.Errorf("application: session_id is required")
	}
	if strings.TrimSpace(f.Page) == "" {
		return fmt.Errorf("application: page is required")
	}
	for name, data := range f.Data {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("application: frame data name is required")
		}
		switch data.Sensitivity {
		case "public", "internal", "sensitive", "secret":
		default:
			return fmt.Errorf("application: frame data %q has invalid sensitivity %q", name, data.Sensitivity)
		}
		switch data.Policy {
		case "include", "redact", "hash":
		default:
			return fmt.Errorf("application: frame data %q has invalid policy %q", name, data.Policy)
		}
		if data.Policy == "include" && (data.Sensitivity == "sensitive" || data.Sensitivity == "secret") {
			return fmt.Errorf("application: frame data %q cannot include %s value", name, data.Sensitivity)
		}
		if len(data.Value) == 0 || !json.Valid(data.Value) {
			return fmt.Errorf("application: frame data %q is not valid JSON", name)
		}
	}

	entries := make([]semanticEntry, 0)
	entries = append(entries, semanticEntry{node: f.Semantic})
	entries = append(entries, semanticEntry{node: f.PageSemantic})
	for _, item := range f.Navigation {
		if item.ID == "" || item.Page == "" {
			return fmt.Errorf("application: navigation id and page are required")
		}
		entries = append(entries, semanticEntry{node: item.Semantic, state: item.State})
	}
	for _, page := range f.Pages {
		if page.ID == "" {
			return fmt.Errorf("application: page descriptor id is required")
		}
		entries = append(entries, semanticEntry{node: page.Semantic})
	}
	for _, component := range f.Components {
		if component.ID == "" {
			return fmt.Errorf("application: component descriptor id is required")
		}
		entries = append(entries, semanticEntry{node: component.Semantic})
	}
	for _, handler := range f.Handlers {
		if handler.ID == "" {
			return fmt.Errorf("application: handler descriptor id is required")
		}
		entries = append(entries, semanticEntry{node: handler.Semantic})
	}
	actionIDs := map[string]Action{}
	if err := collectActions(f.Actions, &entries, actionIDs); err != nil {
		return err
	}
	for _, region := range f.Regions {
		if region.ID == "" {
			return fmt.Errorf("application: region id is required")
		}
		entries = append(entries, semanticEntry{node: region.Semantic, state: region.State})
		for _, card := range region.Cards {
			if card.ID == "" {
				return fmt.Errorf("application: card id is required")
			}
			entries = append(entries, semanticEntry{node: card.Semantic, state: card.State})
			if err := collectActions(card.Actions, &entries, actionIDs); err != nil {
				return err
			}
			for _, element := range card.Body {
				if err := collectElement(element, &entries, actionIDs); err != nil {
					return err
				}
			}
		}
	}

	seen := make(map[string]SemanticNode, len(entries))
	for _, entry := range entries {
		if err := validateSemanticNode(entry.node); err != nil {
			return err
		}
		if prior, ok := seen[entry.node.Ref]; ok {
			if !reflect.DeepEqual(prior, entry.node) {
				return fmt.Errorf("application: conflicting semantic ref %q", entry.node.Ref)
			}
			continue
		}
		seen[entry.node.Ref] = entry.node
	}
	for _, entry := range entries {
		for _, relationship := range entry.node.Relationships {
			if relationship.Kind == "" || relationship.Ref == "" {
				return fmt.Errorf("application: semantic ref %q has an incomplete relationship", entry.node.Ref)
			}
			if _, ok := seen[relationship.Ref]; !ok {
				return fmt.Errorf("application: semantic ref %q relates to unknown ref %q", entry.node.Ref, relationship.Ref)
			}
		}
	}
	return nil
}

func collectElement(element Element, entries *[]semanticEntry, actionIDs map[string]Action) error {
	if element.Kind == "" {
		return fmt.Errorf("application: element kind is required")
	}
	if element.Semantic != nil {
		*entries = append(*entries, semanticEntry{node: *element.Semantic, state: element.State})
	}
	if err := collectActions(element.Actions, entries, actionIDs); err != nil {
		return err
	}
	for _, item := range element.Items {
		if err := collectElement(item, entries, actionIDs); err != nil {
			return err
		}
	}
	return nil
}

func collectActions(actions []Action, entries *[]semanticEntry, actionIDs map[string]Action) error {
	for _, action := range actions {
		if action.ID == "" {
			return fmt.Errorf("application: action id is required")
		}
		if prior, ok := actionIDs[action.ID]; ok {
			if !reflect.DeepEqual(prior, action) {
				return fmt.Errorf("application: conflicting action id %q", action.ID)
			}
			*entries = append(*entries, semanticEntry{node: action.Semantic, state: action.State})
			continue
		}
		actionIDs[action.ID] = action
		if (action.Handler == "") == (action.Intent == "") {
			return fmt.Errorf("application: action %q must bind exactly one handler or intent", action.ID)
		}
		*entries = append(*entries, semanticEntry{node: action.Semantic, state: action.State})
	}
	return nil
}

func validateSemanticNode(node SemanticNode) error {
	if strings.TrimSpace(node.Ref) == "" {
		return fmt.Errorf("application: semantic ref is required")
	}
	if _, ok := semanticKinds[node.Kind]; !ok {
		return fmt.Errorf("application: semantic ref %q has invalid kind %q", node.Ref, node.Kind)
	}
	if strings.TrimSpace(node.Name) == "" || strings.TrimSpace(node.Description) == "" {
		return fmt.Errorf("application: semantic ref %q requires name and description", node.Ref)
	}
	if strings.TrimSpace(node.Source.Story) == "" || strings.TrimSpace(node.Source.Member) == "" {
		return fmt.Errorf("application: semantic ref %q requires story and member provenance", node.Ref)
	}
	return nil
}

func (f Frame) Inspect(ref string, relationshipLimit int) (SemanticInspection, bool, error) {
	if err := f.Validate(); err != nil {
		return SemanticInspection{}, false, err
	}
	if relationshipLimit < 0 {
		return SemanticInspection{}, false, fmt.Errorf("application: relationship limit cannot be negative")
	}

	entries := f.semanticEntries()
	entry, ok := entries[ref]
	if !ok {
		return SemanticInspection{}, false, nil
	}
	incoming := make([]Relationship, 0)
	for sourceRef, source := range entries {
		for _, relationship := range source.node.Relationships {
			if relationship.Ref == ref {
				incoming = append(incoming, Relationship{Kind: relationship.Kind, Ref: sourceRef})
			}
		}
	}
	sortRelationships(incoming)
	outgoing := append([]Relationship(nil), entry.node.Relationships...)
	sortRelationships(outgoing)
	if relationshipLimit > 0 {
		if len(incoming) > relationshipLimit {
			incoming = incoming[:relationshipLimit]
		}
		if len(outgoing) > relationshipLimit {
			outgoing = outgoing[:relationshipLimit]
		}
	}
	return SemanticInspection{
		Node:     entry.node,
		Current:  SemanticState{FrameRevision: f.Revision, State: entry.state},
		Incoming: incoming,
		Outgoing: outgoing,
	}, true, nil
}

func sortRelationships(relationships []Relationship) {
	sort.Slice(relationships, func(i, j int) bool {
		if relationships[i].Kind == relationships[j].Kind {
			return relationships[i].Ref < relationships[j].Ref
		}
		return relationships[i].Kind < relationships[j].Kind
	})
}

func (f Frame) semanticEntries() map[string]semanticEntry {
	entries := map[string]semanticEntry{
		f.Semantic.Ref:     {node: f.Semantic},
		f.PageSemantic.Ref: {node: f.PageSemantic},
	}
	for _, item := range f.Navigation {
		entries[item.Semantic.Ref] = semanticEntry{node: item.Semantic, state: item.State}
	}
	for _, page := range f.Pages {
		entries[page.Semantic.Ref] = semanticEntry{node: page.Semantic}
	}
	for _, component := range f.Components {
		entries[component.Semantic.Ref] = semanticEntry{node: component.Semantic}
	}
	for _, handler := range f.Handlers {
		entries[handler.Semantic.Ref] = semanticEntry{node: handler.Semantic}
	}
	addActions(entries, f.Actions)
	for _, region := range f.Regions {
		entries[region.Semantic.Ref] = semanticEntry{node: region.Semantic, state: region.State}
		for _, card := range region.Cards {
			entries[card.Semantic.Ref] = semanticEntry{node: card.Semantic, state: card.State}
			addActions(entries, card.Actions)
			for _, element := range card.Body {
				addElement(entries, element)
			}
		}
	}
	return entries
}

func addElement(entries map[string]semanticEntry, element Element) {
	if element.Semantic != nil {
		entries[element.Semantic.Ref] = semanticEntry{node: *element.Semantic, state: element.State}
	}
	addActions(entries, element.Actions)
	for _, item := range element.Items {
		addElement(entries, item)
	}
}

func addActions(entries map[string]semanticEntry, actions []Action) {
	for _, action := range actions {
		entries[action.Semantic.Ref] = semanticEntry{node: action.Semantic, state: action.State}
	}
}
