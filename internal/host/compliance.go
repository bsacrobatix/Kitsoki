package host

import "context"

// ComplianceHandler is the builtin registration sentinel. Session runtimes
// replace it with the app-scoped provider from internal/compliance.
var ComplianceHandler Handler = func(context.Context, map[string]any) (Result, error) {
	return Result{Error: "host.compliance.run: app-scoped compliance provider is unavailable"}, nil
}
