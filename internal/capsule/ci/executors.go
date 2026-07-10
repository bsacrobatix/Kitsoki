package ci

import (
	"context"
	"fmt"

	"kitsoki/internal/capsule/executor"
)

// BuiltinExecutors is the no-credential local executor catalog. A production
// remote worker is injected by the embedding process under a project-approved
// name; it is never inferred from story input.
type BuiltinExecutors struct {
	Host       executor.Provider
	FakeRemote executor.Provider
}

func NewBuiltinExecutors() BuiltinExecutors {
	return BuiltinExecutors{
		Host:       executor.NewHostProvider(),
		FakeRemote: executor.NewRemoteProvider(executor.NewFakeRemoteWorker()),
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
	}
	return nil, fmt.Errorf("capsule ci: executor %q is not configured", name)
}

var _ ExecutorSelector = BuiltinExecutors{}
