package daemonfederation

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

type fakeProcess struct {
	started chan struct{}
	done    <-chan struct{}
}

func (p *fakeProcess) Start() error {
	close(p.started)
	return nil
}

func (p *fakeProcess) Wait() error {
	<-p.done
	return nil
}

func TestTunnelManagerStartsAndStopsManagedTunnel(t *testing.T) {
	var mu sync.Mutex
	var name string
	var args []string
	started := make(chan struct{})
	manager := &TunnelManager{
		Workers: []Worker{{Tunnel: &Tunnel{Host: "vm", LocalPort: 17777, RemoteHost: "127.0.0.1", RemotePort: 7777, IdentityFile: "/key", KnownHostsFile: "/known"}}},
		Factory: func(ctx context.Context, gotName string, gotArgs []string, _ io.Writer) Process {
			mu.Lock()
			name = gotName
			args = append([]string(nil), gotArgs...)
			mu.Unlock()
			return &fakeProcess{started: started, done: ctx.Done()}
		},
		RetryInterval: time.Hour,
	}
	manager.Start(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tunnel did not start")
	}
	manager.Close()
	mu.Lock()
	defer mu.Unlock()
	if name != "ssh" || len(args) == 0 {
		t.Fatalf("launch = %q %#v", name, args)
	}
}
