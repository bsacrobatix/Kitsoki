package graph

import "fmt"

// FeedbackNodeSpec describes the single "added" node a feedback report is
// projected into when a catalog sink routes it toward a review queue. Two
// carriers share it (U2, feedback report 01KXD2300AEDR4DKBVGQY7PJT2):
//
//   - the MCP server's feedback.report catalog sink
//     (internal/mcp/graphsrv.routeFeedbackToCatalogSink), which derives
//     Type/Fields from the catalog's own `feedback_routing:` block, and
//   - the web server's POST /api/feedback/local intake
//     (internal/runstatus/server), which derives them from the consumer
//     repo's producer-keyed `feedback_routing:` kitsoki config.
//
// The target Type always lives in the CONSUMER's catalog — kitsoki itself
// never declares feedback node types; routing merely names one.
type FeedbackNodeSpec struct {
	// Type is the node type id to propose (e.g. "portal-feedback").
	Type string
	// Fields lists field ids of the target type to populate, in the
	// taxonomy's declared order. See FeedbackNodeAfter for the mapping.
	Fields []string
	// NodeID is the fresh id for the new node (never a changeset's own
	// cs-<n>, which Propose mints itself).
	NodeID string
	// Title is the human-readable one-liner carried by the node.
	Title string
	// ReportRef is the durable dereference back to the JSONL/markdown
	// record (a report id or idempotency key).
	ReportRef string
}

// FeedbackNodeAfter builds the flat `after` mapping for a feedback node's
// "added" operation. After is a flat mapping (schema/id/title/status/
// visibility + type-specific fields), never a nested {type,fields,edges}
// envelope — see buildNode/fileNode.
//
// Field mapping (judgment call — a routing config names field IDs only,
// never a value template): the FIRST declared field carries the report's
// title/summary text, so any taxonomy gets at least a human-readable line;
// a field literally named "report" or "report_id" (if the taxonomy declares
// one) additionally gets ReportRef verbatim, for a durable dereference back
// to the JSONL/markdown record.
//
// Edge targets are deliberately omitted: routing configs name edge FIELD
// ids only, never a target node id, and nothing in a feedback submission
// reliably supplies one. Writing a stub edge with a hallucinated or empty
// target would propose bad data into the review queue; instead the node
// lands for human review with its declared field(s) populated, and if the
// target type requires an edge, lint surfaces it as a normal, honest
// validation gap on the proposal.
func FeedbackNodeAfter(spec FeedbackNodeSpec) map[string]any {
	after := map[string]any{
		"schema":     fmt.Sprintf("graph/%s/v0", spec.Type),
		"id":         spec.NodeID,
		"title":      spec.Title,
		"status":     "draft",
		"visibility": "internal",
	}
	for i, f := range spec.Fields {
		if i == 0 {
			after[f] = spec.Title
		}
		if f == "report" || f == "report_id" {
			after[f] = spec.ReportRef
		}
	}
	return after
}
