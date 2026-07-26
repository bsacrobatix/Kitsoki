package main

import (
	"testing"

	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
)

func TestSessionRegistryExposesSessionApplicationScheduler(t *testing.T) {
	scheduler := jobs.NewInMemoryScheduler()
	hostRegistry := host.NewRegistry()
	registry := &SessionRegistry{
		sessions: map[string]*entry{
			"session-1": {rt: &sessionRuntime{Scheduler: scheduler, HostRegistry: hostRegistry}},
		},
	}
	got, ok := registry.ApplicationEventScheduler("session-1")
	if !ok || got != scheduler {
		t.Fatalf("ApplicationEventScheduler() = %#v, %v", got, ok)
	}
	if _, ok := registry.ApplicationEventScheduler("missing"); ok {
		t.Fatal("missing session reported an application scheduler")
	}
	gotHosts, ok := registry.ApplicationHostRegistry("session-1")
	if !ok || gotHosts != hostRegistry {
		t.Fatalf("ApplicationHostRegistry() = %#v, %v", gotHosts, ok)
	}
}
