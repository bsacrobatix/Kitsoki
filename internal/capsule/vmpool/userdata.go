package vmpool

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"
)

// BootSpec describes the single ephemeral droplet worker that a generated
// cloud-init user-data script boots. Every value here is single-job: the
// token and TLS key both die with the droplet.
type BootSpec struct {
	WorkerID   string
	JobID      string
	Token      string
	ListenAddr string
	Identity   ServerIdentity
	Env        map[string]string
	// User, when set, runs the kitsoki-worker service as this pre-existing
	// image user instead of root, so credentials the image bakes for that
	// user (agent CLI auth state, HOME-relative config) are usable by story
	// steps. The boot script chowns the worker's config, durable root, and
	// log directories to it and fails the boot early if the user is missing.
	User string
}

// heredocMarker identifies one embedded here-document block within the
// generated script. Using a distinct, unlikely-to-collide marker per block
// keeps the "one content line happens to equal the terminator" failure mode
// independent across blocks.
type heredocMarker string

const (
	markerCert heredocMarker = "KITSOKI_VMPOOL_CERT_EOF"
	markerKey  heredocMarker = "KITSOKI_VMPOOL_KEY_EOF"
	markerEnv  heredocMarker = "KITSOKI_VMPOOL_ENV_EOF"
	markerUnit heredocMarker = "KITSOKI_VMPOOL_UNIT_EOF"
)

// reservedEnvKeys are the fixed environment keys GenerateUserData always
// writes itself; spec.Env may not redefine them (avoids an ambiguous,
// silently-overridden value reaching the worker).
var reservedEnvKeys = []string{
	"KITSOKI_WORKER_TOKEN",
	"KITSOKI_WORKER_LISTEN",
	"WORKER_ID",
	"JOB_ID",
}

// GenerateUserData renders a thin cloud-init shell script that boots exactly
// one kitsoki capsule worker on a droplet whose base-image snapshot already
// contains the kitsoki binary and agent CLIs. It does not install any
// toolchain. It writes the per-droplet TLS identity and worker env file,
// installs and starts the kitsoki-worker systemd unit, and appends staged
// progress markers to /var/log/kitsoki-worker/boot.log so a stuck boot can be
// diagnosed from the droplet's console/SSH — the same diagnostic trick
// rumbledunk's vmworkerpool used with cloud-init-progress.log.
//
// Every secret and PEM value is embedded via a single-quoted heredoc
// (`<<'MARKER'`), which disables all shell expansion of its body, so
// arbitrary bytes in a token, key, or env value cannot break out of the
// script. GenerateUserData validates spec before rendering and returns an
// error rather than emit a broken or unsafe script.
//
// The user-data blob is only readable via the droplet's own metadata
// service from inside the droplet itself; both the bearer token and the TLS
// private key it carries are single-job and are destroyed with the droplet.
func GenerateUserData(spec BootSpec) (string, error) {
	if err := validateBootSpec(spec); err != nil {
		return "", err
	}

	envLines, err := buildEnvLines(spec)
	if err != nil {
		return "", err
	}
	if err := checkHeredocSafe(markerEnv, envLines); err != nil {
		return "", err
	}

	certLines := splitLines(strings.TrimRight(string(spec.Identity.CertPEM), "\n"))
	if err := checkHeredocSafe(markerCert, certLines); err != nil {
		return "", err
	}
	keyLines := splitLines(strings.TrimRight(string(spec.Identity.KeyPEM), "\n"))
	if err := checkHeredocSafe(markerKey, keyLines); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("set -euo pipefail\n")
	b.WriteString("\n")
	b.WriteString("mkdir -p /etc/kitsoki-worker\n")
	b.WriteString("mkdir -p /var/log/kitsoki-worker\n")
	b.WriteString("\n")
	b.WriteString("log() {\n")
	b.WriteString("  printf '%s %s\\n' \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\" \"$1\" >> /var/log/kitsoki-worker/boot.log\n")
	b.WriteString("}\n")
	b.WriteString("\n")
	b.WriteString("log 'boot: writing tls server identity'\n")
	writeHeredoc(&b, markerCert, "/etc/kitsoki-worker/server.crt", certLines)
	b.WriteString("chmod 0600 /etc/kitsoki-worker/server.crt\n")
	b.WriteString("\n")
	writeHeredoc(&b, markerKey, "/etc/kitsoki-worker/server.key", keyLines)
	b.WriteString("chmod 0600 /etc/kitsoki-worker/server.key\n")
	b.WriteString("\n")
	b.WriteString("log 'boot: writing worker env'\n")
	writeHeredoc(&b, markerEnv, "/etc/kitsoki-worker/env", envLines)
	b.WriteString("chmod 0600 /etc/kitsoki-worker/env\n")
	b.WriteString("\n")
	if spec.User != "" {
		fmt.Fprintf(&b, "log 'boot: preparing service user %s'\n", spec.User)
		fmt.Fprintf(&b, "id -u %s >/dev/null\n", spec.User)
		b.WriteString("mkdir -p /var/lib/kitsoki-worker\n")
		fmt.Fprintf(&b, "chown -R %s: /etc/kitsoki-worker /var/lib/kitsoki-worker /var/log/kitsoki-worker\n", spec.User)
		b.WriteString("\n")
	}
	b.WriteString("log 'boot: writing kitsoki-worker systemd unit'\n")
	writeHeredoc(&b, markerUnit, "/etc/systemd/system/kitsoki-worker.service", unitLines(spec.User))
	b.WriteString("\n")
	b.WriteString("log 'boot: enabling kitsoki-worker service'\n")
	b.WriteString("systemctl daemon-reload\n")
	b.WriteString("systemctl enable kitsoki-worker.service\n")
	b.WriteString("systemctl start kitsoki-worker.service\n")
	b.WriteString("log 'boot: kitsoki-worker service started'\n")

	return b.String(), nil
}

// writeHeredoc appends a `cat <<'MARKER' > path ... MARKER` block. The
// quoted delimiter disables parameter/command substitution and backslash
// processing inside the body, so lines are written to path verbatim.
func writeHeredoc(b *strings.Builder, marker heredocMarker, path string, lines []string) {
	fmt.Fprintf(b, "cat <<'%s' > %s\n", marker, path)
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "%s\n", marker)
}

func unitLines(user string) []string {
	lines := []string{
		"[Unit]",
		"Description=Kitsoki capsule worker",
		"After=network-online.target",
		"Wants=network-online.target",
		"",
		"[Service]",
		"Type=simple",
	}
	if user != "" {
		lines = append(lines, "User="+user)
	}
	lines = append(lines,
		// systemd does not source /etc/environment on its own; loading it
		// here lets credentials baked into the image reach the worker
		// process, where KITSOKI_WORKER_PASS_ENV can name them for story
		// steps. The leading '-' keeps an absent file non-fatal.
		"EnvironmentFile=-/etc/environment",
		"ExecStart=/usr/local/bin/kitsoki capsule worker serve --config /etc/kitsoki-worker/env",
		"Restart=on-failure",
		"RestartSec=2",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
	)
	return lines
}

// buildEnvLines renders the KEY=VALUE lines written to
// /etc/kitsoki-worker/env: the four fixed keys in a fixed order, followed by
// spec.Env sorted by key for determinism.
func buildEnvLines(spec BootSpec) ([]string, error) {
	lines := []string{
		"KITSOKI_WORKER_TOKEN=" + spec.Token,
		"KITSOKI_WORKER_LISTEN=" + spec.ListenAddr,
		"WORKER_ID=" + spec.WorkerID,
		"JOB_ID=" + spec.JobID,
	}

	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, k+"="+spec.Env[k])
	}
	return lines, nil
}

// checkHeredocSafe rejects a body whose delimiter word appears as an exact
// line, which would otherwise terminate the here-document early and either
// truncate the write or corrupt the rest of the script.
func checkHeredocSafe(marker heredocMarker, lines []string) error {
	for _, line := range lines {
		if line == string(marker) {
			return fmt.Errorf("vmpool: generate user data: value collides with heredoc marker %s", marker)
		}
	}
	return nil
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// validateBootSpec rejects an incomplete or unsafe spec rather than let
// GenerateUserData emit a broken script.
func validateBootSpec(spec BootSpec) error {
	if spec.WorkerID == "" {
		return fmt.Errorf("vmpool: generate user data: WorkerID is required")
	}
	if spec.JobID == "" {
		return fmt.Errorf("vmpool: generate user data: JobID is required")
	}
	if spec.Token == "" {
		return fmt.Errorf("vmpool: generate user data: Token is required")
	}
	if spec.ListenAddr == "" {
		return fmt.Errorf("vmpool: generate user data: ListenAddr is required")
	}

	for name, v := range map[string]string{
		"WorkerID":   spec.WorkerID,
		"JobID":      spec.JobID,
		"Token":      spec.Token,
		"ListenAddr": spec.ListenAddr,
	} {
		if strings.Contains(v, "\n") {
			return fmt.Errorf("vmpool: generate user data: %s must not contain a newline", name)
		}
	}

	if spec.User != "" && !ValidUserName(spec.User) {
		return fmt.Errorf("vmpool: generate user data: User %q is not a valid unix user name", spec.User)
	}

	for k, v := range spec.Env {
		if k == "" {
			return fmt.Errorf("vmpool: generate user data: Env key must not be empty")
		}
		if strings.Contains(k, "\n") || strings.Contains(v, "\n") {
			return fmt.Errorf("vmpool: generate user data: Env[%q] must not contain a newline", k)
		}
		for _, reserved := range reservedEnvKeys {
			if k == reserved {
				return fmt.Errorf("vmpool: generate user data: Env must not redefine reserved key %q", k)
			}
		}
	}

	if err := validatePEMBlock(spec.Identity.CertPEM, "CERTIFICATE"); err != nil {
		return fmt.Errorf("vmpool: generate user data: Identity.CertPEM: %w", err)
	}
	if _, err := x509.ParseCertificate(mustPEMBytes(spec.Identity.CertPEM)); err != nil {
		return fmt.Errorf("vmpool: generate user data: Identity.CertPEM: parse certificate: %w", err)
	}
	if err := validatePEMBlock(spec.Identity.KeyPEM, ""); err != nil {
		return fmt.Errorf("vmpool: generate user data: Identity.KeyPEM: %w", err)
	}
	if err := validatePrivateKeyPEM(spec.Identity.KeyPEM); err != nil {
		return fmt.Errorf("vmpool: generate user data: Identity.KeyPEM: %w", err)
	}

	return nil
}

// ValidUserName accepts only a conservative useradd-compatible name.
// BootSpec.User is interpolated into the boot script and systemd unit
// outside any heredoc, so anything beyond this character set is rejected
// rather than escaped. Exported so ci config validation can apply the same
// rule at no-spend validate time.
func ValidUserName(user string) bool {
	if len(user) == 0 || len(user) > 32 {
		return false
	}
	for i, r := range user {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case i > 0 && (r >= '0' && r <= '9' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// validatePrivateKeyPEM requires the decoded PEM block to parse as either an
// EC private key (the format MintServerIdentity produces) or a PKCS#8
// private key, so a malformed key is rejected before it reaches a script.
func validatePrivateKeyPEM(data []byte) error {
	block, _ := pem.Decode(data)
	if block == nil {
		return fmt.Errorf("no PEM block found")
	}
	if _, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return nil
	}
	if _, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return nil
	}
	return fmt.Errorf("failed to parse private key (not EC or PKCS8)")
}

// validatePEMBlock requires data to decode as exactly one well-formed PEM
// block. When wantType is non-empty, the block type must match it.
func validatePEMBlock(data []byte, wantType string) error {
	if len(data) == 0 {
		return fmt.Errorf("empty PEM data")
	}
	block, rest := pem.Decode(data)
	if block == nil {
		return fmt.Errorf("no PEM block found")
	}
	if len(bytesTrimSpace(rest)) != 0 {
		return fmt.Errorf("unexpected trailing data after PEM block")
	}
	if wantType != "" && block.Type != wantType {
		return fmt.Errorf("unexpected PEM block type %q, want %q", block.Type, wantType)
	}
	return nil
}

func mustPEMBytes(data []byte) []byte {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil
	}
	return block.Bytes
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
