// Package host implements the host invocation registry (§2).
//
// Handlers are registered at process start and invoked from the effect executor
// in internal/machine/ when an effect declares invoke: host.<name>.
//
// # Handler interface
//
// A Handler is a plain function: (ctx, args) -> (Result, error).
// The Result.Data map is bound into world slots per the effect's bind: spec.
// Result.Error is for expected, application-level errors (distinguished from
// infrastructure failures which return a non-nil Go error).
//
// # Auth / secrets
//
// Secrets are loaded from env and ~/.hally/secrets.yaml at registry creation
// time and injected into every handler call via context.
//
// # Allow-list enforcement
//
// Apps declare required hosts in a top-level `hosts:` section. The loader
// validates that every invoke: host.* matches the allow-list, and the
// registry validates at startup that every declared host is registered.
package host

import (
	"context"
	"fmt"
	"sync"
)

// Handler is the callable unit for a host invocation.
// args are the template-resolved values from the effect's with: block.
// Returns (Result, nil) on success or expected-error; returns (Result{}, err) on infra failure.
type Handler func(ctx context.Context, args map[string]any) (Result, error)

// Result is the structured return from a Handler.
type Result struct {
	// Data is bound into world/proposal per the effect's bind: spec.
	Data map[string]any
	// Error is non-empty when the handler encountered an expected, domain-level error.
	// Infra failures are returned as Go errors instead.
	Error string
}

// Registry holds the set of registered Handler functions, keyed by name.
// Names should be dot-separated, e.g. "host.workspace_manager.get".
// The registry is safe for concurrent reads after initialization.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewRegistry creates a new empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]Handler),
	}
}

// Register adds a handler under the given name.
// Panics if a handler with that name is already registered (init-time contract).
func (r *Registry) Register(name string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[name]; exists {
		panic(fmt.Sprintf("host: handler %q already registered", name))
	}
	r.handlers[name] = h
}

// Get returns the handler for the given name.
// Returns (nil, false) if not found.
func (r *Registry) Get(name string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[name]
	return h, ok
}

// ValidateAllowList checks that every name in the allow-list is registered.
// Returns an error listing all missing handlers.
func (r *Registry) ValidateAllowList(allowList []string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var missing []string
	for _, name := range allowList {
		if _, ok := r.handlers[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("host: unregistered hosts declared in app manifest: %v", missing)
	}
	return nil
}

// Invoke calls the named handler with the provided args.
// Returns ErrHostNotFound if no handler is registered under name.
func (r *Registry) Invoke(ctx context.Context, name string, args map[string]any) (Result, error) {
	h, ok := r.Get(name)
	if !ok {
		return Result{}, fmt.Errorf("host: no handler registered for %q", name)
	}
	return h(ctx, args)
}

// ErrHostNotFound is returned when the registry has no handler for a name.
var ErrHostNotFound = fmt.Errorf("host: handler not found")

// secretsKey is the context key for injected secrets.
type secretsKey struct{}

// WithSecrets injects a secrets map into a context for use by handlers.
func WithSecrets(ctx context.Context, secrets map[string]string) context.Context {
	return context.WithValue(ctx, secretsKey{}, secrets)
}

// SecretsFromContext retrieves the secrets map from a context.
// Returns nil if no secrets were injected.
func SecretsFromContext(ctx context.Context) map[string]string {
	if v, ok := ctx.Value(secretsKey{}).(map[string]string); ok {
		return v
	}
	return nil
}
