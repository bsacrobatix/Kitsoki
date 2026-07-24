// Package playground owns short-lived dev/test processes for managed Capsules.
package playground

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"kitsoki/internal/atomicfile"
)

const DefaultIdleTimeout = 12 * time.Minute

type Lease struct {
	IdleTimeoutSeconds int       `json:"idle_timeout_seconds"`
	LastActivityAt     time.Time `json:"last_activity_at,omitempty"`
}
type Testing struct {
	Instructions []string `json:"instructions"`
	ScenarioIDs  []string `json:"scenario_ids"`
}

// Record is deliberately suitable for a virtual-PR client. SourceSHA is the
// immutable workspace commit; clients must never infer it from a branch name.
type Record struct {
	ID        string   `json:"id"`
	SourceSHA string   `json:"source_sha"`
	Ref       string   `json:"ref,omitempty"`
	State     string   `json:"state"`
	URL       string   `json:"url,omitempty"`
	Lease     Lease    `json:"lease"`
	Testing   Testing  `json:"testing"`
	Port      int      `json:"port,omitempty"`
	PID       int      `json:"pid,omitempty"`
	Workspace string   `json:"workspace,omitempty"`
	Command   []string `json:"command,omitempty"`
	Error     string   `json:"error,omitempty"`
}
type StartRequest struct {
	Project, ID, Workspace, SourceSHA, Ref string
	Command                                []string
	IdleTimeout                            time.Duration
	Testing                                Testing
}

func Root(project string) string     { return filepath.Join(project, ".capsules", "playgrounds") }
func path(project, id string) string { return filepath.Join(Root(project), id+".json") }

func Start(ctx context.Context, r StartRequest) (Record, error) {
	if r.ID == "" || strings.ContainsAny(r.ID, "/\\") || r.Workspace == "" || len(r.Command) == 0 || r.SourceSHA == "" {
		return Record{}, fmt.Errorf("capsule playground: id, workspace, source_sha, and command are required")
	}
	if r.IdleTimeout == 0 {
		r.IdleTimeout = DefaultIdleTimeout
	}
	if r.IdleTimeout < 10*time.Minute || r.IdleTimeout > 15*time.Minute {
		return Record{}, fmt.Errorf("capsule playground: idle timeout must be between 10m and 15m")
	}
	if _, err := os.Stat(filepath.Join(r.Workspace, ".kitsoki-capsule")); err != nil {
		return Record{}, fmt.Errorf("capsule playground: workspace must be a managed Capsule: %w", err)
	}
	if err := os.MkdirAll(Root(r.Project), 0755); err != nil {
		return Record{}, err
	}
	unlock, err := acquireStartLock(r.Project)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	if existing, err := Read(r.Project, r.ID); err == nil && (existing.State == "ready" || existing.State == "starting") && alive(existing.PID) {
		return Touch(r.Project, r.ID)
	}
	port, err := allocatePort(r.Project, r.ID)
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	record := Record{ID: r.ID, SourceSHA: r.SourceSHA, Ref: r.Ref, State: "starting", URL: fmt.Sprintf("http://127.0.0.1:%d", port), Port: port, Workspace: r.Workspace, Command: append([]string(nil), r.Command...), Lease: Lease{IdleTimeoutSeconds: int(r.IdleTimeout.Seconds()), LastActivityAt: now}, Testing: r.Testing}
	if err := write(r.Project, record); err != nil {
		return Record{}, err
	}
	cmd := exec.CommandContext(ctx, r.Command[0], r.Command[1:]...)
	cmd.Dir = r.Workspace
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port), fmt.Sprintf("KITSOKI_PLAYGROUND_PORT=%d", port), "KITSOKI_PLAYGROUND_ID="+r.ID)
	log, err := os.OpenFile(filepath.Join(Root(r.Project), r.ID+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return Record{}, err
	}
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		record.State = "failed"
		record.Error = err.Error()
		_ = write(r.Project, record)
		return record, err
	}
	record.PID = cmd.Process.Pid
	record.State = "ready"
	if err := write(r.Project, record); err != nil {
		_ = stopPID(record.PID)
		return Record{}, err
	}
	// The supervisor is a separate process so the lease survives the CLI caller.
	supervisor := exec.Command(os.Args[0], "capsule", "playground", "supervise", "--project", r.Project, "--id", r.ID)
	supervisor.Stdout, supervisor.Stderr = log, log
	supervisor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	_ = supervisor.Start()
	return record, nil
}
func Read(project, id string) (Record, error) {
	var r Record
	b, e := os.ReadFile(path(project, id))
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(b, &r)
	return r, e
}
func Touch(project, id string) (Record, error) {
	r, e := Read(project, id)
	if e != nil {
		return r, e
	}
	r.Lease.LastActivityAt = time.Now().UTC()
	e = write(project, r)
	return r, e
}
func Status(project, id string) (Record, error) {
	r, e := Read(project, id)
	if e != nil {
		return r, e
	}
	if (r.State == "ready" || r.State == "starting") && !alive(r.PID) {
		r.State = "failed"
		r.Error = "playground process is no longer running"
		e = write(project, r)
	}
	return r, e
}
func Stop(project, id string) (Record, error) {
	r, e := Read(project, id)
	if e != nil {
		return r, e
	}
	if r.State == "ready" || r.State == "starting" {
		_ = stopPID(r.PID)
		r.State = "stopped"
		r.URL = ""
		e = write(project, r)
	}
	return r, e
}
func Reap(project, id string, now time.Time) (Record, error) {
	r, e := Status(project, id)
	if e != nil {
		return r, e
	}
	if r.State == "ready" && now.Sub(r.Lease.LastActivityAt) >= time.Duration(r.Lease.IdleTimeoutSeconds)*time.Second {
		return Stop(project, id)
	}
	return r, nil
}
func allocatePort(project, id string) (int, error) {
	h := sha256.Sum256([]byte(filepath.Clean(project) + "\x00" + id))
	start := 46000 + (int(h[0])<<2)%1000
	for i := 0; i < 1000; i++ {
		p := 46000 + (start-46000+i)%1000
		if portClaimed(project, p) {
			continue
		}
		l, e := net.Listen("tcp", "127.0.0.1:"+fmt.Sprint(p))
		if e == nil {
			_ = l.Close()
			return p, nil
		}
	}
	return 0, fmt.Errorf("capsule playground: no controlled port available")
}
func portClaimed(project string, port int) bool {
	entries, err := os.ReadDir(Root(project))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var r Record
		raw, err := os.ReadFile(filepath.Join(Root(project), entry.Name()))
		if err == nil && json.Unmarshal(raw, &r) == nil && r.Port == port && (r.State == "ready" || r.State == "starting") && alive(r.PID) {
			return true
		}
	}
	return false
}
func acquireStartLock(project string) (func(), error) {
	lock := filepath.Join(Root(project), ".start.lock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := os.Mkdir(lock, 0755); err == nil {
			return func() { _ = os.Remove(lock) }, nil
		} else if !os.IsExist(err) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("capsule playground: timed out allocating a controlled port")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func write(project string, r Record) error {
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	return atomicfile.WriteFile(path(project, r.ID), b, 0o644, 0o755)
}
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
func stopPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	if e := syscall.Kill(-pid, syscall.SIGTERM); e != nil && e != syscall.ESRCH {
		return e
	}
	return nil
}
func SourceSHAForWorkspace(workspace string) (string, error) {
	out, e := exec.Command("git", "-C", workspace, "rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(out)), e
}
func ValidSHA(s string) bool { _, e := hex.DecodeString(s); return e == nil && len(s) >= 7 }
