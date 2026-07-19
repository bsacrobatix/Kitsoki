package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"sync"
)

// HostLauncher starts only declared argv. It uses a separate process group so
// Stop never targets a listener selected by port or by ambient process name.
type HostLauncher struct {
	mu        sync.Mutex
	processes map[string]*hostProcess
}

func NewHostLauncher() *HostLauncher { return &HostLauncher{processes: map[string]*hostProcess{}} }
func (l *HostLauncher) Start(ctx context.Context, spec ProcessSpec) (Process, error) {
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return nil, fmt.Errorf("capsule runtime: argv is required")
	}
	cmd := exec.CommandContext(ctx, spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Directory
	cmd.Env = hostEnv(spec.Env)
	configureProcessGroup(cmd)
	p := &hostProcess{id: spec.RuntimeID + "-" + spec.Service + "-" + strconv.FormatUint(spec.Generation, 10), cmd: cmd}
	cmd.Stdout = &p.logs
	cmd.Stderr = &p.logs
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.processes[p.id] = p
	l.mu.Unlock()
	go func() { _ = cmd.Wait() }()
	return p, nil
}
func (l *HostLauncher) Process(id string) (Process, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.processes[id]
	return p, ok
}
func hostEnv(named map[string]string) []string {
	out := append([]string(nil), os.Environ()...)
	keys := make([]string, 0, len(named))
	for k := range named {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+named[k])
	}
	return out
}

type hostProcess struct {
	id   string
	cmd  *exec.Cmd
	logs bytes.Buffer
	mu   sync.Mutex
}

func (p *hostProcess) ID() string { return p.id }
func (p *hostProcess) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd.Process == nil {
		return nil
	}
	return stopProcessGroup(ctx, p.cmd)
}
func (p *hostProcess) Logs(context.Context) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.logs.Bytes()...), nil
}
