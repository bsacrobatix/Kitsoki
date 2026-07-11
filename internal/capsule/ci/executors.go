package ci

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"kitsoki/internal/capsule/executor"
)

// BuiltinExecutors is the no-credential local executor catalog. A production
// remote worker is injected by the embedding process under a project-approved
// name; it is never inferred from story input.
type BuiltinExecutors struct {
	Host          executor.Provider
	Container     executor.Provider
	FakeRemote    executor.Provider
	FakeContainer executor.Provider
}

func NewBuiltinExecutors() BuiltinExecutors {
	return BuiltinExecutors{
		Host:          executor.NewHostProvider(),
		FakeRemote:    executor.NewRemoteProvider(executor.NewFakeRemoteWorker()),
		FakeContainer: executor.NewContainerProvider(executor.NewFakeContainerBackend()),
	}
}

func (e BuiltinExecutors) Select(_ context.Context, name string) (executor.Provider, error) {
	switch name {
	case "", "host", "local":
		if e.Host != nil {
			return e.Host, nil
		}
	case "remote-fake":
		if e.FakeRemote != nil {
			return e.FakeRemote, nil
		}
	case "container":
		if e.Container != nil {
			return e.Container, nil
		}
	case "container-fake":
		if e.FakeContainer != nil {
			return e.FakeContainer, nil
		}
	}
	return nil, fmt.Errorf("capsule ci: executor %q is not configured", name)
}

var _ ExecutorSelector = BuiltinExecutors{}

// ConfiguredExecutors extends the no-credential builtins with project-declared
// remote workers. The checked-in CI file names endpoints and credential env var
// names only; credential values are read at request time and stay out of
// envelopes, traces, and receipts.
type ConfiguredExecutors struct {
	Builtins BuiltinExecutors
	Remotes  map[string]Remote
	Client   *http.Client
}

func NewConfiguredExecutors(cfg Config) ConfiguredExecutors {
	return ConfiguredExecutors{Builtins: NewBuiltinExecutors(), Remotes: cfg.Remotes}
}

func (e ConfiguredExecutors) Select(ctx context.Context, name string) (executor.Provider, error) {
	if isBuiltinExecutor(name) {
		return e.Builtins.Select(ctx, name)
	}
	remote, ok := e.Remotes[name]
	if !ok {
		return nil, fmt.Errorf("capsule ci: executor %q is not configured", name)
	}
	worker := executor.HTTPRemoteWorker{Endpoint: remote.Endpoint, Client: e.Client}
	if remote.CredentialEnv != "" {
		env := remote.CredentialEnv
		worker.Credential = func(context.Context) (string, error) {
			token := os.Getenv(env)
			if token == "" {
				return "", fmt.Errorf("%s is not set", env)
			}
			return token, nil
		}
	}
	return executor.NewRemoteProvider(worker), nil
}

var _ ExecutorSelector = ConfiguredExecutors{}
