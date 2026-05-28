// Package oracle — registry tests.
package oracle

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestRegistry_RegisterAndGet verifies basic registration and retrieval.
func TestRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()

	// Use a closingOracle (pointer-based) so identity comparison works.
	o := &closingOracle{}
	reg.Register("oracle.claude", o)

	got, ok := reg.Get("oracle.claude")
	if !ok {
		t.Fatal("Get: oracle.claude not found")
	}
	// Compare by calling Ask on both — same pointer means same oracle.
	if got != o {
		t.Error("Get: returned different oracle than registered")
	}
}

// TestRegistry_DuplicatePanics verifies that registering the same name twice
// panics.
func TestRegistry_DuplicatePanics(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	o := New(AskFunc(func(_ context.Context, _ AskRequest) (AskResponse, error) {
		return AskResponse{}, nil
	}))
	reg.Register("oracle.claude", o)

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate registration, got none")
		}
	}()
	reg.Register("oracle.claude", o)
}

// TestRegistry_ResolveDefault verifies that Resolve("") falls back to
// "oracle.claude".
func TestRegistry_ResolveDefault(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	o := &closingOracle{}
	reg.Register("oracle.claude", o)

	got, err := reg.Resolve("")
	if err != nil {
		t.Fatalf("Resolve(''): unexpected error: %v", err)
	}
	if got != o {
		t.Error("Resolve(''): returned different oracle than registered")
	}
}

// TestRegistry_ResolveUnknown verifies that Resolve returns an error for
// unknown names with no fallback.
func TestRegistry_ResolveUnknown(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	// No oracle registered.

	_, err := reg.Resolve("oracle.nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown oracle, got nil")
	}
	if !strings.Contains(err.Error(), "oracle.nonexistent") {
		t.Errorf("error should mention the name; got: %v", err)
	}
}

// TestRegistry_ResolveNamedFallsBackToDefault verifies that a named oracle
// that's absent falls back to oracle.claude when it exists.
func TestRegistry_ResolveNamedFallsBackToDefault(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	defaultOracle := &closingOracle{}
	reg.Register("oracle.claude", defaultOracle)

	// Resolve a name that doesn't exist; should fall back to oracle.claude.
	got, err := reg.Resolve("oracle.nonexistent")
	if err != nil {
		t.Fatalf("expected fallback to default, got error: %v", err)
	}
	if got != defaultOracle {
		t.Error("fallback: did not return oracle.claude")
	}
}

// TestRegistry_Close closes all oracles without error.
func TestRegistry_Close(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	var closed1, closed2 bool
	makeCloser := func(flag *bool) Oracle {
		return closingOracle{flag: flag}
	}
	reg.Register("oracle.a", makeCloser(&closed1))
	reg.Register("oracle.b", makeCloser(&closed2))

	if err := reg.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
	if !closed1 {
		t.Error("oracle.a was not closed")
	}
	if !closed2 {
		t.Error("oracle.b was not closed")
	}
}

// TestPluginNotSupportedError verifies the error message.
func TestPluginNotSupportedError(t *testing.T) {
	t.Parallel()
	err := &PluginNotSupportedError{Plugin: "mcp_http"}
	if !strings.Contains(err.Error(), "mcp_http") {
		t.Errorf("error message should mention plugin name; got: %v", err)
	}
	if !strings.Contains(err.Error(), "B-3") {
		t.Errorf("error message should mention B-3; got: %v", err)
	}
}

// TestRegistry_ConcurrentAccess verifies the registry is safe for concurrent
// read after setup.
func TestRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	o := &closingOracle{}
	reg.Register("oracle.claude", o)
	reg.Register("oracle.b", o)

	const n = 20
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			got, err := reg.Resolve("oracle.claude")
			if err != nil {
				errs <- err
				return
			}
			if got != o {
				errs <- errors.New("got unexpected oracle")
				return
			}
			errs <- nil
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Resolve: %v", err)
		}
	}
}

// closingOracle is an Oracle that tracks whether Close was called.
type closingOracle struct {
	flag *bool
}

func (c closingOracle) Ask(_ context.Context, _ AskRequest) (AskResponse, error) {
	return AskResponse{}, nil
}

func (c closingOracle) Close() error {
	if c.flag != nil {
		*c.flag = true
	}
	return nil
}
