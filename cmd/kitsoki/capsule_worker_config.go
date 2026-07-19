package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"kitsoki/internal/capsule/bucketsource"
	"kitsoki/internal/capsule/workerserver"
	"kitsoki/internal/objectstore"
)

// Worker env-file keys. A VM worker's cloud-init writes one KEY=VALUE file
// (/etc/kitsoki-worker/env) and starts `kitsoki capsule worker serve --config
// <file>`; the file is the single boot contract between the pool's user-data
// generator and this process.
const (
	workerEnvToken       = "KITSOKI_WORKER_TOKEN"
	workerEnvListen      = "KITSOKI_WORKER_LISTEN"
	workerEnvRoot        = "KITSOKI_WORKER_ROOT"
	workerEnvTLSCert     = "KITSOKI_WORKER_TLS_CERT"
	workerEnvTLSKey      = "KITSOKI_WORKER_TLS_KEY"
	workerEnvIsolation   = "KITSOKI_WORKER_ISOLATION"
	workerEnvNetworks    = "KITSOKI_WORKER_NETWORKS"
	workerEnvBackend     = "KITSOKI_WORKER_AGENT_BACKEND"
	workerEnvPassEnv     = "KITSOKI_WORKER_PASS_ENV"
	workerEnvOutputsURL  = "KITSOKI_WORKER_OUTPUTS_URL"
	workerEnvOutputsKey  = "KITSOKI_WORKER_OUTPUTS_KEY_ENV"
	workerEnvOutputsSec  = "KITSOKI_WORKER_OUTPUTS_SECRET_ENV"
	workerEnvOutputsPfx  = "KITSOKI_WORKER_OUTPUTS_PREFIX"
	workerConfigDefaults = "listen=127.0.0.1:7443 root=/var/lib/kitsoki-worker tls=/etc/kitsoki-worker/server.{crt,key} isolation=vm"
)

// loadWorkerEnvConfig parses a KEY=VALUE env file (comments and blank lines
// allowed, no shell interpolation) and exports every pair into the process
// environment, so KITSOKI_WORKER_TOKEN keeps flowing through the existing
// --token-env path and output-bucket credentials resolve by name.
func loadWorkerEnvConfig(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("capsule worker: open config: %w", err)
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("capsule worker: config line %d is not KEY=VALUE", line)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("capsule worker: read config: %w", err)
	}
	for key, value := range values {
		if err := os.Setenv(key, value); err != nil {
			return nil, err
		}
	}
	return values, nil
}

// workerOutputsFromEnv builds the optional bucket output mirror from resolved
// worker config values. Returns nil when no output bucket is configured.
func workerOutputsFromEnv(values map[string]string) (workerserver.OutputSink, error) {
	bucketURL := values[workerEnvOutputsURL]
	if strings.TrimSpace(bucketURL) == "" {
		return nil, nil
	}
	keyEnv, secretEnv := values[workerEnvOutputsKey], values[workerEnvOutputsSec]
	if keyEnv == "" || secretEnv == "" {
		return nil, fmt.Errorf("capsule worker: %s requires %s and %s", workerEnvOutputsURL, workerEnvOutputsKey, workerEnvOutputsSec)
	}
	cfg, err := objectstore.ParseBucketURL(bucketURL)
	if err != nil {
		return nil, err
	}
	cfg.KeyEnv, cfg.SecretEnv = keyEnv, secretEnv
	store, err := objectstore.NewSpaces(cfg, nil)
	if err != nil {
		return nil, err
	}
	return bucketsource.OutputMirror{Store: store, Prefix: values[workerEnvOutputsPfx]}, nil
}

// applyWorkerEnvConfig maps config values onto serve options, keeping any
// value the operator overrode with an explicit flag.
type workerServeOptions struct {
	listen, root, certFile, keyFile, tokenEnv, isolation, agentBackend string
	networks, passEnv                                                  []string
}

func applyWorkerEnvConfig(values map[string]string, set func(name string) bool, opts *workerServeOptions) {
	assign := func(flag, key string, target *string) {
		if value, ok := values[key]; ok && !set(flag) && strings.TrimSpace(value) != "" {
			*target = value
		}
	}
	assign("listen", workerEnvListen, &opts.listen)
	assign("root", workerEnvRoot, &opts.root)
	assign("tls-cert", workerEnvTLSCert, &opts.certFile)
	assign("tls-key", workerEnvTLSKey, &opts.keyFile)
	assign("isolation", workerEnvIsolation, &opts.isolation)
	assign("agent-backend", workerEnvBackend, &opts.agentBackend)
	if _, ok := values[workerEnvToken]; ok && !set("token-env") {
		opts.tokenEnv = workerEnvToken
	}
	if value, ok := values[workerEnvNetworks]; ok && !set("network") && strings.TrimSpace(value) != "" {
		opts.networks = splitTrimmed(value)
	}
	if value, ok := values[workerEnvPassEnv]; ok && !set("pass-env") && strings.TrimSpace(value) != "" {
		opts.passEnv = splitTrimmed(value)
	}
	// Config-driven defaults for a VM worker boot: durable root and TLS
	// material live at the paths cloud-init writes.
	if opts.root == "" && len(values) > 0 {
		opts.root = "/var/lib/kitsoki-worker"
	}
	if opts.certFile == "" && len(values) > 0 {
		opts.certFile = "/etc/kitsoki-worker/server.crt"
	}
	if opts.keyFile == "" && len(values) > 0 {
		opts.keyFile = "/etc/kitsoki-worker/server.key"
	}
}

func splitTrimmed(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
