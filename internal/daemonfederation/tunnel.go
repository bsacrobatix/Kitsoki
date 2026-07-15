package daemonfederation

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Process is the minimal supervised-process contract used by TunnelManager.
type Process interface {
	Start() error
	Wait() error
}

// ProcessFactory creates a process without starting it, allowing tunnel
// supervision to be tested without opening network connections.
type ProcessFactory func(context.Context, string, []string, io.Writer) Process

// OSProcessFactory creates an OpenSSH process backed by os/exec.
func OSProcessFactory(ctx context.Context, name string, args []string, stderr io.Writer) Process {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = stderr
	return cmd
}

// TunnelManager supervises configured SSH forwards until Close or parent
// cancellation. Start and Close must not be called concurrently.
type TunnelManager struct {
	Workers       []Worker
	Factory       ProcessFactory
	SSHBinary     string
	RetryInterval time.Duration

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Start launches supervisors for tunneled workers. Repeated calls before Close
// are no-ops.
func (m *TunnelManager) Start(parent context.Context) {
	if m.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	for _, worker := range m.Workers {
		if worker.Tunnel == nil {
			continue
		}
		m.wg.Add(1)
		go m.supervise(ctx, *worker.Tunnel)
	}
}

// Close cancels all supervisors and waits for their processes to exit. Calling
// Close before Start or more than once is safe.
func (m *TunnelManager) Close() {
	if m.cancel == nil {
		return
	}
	m.cancel()
	m.wg.Wait()
	m.cancel = nil
}

func (m *TunnelManager) supervise(ctx context.Context, tunnel Tunnel) {
	defer m.wg.Done()
	factory := m.Factory
	if factory == nil {
		factory = OSProcessFactory
	}
	binary := m.SSHBinary
	if binary == "" {
		binary = "ssh"
	}
	retry := m.RetryInterval
	if retry <= 0 {
		retry = 2 * time.Second
	}
	for ctx.Err() == nil {
		stderr := &boundedWriter{limit: 4096}
		process := factory(ctx, binary, tunnel.SSHArgs(), stderr)
		if err := process.Start(); err == nil {
			_ = process.Wait()
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type boundedWriter struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.limit {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.limit:]...)
	}
	return n, nil
}
