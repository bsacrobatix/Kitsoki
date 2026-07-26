package studio

import (
	"context"
	"testing"
	"time"
)

func TestSessionRuntimeApplicationInterruptUsesActiveTurnCancel(t *testing.T) {
	turnCtx, cancel := context.WithCancel(context.Background())
	runtime := &sessionRuntime{activeTurnCancel: cancel}
	if !runtime.interruptActiveTurn() {
		t.Fatal("interruptActiveTurn reported no active turn")
	}
	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("application interrupt did not cancel the active turn context")
	}
}
