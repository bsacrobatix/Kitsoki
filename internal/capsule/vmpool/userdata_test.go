package vmpool

import (
	"strings"
	"testing"
	"time"
)

func testIdentity(t *testing.T) ServerIdentity {
	t.Helper()
	id, err := MintServerIdentity("worker-1.kitsoki.local", []string{"10.0.0.5"}, time.Hour)
	if err != nil {
		t.Fatalf("MintServerIdentity: %v", err)
	}
	return id
}

func validSpec(t *testing.T) BootSpec {
	t.Helper()
	return BootSpec{
		WorkerID:   "worker-1",
		JobID:      "job-42",
		Token:      "tok-abc123",
		ListenAddr: "0.0.0.0:8443",
		Identity:   testIdentity(t),
		Env: map[string]string{
			"KITSOKI_LOG_LEVEL": "debug",
			"ARTIFACT_BUCKET":   "kitsoki-test",
		},
	}
}

func TestGenerateUserDataStructure(t *testing.T) {
	spec := validSpec(t)
	script, err := GenerateUserData(spec)
	if err != nil {
		t.Fatalf("GenerateUserData: %v", err)
	}

	if !strings.HasPrefix(script, "#!/bin/bash\n") {
		t.Fatalf("script must start with shebang, got prefix %q", script[:min(40, len(script))])
	}
	if !strings.Contains(script, "set -euo pipefail") {
		t.Fatalf("expected set -euo pipefail")
	}
	if !strings.Contains(script, "kitsoki-worker.service") {
		t.Fatalf("expected systemd unit file reference")
	}
	if !strings.Contains(script, "ExecStart=/usr/local/bin/kitsoki capsule worker serve --config /etc/kitsoki-worker/env") {
		t.Fatalf("expected exact ExecStart contract line")
	}
	if !strings.Contains(script, "systemctl enable kitsoki-worker.service") {
		t.Fatalf("expected systemctl enable")
	}
	if !strings.Contains(script, "systemctl start kitsoki-worker.service") {
		t.Fatalf("expected systemctl start")
	}
	if !strings.Contains(script, "/var/log/kitsoki-worker/boot.log") {
		t.Fatalf("expected boot.log progress marker path")
	}
	if !strings.Contains(script, "mkdir -p /var/log/kitsoki-worker") {
		t.Fatalf("expected boot.log directory creation")
	}

	if !strings.Contains(script, "chmod 0600 /etc/kitsoki-worker/server.crt") {
		t.Fatalf("expected cert chmod 0600")
	}
	if !strings.Contains(script, "chmod 0600 /etc/kitsoki-worker/server.key") {
		t.Fatalf("expected key chmod 0600")
	}
	if !strings.Contains(script, "chmod 0600 /etc/kitsoki-worker/env") {
		t.Fatalf("expected env chmod 0600")
	}

	// No toolchain installation: thin script must not apt/yum/curl-pipe-install.
	for _, banned := range []string{"apt-get install", "apt install", "yum install", "curl -sSL https://", "go install"} {
		if strings.Contains(script, banned) {
			t.Fatalf("thin user-data script must not install toolchains; found %q", banned)
		}
	}
}

func TestGenerateUserDataWritesCertVerbatim(t *testing.T) {
	spec := validSpec(t)
	script, err := GenerateUserData(spec)
	if err != nil {
		t.Fatalf("GenerateUserData: %v", err)
	}

	certBody := strings.TrimRight(string(spec.Identity.CertPEM), "\n")
	if !strings.Contains(script, certBody) {
		t.Fatalf("expected cert PEM body embedded verbatim")
	}
	keyBody := strings.TrimRight(string(spec.Identity.KeyPEM), "\n")
	if !strings.Contains(script, keyBody) {
		t.Fatalf("expected key PEM body embedded verbatim")
	}
}

func TestGenerateUserDataEnvLinesSortedAndComplete(t *testing.T) {
	spec := validSpec(t)
	script, err := GenerateUserData(spec)
	if err != nil {
		t.Fatalf("GenerateUserData: %v", err)
	}

	envStart := strings.Index(script, "> /etc/kitsoki-worker/env")
	if envStart == -1 {
		t.Fatalf("expected env heredoc redirect")
	}
	envBlock := script[envStart:]
	endMarkerIdx := strings.Index(envBlock, string(markerEnv)+"\n")
	if endMarkerIdx == -1 {
		// last block in file may not have trailing newline after marker; still find marker.
		endMarkerIdx = strings.Index(envBlock[1:], string(markerEnv))
	}

	wantOrder := []string{
		"KITSOKI_WORKER_TOKEN=tok-abc123",
		"KITSOKI_WORKER_LISTEN=0.0.0.0:8443",
		"WORKER_ID=worker-1",
		"JOB_ID=job-42",
		"ARTIFACT_BUCKET=kitsoki-test",
		"KITSOKI_LOG_LEVEL=debug",
	}
	lastIdx := -1
	for _, line := range wantOrder {
		idx := strings.Index(script, line)
		if idx == -1 {
			t.Fatalf("expected env line %q in script", line)
		}
		if idx <= lastIdx {
			t.Fatalf("env line %q out of order (fixed keys first, then Env sorted)", line)
		}
		lastIdx = idx
	}
}

func TestGenerateUserDataDeterministic(t *testing.T) {
	spec := validSpec(t)
	a, err := GenerateUserData(spec)
	if err != nil {
		t.Fatalf("GenerateUserData: %v", err)
	}
	b, err := GenerateUserData(spec)
	if err != nil {
		t.Fatalf("GenerateUserData: %v", err)
	}
	if a != b {
		t.Fatalf("expected deterministic output for identical spec")
	}
}

func TestGenerateUserDataQuotingSafety(t *testing.T) {
	spec := validSpec(t)
	dangerous := `tok'; rm -rf / #$(whoami)`
	spec.Token = dangerous

	script, err := GenerateUserData(spec)
	if err != nil {
		t.Fatalf("GenerateUserData: %v", err)
	}

	line := "KITSOKI_WORKER_TOKEN=" + dangerous
	if !strings.Contains(script, line) {
		t.Fatalf("expected dangerous token embedded verbatim as one line")
	}

	// The dangerous value must appear only within the single-quoted heredoc
	// body, i.e. strictly between the opening `<<'MARKER'` and the closing
	// bare MARKER line for the env block.
	envOpenTag := "<<'" + string(markerEnv) + "'"
	openIdx := strings.Index(script, envOpenTag)
	if openIdx == -1 {
		t.Fatalf("expected quoted heredoc open marker for env block")
	}
	afterOpen := script[openIdx+len(envOpenTag):]
	closeIdx := strings.Index(afterOpen, "\n"+string(markerEnv)+"\n")
	if closeIdx == -1 {
		t.Fatalf("expected balanced closing heredoc marker for env block")
	}
	body := afterOpen[:closeIdx]
	if !strings.Contains(body, dangerous) {
		t.Fatalf("expected dangerous value inside the env heredoc body")
	}

	// Outside of that body (before open / after close), the raw dangerous
	// value must not appear again — i.e. it was not also interpolated
	// unsafely somewhere else in the script.
	before := script[:openIdx]
	after := afterOpen[closeIdx+len("\n"+string(markerEnv)+"\n"):]
	if strings.Contains(before, dangerous) || strings.Contains(after, dangerous) {
		t.Fatalf("dangerous value leaked outside its quoted heredoc body")
	}

	// Markers must be balanced: exactly one open + one close per block for
	// every marker actually used in the script.
	for _, m := range []heredocMarker{markerCert, markerKey, markerEnv, markerUnit} {
		open := strings.Count(script, "<<'"+string(m)+"'")
		closeCount := strings.Count(script, "\n"+string(m)+"\n")
		if open != 1 {
			t.Fatalf("marker %s: expected exactly 1 opening tag, got %d", m, open)
		}
		if closeCount != 1 {
			t.Fatalf("marker %s: expected exactly 1 closing line, got %d", m, closeCount)
		}
	}
}

func TestGenerateUserDataRejectsMissingFields(t *testing.T) {
	base := validSpec(t)

	cases := []struct {
		name   string
		modify func(*BootSpec)
	}{
		{"missing worker id", func(s *BootSpec) { s.WorkerID = "" }},
		{"missing job id", func(s *BootSpec) { s.JobID = "" }},
		{"missing token", func(s *BootSpec) { s.Token = "" }},
		{"missing listen addr", func(s *BootSpec) { s.ListenAddr = "" }},
		{"missing cert pem", func(s *BootSpec) { s.Identity.CertPEM = nil }},
		{"missing key pem", func(s *BootSpec) { s.Identity.KeyPEM = nil }},
		{"newline in token", func(s *BootSpec) { s.Token = "line1\nline2" }},
		{"reserved env key", func(s *BootSpec) { s.Env["WORKER_ID"] = "clobber" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := base
			spec.Env = map[string]string{}
			for k, v := range base.Env {
				spec.Env[k] = v
			}
			tc.modify(&spec)
			if _, err := GenerateUserData(spec); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestGenerateUserDataRejectsBadPEM(t *testing.T) {
	spec := validSpec(t)
	spec.Identity.CertPEM = []byte("not a pem block at all")
	if _, err := GenerateUserData(spec); err == nil {
		t.Fatalf("expected error for garbage CertPEM")
	}

	spec2 := validSpec(t)
	spec2.Identity.KeyPEM = []byte("-----BEGIN EC PRIVATE KEY-----\nbm90IHJlYWwga2V5IGJ5dGVz\n-----END EC PRIVATE KEY-----\n")
	if _, err := GenerateUserData(spec2); err == nil {
		t.Fatalf("expected error for malformed key PEM contents")
	}

	spec3 := validSpec(t)
	spec3.Identity.CertPEM = []byte("-----BEGIN CERTIFICATE-----\nbm90IGEgcmVhbCBjZXJ0\n-----END CERTIFICATE-----\n")
	if _, err := GenerateUserData(spec3); err == nil {
		t.Fatalf("expected error for malformed certificate PEM contents")
	}
}
