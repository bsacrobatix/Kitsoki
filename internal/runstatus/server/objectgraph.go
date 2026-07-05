// objectgraph.go — the runstatus.objectgraph.* RPC family (W5.0). Serves a
// project object graph catalog (internal/graph, the W1.0/W1.1 substrate) as
// the renderer-neutral kitsoki.graph/v1 wire shape (internal/app/graph),
// so the same viewer that renders story room graphs
// (runstatus.editor.graph) can render project-object-graph catalogs too.
//
// Unlike the editor.* family this does not resolve a story from the
// session registry — a catalog path is just loaded directly, since a
// project object graph catalog is not tied to a running story/session.
package server

import (
	appgraph "kitsoki/internal/app/graph"
	objectgraph "kitsoki/internal/graph"
)

// dispatchObjectGraph handles the runstatus.objectgraph.* method family. It
// returns (result, nil, true) when it handled the method, or (nil, nil,
// false) when the method is not one of this family's, so the caller can
// fall through to the next dispatcher.
func (s *Server) dispatchObjectGraph(method string, params map[string]any) (any, *rpcError, bool) {
	switch method {
	// runstatus.objectgraph.load {catalog_path} → kitsoki.graph/v1 graph
	case "runstatus.objectgraph.load":
		catalogPath, _ := params["catalog_path"].(string)
		if catalogPath == "" {
			return nil, &rpcError{Code: codeServerError, Message: "objectgraph.load: missing 'catalog_path'"}, true
		}
		cat, err := objectgraph.LoadCatalog(catalogPath)
		if err != nil {
			return nil, &rpcError{Code: codeServerError, Message: "objectgraph.load: " + err.Error()}, true
		}
		return appgraph.ObjectCatalogGraph(cat, "objectgraph:"+catalogPath), nil, true

	// runstatus.objectgraph.diff {catalog_path, overlay_path} → kitsoki.graph/v1
	// graph with a diff_kind attr per node (added/modified/removed/unchanged) —
	// diff mode's data source. catalog_path is "current"; catalog_path loaded
	// with overlay_path unioned in (objectgraph.LoadCatalogWithOverlay) is
	// "desired".
	case "runstatus.objectgraph.diff":
		catalogPath, _ := params["catalog_path"].(string)
		overlayPath, _ := params["overlay_path"].(string)
		if catalogPath == "" {
			return nil, &rpcError{Code: codeServerError, Message: "objectgraph.diff: missing 'catalog_path'"}, true
		}
		if overlayPath == "" {
			return nil, &rpcError{Code: codeServerError, Message: "objectgraph.diff: missing 'overlay_path'"}, true
		}
		current, err := objectgraph.LoadCatalog(catalogPath)
		if err != nil {
			return nil, &rpcError{Code: codeServerError, Message: "objectgraph.diff: " + err.Error()}, true
		}
		desired, err := objectgraph.LoadCatalogWithOverlay(catalogPath, overlayPath)
		if err != nil {
			return nil, &rpcError{Code: codeServerError, Message: "objectgraph.diff: " + err.Error()}, true
		}
		return appgraph.ObjectCatalogDiffGraph(current, desired, "objectgraph-diff:"+catalogPath+"+"+overlayPath), nil, true

	default:
		return nil, nil, false
	}
}
