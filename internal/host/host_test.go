package host_test

import (
	"context"
	"testing"

	"hally/internal/host"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := host.NewRegistry()
	called := false
	r.Register("host.test", func(ctx context.Context, args map[string]any) (host.Result, error) {
		called = true
		return host.Result{Data: map[string]any{"echo": args["msg"]}}, nil
	})

	h, ok := r.Get("host.test")
	if !ok {
		t.Fatal("expected handler to be registered")
	}

	result, err := h(context.Background(), map[string]any{"msg": "hello"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
	if result.Data["echo"] != "hello" {
		t.Fatalf("expected echo=hello, got %v", result.Data["echo"])
	}
}

func TestRegistry_DuplicatePanics(t *testing.T) {
	r := host.NewRegistry()
	r.Register("host.dup", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{}, nil
	})
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r.Register("host.dup", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{}, nil
	})
}

func TestRegistry_NotFound(t *testing.T) {
	r := host.NewRegistry()
	_, err := r.Invoke(context.Background(), "host.missing", nil)
	if err == nil {
		t.Fatal("expected error for missing handler")
	}
}

func TestRegistry_ValidateAllowList(t *testing.T) {
	r := host.NewRegistry()
	r.Register("host.a", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{}, nil
	})

	// Passes when all declared hosts are registered.
	if err := r.ValidateAllowList([]string{"host.a"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Fails when a declared host is not registered.
	if err := r.ValidateAllowList([]string{"host.a", "host.missing"}); err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestSecretsContext(t *testing.T) {
	ctx := context.Background()
	secrets := map[string]string{"API_KEY": "abc123"}
	ctx = host.WithSecrets(ctx, secrets)

	got := host.SecretsFromContext(ctx)
	if got["API_KEY"] != "abc123" {
		t.Fatalf("expected API_KEY=abc123, got %v", got)
	}
}

func TestRunHandler(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)

	result, err := r.Invoke(context.Background(), "host.run", map[string]any{
		"cmd": "echo hello",
	})
	if err != nil {
		t.Fatalf("host.run error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("host.run domain error: %v", result.Error)
	}
	stdout, _ := result.Data["stdout"].(string)
	if stdout == "" {
		t.Fatal("expected non-empty stdout")
	}
	ok, _ := result.Data["ok"].(bool)
	if !ok {
		t.Fatal("expected ok=true")
	}
}

func TestRunHandler_MissingCmd(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)

	result, err := r.Invoke(context.Background(), "host.run", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected infra error: %v", err)
	}
	if result.Error == "" {
		t.Fatal("expected domain error for missing cmd")
	}
}

func TestRunHandler_NonZeroExit(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)

	result, err := r.Invoke(context.Background(), "host.run", map[string]any{
		"cmd": "exit 1",
	})
	if err != nil {
		t.Fatalf("host.run infra error: %v", err)
	}
	ok, _ := result.Data["ok"].(bool)
	if ok {
		t.Fatal("expected ok=false for non-zero exit")
	}
	exitCode, _ := result.Data["exit_code"].(int)
	if exitCode != 1 {
		t.Fatalf("expected exit_code=1, got %v", exitCode)
	}
}
