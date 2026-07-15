package daemonfederation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// PlacementThin identifies a worker whose harness calls a remote model.
	PlacementThin = "thin"
	// PlacementWorkstation identifies a worker that owns substantial workflow compute.
	PlacementWorkstation = "workstation"
	// PlacementLocalModel identifies a worker that serves model inference locally.
	PlacementLocalModel = "local-model"

	// HealthConnecting is reserved for a tunnel or first poll still in progress.
	HealthConnecting = "connecting"
	// HealthOnline means the latest worker poll completed successfully.
	HealthOnline = "online"
	// HealthDegraded means a known worker missed its latest poll; cached jobs remain visible.
	HealthDegraded = "degraded"
	// HealthOffline means a worker has not completed any successful poll.
	HealthOffline = "offline"
)

var workerID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Config is the validated set of independently deployed daemon workers. Its
// zero value is valid and disables federation.
type Config struct {
	Workers []Worker `yaml:"workers,omitempty"`
}

// Worker defines one daemon endpoint and its placement metadata. Endpoint must
// identify an HTTP JSON-RPC server; use Tunnel for a loopback SSH forward.
type Worker struct {
	ID        string  `yaml:"id"`
	Label     string  `yaml:"label"`
	Placement string  `yaml:"placement"`
	Endpoint  string  `yaml:"endpoint"`
	Tunnel    *Tunnel `yaml:"tunnel,omitempty"`
}

// Tunnel defines a strict SSH local forward. Identity and known-host paths are
// resolved relative to the config file by Config.Validate.
type Tunnel struct {
	Host           string `yaml:"host"`
	User           string `yaml:"user,omitempty"`
	LocalPort      int    `yaml:"local_port"`
	RemoteHost     string `yaml:"remote_host"`
	RemotePort     int    `yaml:"remote_port"`
	IdentityFile   string `yaml:"identity_file"`
	KnownHostsFile string `yaml:"known_hosts_file"`
}

// Validate normalizes and validates the complete federation config. It rejects
// ambiguous worker identities, duplicate local ports, and tunneled endpoints
// that could bypass the operator's loopback trust boundary.
func (c *Config) Validate(configPath string) error {
	seenIDs := map[string]bool{}
	seenPorts := map[int]bool{}
	for i := range c.Workers {
		w := &c.Workers[i]
		w.ID = strings.TrimSpace(w.ID)
		w.Label = strings.TrimSpace(w.Label)
		if !workerID.MatchString(w.ID) || w.ID == "local" || seenIDs[w.ID] {
			return fmt.Errorf("daemon_federation.workers[%d]: id must be a unique lowercase slug other than local", i)
		}
		seenIDs[w.ID] = true
		if w.Label == "" {
			return fmt.Errorf("daemon_federation.workers[%d]: label is required", i)
		}
		switch w.Placement {
		case PlacementThin, PlacementWorkstation, PlacementLocalModel:
		default:
			return fmt.Errorf("daemon_federation.workers[%d]: placement must be thin, workstation, or local-model", i)
		}
		u, err := url.Parse(w.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("daemon_federation.workers[%d]: endpoint must be absolute http(s) without credentials", i)
		}
		if w.Tunnel == nil {
			continue
		}
		t := w.Tunnel
		if t.Host == "" || t.RemoteHost == "" || t.LocalPort < 1 || t.LocalPort > 65535 || t.RemotePort < 1 || t.RemotePort > 65535 {
			return fmt.Errorf("daemon_federation.workers[%d].tunnel: host, remote_host, and valid ports are required", i)
		}
		if seenPorts[t.LocalPort] {
			return fmt.Errorf("daemon_federation.workers[%d].tunnel: local_port %d is already used", i, t.LocalPort)
		}
		seenPorts[t.LocalPort] = true
		if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
			return fmt.Errorf("daemon_federation.workers[%d]: tunnel endpoint must be loopback", i)
		}
		if u.Port() != fmt.Sprintf("%d", t.LocalPort) {
			return fmt.Errorf("daemon_federation.workers[%d]: endpoint port must equal tunnel.local_port", i)
		}
		base := filepath.Dir(configPath)
		t.IdentityFile, err = resolvePath(base, t.IdentityFile)
		if err != nil {
			return fmt.Errorf("daemon_federation.workers[%d].tunnel.identity_file: %w", i, err)
		}
		t.KnownHostsFile, err = resolvePath(base, t.KnownHostsFile)
		if err != nil {
			return fmt.Errorf("daemon_federation.workers[%d].tunnel.known_hosts_file: %w", i, err)
		}
	}
	return nil
}

func resolvePath(base, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("is required")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	clean := filepath.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("must not escape config directory")
	}
	return filepath.Join(base, clean), nil
}

// SSHArgs returns non-interactive OpenSSH arguments with strict host-key
// verification. The receiver must have passed Config.Validate first.
func (t Tunnel) SSHArgs() []string {
	destination := t.Host
	if t.User != "" {
		destination = t.User + "@" + destination
	}
	return []string{
		"-N", "-T",
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + t.KnownHostsFile,
		"-i", t.IdentityFile,
		"-L", fmt.Sprintf("127.0.0.1:%d:%s:%d", t.LocalPort, t.RemoteHost, t.RemotePort),
		destination,
	}
}

// Job is the transport-neutral subset of a worker's durable job summary plus
// controller-assigned worker provenance.
type Job struct {
	JobID             string    `json:"job_id"`
	SessionID         string    `json:"session_id,omitempty"`
	AppID             string    `json:"app_id"`
	Story             string    `json:"story"`
	Status            string    `json:"status"`
	Phase             string    `json:"phase,omitempty"`
	Summary           string    `json:"summary,omitempty"`
	RunURL            string    `json:"run_url"`
	UpdatedAt         time.Time `json:"updated_at"`
	InterruptedReason string    `json:"interrupted_reason,omitempty"`
	WorkerID          string    `json:"worker_id,omitempty"`
	WorkerLabel       string    `json:"worker_label,omitempty"`
	Placement         string    `json:"placement,omitempty"`
	OpenURL           string    `json:"open_url,omitempty"`
}

// WorkerStatus describes the latest health observation for one configured
// worker. LastSeen is the last successful poll, not the last attempted poll.
type WorkerStatus struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Placement string    `json:"placement"`
	Health    string    `json:"health"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	JobCount  int       `json:"job_count"`
}

// RPCClient abstracts worker polling so Pool does not depend on an HTTP
// transport in tests or future tunnel implementations.
type RPCClient interface {
	ListJobs(context.Context, string) ([]Job, error)
}

// HTTPClient calls a worker's JSON-RPC endpoint. Its zero value uses a bounded
// default HTTP client and is safe for concurrent use.
type HTTPClient struct {
	Client *http.Client
}

// ListJobs returns the worker's durable artifact jobs. Transport failures,
// non-200 responses, RPC errors, and malformed responses are errors.
func (c HTTPClient) ListJobs(ctx context.Context, endpoint string) ([]Job, error) {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/rpc", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":"federation","method":"runstatus.jobs.list","params":{}}`)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rpc HTTP %s", resp.Status)
	}
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		return nil, fmt.Errorf("decode rpc: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("rpc: %s", rpc.Error.Message)
	}
	if rpc.JSONRPC != "2.0" || rpc.Result == nil {
		return nil, fmt.Errorf("rpc: malformed response")
	}
	var jobs []Job
	if err := json.Unmarshal(rpc.Result, &jobs); err != nil {
		return nil, fmt.Errorf("decode jobs: %w", err)
	}
	return jobs, nil
}

// Snapshot is one coherent view of federated jobs and worker health.
type Snapshot struct {
	Jobs    []Job
	Workers []WorkerStatus
}

// Pool concurrently polls configured workers and caches coherent snapshots.
// Pool is safe for concurrent use. Its zero value represents no workers.
type Pool struct {
	Workers []Worker
	Client  RPCClient
	Timeout time.Duration
	TTL     time.Duration
	Now     func() time.Time

	mu          sync.Mutex
	refreshed   time.Time
	snapshot    Snapshot
	refreshing  bool
	refreshDone chan struct{}
}

// Get returns a cloned cached snapshot or performs one concurrent refresh when
// the TTL has expired. A slow or failed worker never suppresses healthy peers.
func (p *Pool) Get(ctx context.Context) Snapshot {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	ttl := p.TTL
	if ttl <= 0 {
		ttl = 2 * time.Second
	}
	p.mu.Lock()
	if !p.refreshed.IsZero() && now().Sub(p.refreshed) < ttl {
		snapshot := cloneSnapshot(p.snapshot)
		p.mu.Unlock()
		return snapshot
	}
	if p.refreshing {
		if !p.refreshed.IsZero() {
			snapshot := cloneSnapshot(p.snapshot)
			p.mu.Unlock()
			return snapshot
		}
		done := p.refreshDone
		p.mu.Unlock()
		select {
		case <-done:
			p.mu.Lock()
			snapshot := cloneSnapshot(p.snapshot)
			p.mu.Unlock()
			return snapshot
		case <-ctx.Done():
			return Snapshot{}
		}
	}
	p.refreshing = true
	p.refreshDone = make(chan struct{})
	previous := cloneSnapshot(p.snapshot)
	p.mu.Unlock()

	// A refresh populates shared controller state. One browser abandoning its
	// request must not cancel the poll and mark a healthy worker degraded for
	// every other caller; per-worker timeouts still bound the detached work.
	snapshot := p.poll(context.WithoutCancel(ctx), now, previous)
	p.mu.Lock()
	p.snapshot = snapshot
	p.refreshed = now()
	p.refreshing = false
	close(p.refreshDone)
	result := cloneSnapshot(p.snapshot)
	p.mu.Unlock()
	return result
}

func (p *Pool) poll(ctx context.Context, now func() time.Time, previous Snapshot) Snapshot {
	client := p.Client
	if client == nil {
		client = HTTPClient{}
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	statuses := make([]WorkerStatus, len(p.Workers))
	jobs := []Job{}
	previousStatuses := make(map[string]WorkerStatus, len(previous.Workers))
	previousJobs := make(map[string][]Job, len(previous.Workers))
	for _, status := range previous.Workers {
		previousStatuses[status.ID] = status
	}
	for _, job := range previous.Jobs {
		previousJobs[job.WorkerID] = append(previousJobs[job.WorkerID], job)
	}
	var jobsMu sync.Mutex
	var wg sync.WaitGroup
	for i, worker := range p.Workers {
		wg.Add(1)
		go func(i int, worker Worker) {
			defer wg.Done()
			status := WorkerStatus{ID: worker.ID, Label: worker.Label, Placement: worker.Placement, Health: HealthConnecting}
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			result, err := client.ListJobs(callCtx, worker.Endpoint)
			if err != nil {
				status.Health = HealthOffline
				if prior, ok := previousStatuses[worker.ID]; ok && !prior.LastSeen.IsZero() {
					status.Health = HealthDegraded
					status.LastSeen = prior.LastSeen
					status.JobCount = len(previousJobs[worker.ID])
					jobsMu.Lock()
					jobs = append(jobs, previousJobs[worker.ID]...)
					jobsMu.Unlock()
				}
				status.LastError = bound(err.Error())
				statuses[i] = status
				return
			}
			status.Health = HealthOnline
			status.LastSeen = now().UTC()
			status.JobCount = len(result)
			for j := range result {
				result[j].WorkerID = worker.ID
				result[j].WorkerLabel = worker.Label
				result[j].Placement = worker.Placement
				result[j].OpenURL = resolveOpenURL(worker.Endpoint, result[j].RunURL)
			}
			jobsMu.Lock()
			jobs = append(jobs, result...)
			jobsMu.Unlock()
			statuses[i] = status
		}(i, worker)
	}
	wg.Wait()
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].WorkerID == jobs[j].WorkerID {
			return jobs[i].JobID < jobs[j].JobID
		}
		return jobs[i].WorkerID < jobs[j].WorkerID
	})
	return Snapshot{Jobs: jobs, Workers: statuses}
}

func resolveOpenURL(endpoint, runURL string) string {
	base, err := url.Parse(strings.TrimRight(endpoint, "/") + "/")
	if err != nil {
		return ""
	}
	ref, err := url.Parse(runURL)
	if err != nil || ref.IsAbs() || ref.Host != "" {
		return ""
	}
	return base.ResolveReference(ref).String()
}

func cloneSnapshot(in Snapshot) Snapshot {
	return Snapshot{
		Jobs:    append([]Job(nil), in.Jobs...),
		Workers: append([]WorkerStatus(nil), in.Workers...),
	}
}

func bound(s string) string {
	if len(s) > 240 {
		return s[:240]
	}
	return s
}
