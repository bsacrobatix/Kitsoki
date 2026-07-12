// graph_tools.go mounts the graph.*/feedback.* tool family (internal/mcp/
// graphsrv) onto the studio server — the graph-mcp plan's §3.1 "studio
// second door": "the identical family is built by shared registerGraphTools
// (srv, deps, mode) / registerFeedbackTools(srv, deps) functions also
// called by the studio server ... so a human's Claude Code session gets
// mcp__kitsoki__graph.* beside story/vcs tools with zero drift."
//
// The catalog-arg schema on tools mounted here is IDENTICAL to the
// standalone `mcp-graph` server's: alias-only `catalog?: <alias>`, no raw
// `catalog_path` fork. The plan's "studio-mounted copies accept
// catalog_path" aside (§3.2) is treated as non-binding/aspirational for
// this stage — a second arg shape per tool would double the schema surface
// this plan is explicitly trying to keep small (§3.1's tools/list economics
// rationale), for a "that audience has file tools anyway" convenience this
// stage doesn't need to buy.
package studio

import (
	"kitsoki/internal/clock"
	"kitsoki/internal/host"
	"kitsoki/internal/mcp/graphsrv"
)

// WithGraphCatalogs configures the studio-mounted graph.*/feedback.* tool
// family's --catalog bindings. specs mirrors mcp-graph's own repeatable
// --catalog flag: each entry is "[alias=]path"; the first becomes the
// default catalog. Omitting this option (or passing no specs, and no
// pog/catalog.yaml under cwd) leaves the graph family bound to zero
// catalogs — every graph/feedback tool call then reports NO_CATALOG rather
// than the tool family being absent from tools/list.
func WithGraphCatalogs(specs []string) ServerOption {
	return func(s *Server) { s.graphCatalogSpecs = specs }
}

// WithGraphSteward opts the studio-mounted graph family into steward mode
// (graph.authorize + a real, non-dry-run graph.apply). Default false =
// propose mode: the read family, graph.propose/withdraw-own/changeset, and
// graph.apply(dry_run:true) only. This is the plan's red-team amendment
// ("studio second door"): authorize/live-apply need an explicit opt-in on
// BOTH construction sites (mcp-graph's own --mode steward, and this flag)
// — the gate must exist on both or it exists on neither, since sub-agents
// auto-attach the studio server via the mcp__kitsoki__* naming convention.
func WithGraphSteward(steward bool) ServerOption {
	return func(s *Server) { s.graphSteward = steward }
}

// WithGraphActor sets the actor name stamped on studio-mounted graph
// write-tool calls (authored_by/authorized_by, withdraw-own checks) —
// mirrors mcp-graph's --actor.
func WithGraphActor(actor string) ServerOption {
	return func(s *Server) { s.graphActor = actor }
}

// WithGraphFeedbackSink sets the studio-mounted feedback.report's sink
// (local|catalog|github), mirroring mcp-graph's --feedback-sink. Empty
// keeps the local-only default.
func WithGraphFeedbackSink(sink string) ServerOption {
	return func(s *Server) { s.graphFeedbackSink = sink }
}

// WithGraphWriteVia sets the studio-mounted graph write family's write
// routing (auto|direct|capsule), mirroring mcp-graph's --write-via. Empty
// keeps auto: each bound catalog's route comes from its own repo's
// .kitsoki/project-profile.yaml `graph: {write_via: ...}` block, defaulting
// to direct when absent.
func WithGraphWriteVia(via string) ServerOption {
	return func(s *Server) { s.graphWriteVia = via }
}

// WithGraphIssueFiler injects the GitHub issue-filing seam the
// studio-mounted feedback.report uses for --feedback-sink github.
// Independent of WithIssueFiler (issue.create's own seam) — see
// graphsrv.IssueFiler's doc comment for why graphsrv defines its own type
// rather than importing this package's IssueFiler.
func WithGraphIssueFiler(f graphsrv.IssueFiler) ServerOption {
	return func(s *Server) { s.graphIssueFiler = f }
}

// registerGraphTools mounts the graph.*/feedback.* family on the studio
// server (graph-mcp plan §3.1, P6). Called unconditionally from NewServer
// (see the call site's doc comment) — a malformed --catalog spec degrades
// to a zero-catalog binding (every call then reports NO_CATALOG) rather
// than failing studio server construction over a family most studio
// sessions never touch; mcp-graph itself validates --catalog at its own
// cobra flag layer before construction, which the studio mount has no
// equivalent hook for.
func (srv *Server) registerGraphTools() {
	catalogs, err := graphsrv.ParseCatalogFlags(srv.graphCatalogSpecs)
	if err != nil {
		catalogs = &graphsrv.CatalogSet{}
	}

	mode := graphsrv.ModePropose
	if srv.graphSteward {
		mode = graphsrv.ModeSteward
	}

	sink := srv.graphFeedbackSink
	if sink == "" {
		sink = graphsrv.FeedbackSinkLocal
	}

	// A dedicated host.Registry, independent of any registry the legacy
	// toolbox/operating-system plane uses — the graph family's host.graph.*
	// ops need only the builtins, and giving it its own registry keeps this
	// mount free of ordering dependencies on the rest of NewServer.
	registry := host.NewRegistry()
	host.RegisterBuiltins(registry)

	via := srv.graphWriteVia
	if via == "" {
		via = graphsrv.DefaultWriteVia
	}
	if graphsrv.ValidateWriteVia(via) != nil {
		// Mirror the malformed --catalog degrade above: an invalid value
		// must not fail studio server construction over this family.
		via = graphsrv.DefaultWriteVia
	}

	deps := &graphsrv.Deps{
		Registry:     registry,
		Catalogs:     catalogs,
		Mode:         mode,
		Actor:        srv.graphActor,
		FeedbackSink: sink,
		Clock:        clock.Real(),
		Recorder:     graphsrv.NewRecorder(),
		IssueFiler:   srv.graphIssueFiler,
		Router:       graphsrv.NewWriteRouter(via, nil),
	}

	graphsrv.RegisterGraphTools(srv.mcpSrv, deps, mode)
	graphsrv.RegisterFeedbackTools(srv.mcpSrv, deps)
}
