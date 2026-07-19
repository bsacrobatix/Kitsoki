package vmpool

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMintServerIdentityProducesParseableCertAndKey(t *testing.T) {
	id, err := MintServerIdentity("worker-1.kitsoki.local", []string{"10.1.2.3", "worker-1.kitsoki.local"}, time.Hour)
	if err != nil {
		t.Fatalf("MintServerIdentity: %v", err)
	}
	if len(id.CertPEM) == 0 || len(id.KeyPEM) == 0 {
		t.Fatalf("expected non-empty CertPEM and KeyPEM")
	}

	certBlock, _ := pem.Decode(id.CertPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		t.Fatalf("expected a CERTIFICATE PEM block, got %+v", certBlock)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	keyBlock, _ := pem.Decode(id.KeyPEM)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		t.Fatalf("expected an EC PRIVATE KEY PEM block, got %+v", keyBlock)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseECPrivateKey: %v", err)
	}

	certPub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("expected cert public key to be *ecdsa.PublicKey, got %T", cert.PublicKey)
	}
	if !certPub.Equal(&key.PublicKey) {
		t.Fatalf("cert public key does not match private key's public key")
	}

	if cert.Subject.CommonName != "worker-1.kitsoki.local" {
		t.Fatalf("CommonName = %q, want worker-1.kitsoki.local", cert.Subject.CommonName)
	}
}

func TestMintServerIdentitySANs(t *testing.T) {
	id, err := MintServerIdentity("cn", []string{"203.0.113.9", "example-worker.internal"}, time.Hour)
	if err != nil {
		t.Fatalf("MintServerIdentity: %v", err)
	}
	block, _ := pem.Decode(id.CertPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP("203.0.113.9")) {
		t.Fatalf("expected IP SAN 203.0.113.9, got %v", cert.IPAddresses)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "example-worker.internal" {
		t.Fatalf("expected DNS SAN example-worker.internal, got %v", cert.DNSNames)
	}

	foundServerAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			foundServerAuth = true
		}
	}
	if !foundServerAuth {
		t.Fatalf("expected ExtKeyUsageServerAuth in %v", cert.ExtKeyUsage)
	}
	if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Fatalf("expected KeyUsageDigitalSignature set")
	}
}

func TestMintServerIdentityHonorsTTLAndBackdate(t *testing.T) {
	ttl := 30 * time.Minute
	before := time.Now().UTC()
	id, err := MintServerIdentity("cn", nil, ttl)
	if err != nil {
		t.Fatalf("MintServerIdentity: %v", err)
	}
	after := time.Now().UTC()

	block, _ := pem.Decode(id.CertPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	if !cert.NotBefore.Before(before) {
		t.Fatalf("expected NotBefore backdated before mint time %v, got %v", before, cert.NotBefore)
	}
	if cert.NotBefore.After(before) {
		t.Fatalf("NotBefore should be in the past relative to mint start")
	}

	// x509 certificate timestamps only carry 1-second resolution, so allow a
	// second of slack on the floor of the window for truncation.
	wantNotAfterMin := before.Add(ttl).Add(-time.Second)
	wantNotAfterMax := after.Add(ttl)
	if cert.NotAfter.Before(wantNotAfterMin) || cert.NotAfter.After(wantNotAfterMax) {
		t.Fatalf("NotAfter = %v, want within [%v, %v]", cert.NotAfter, wantNotAfterMin, wantNotAfterMax)
	}
}

func TestMintServerIdentityRejectsInvalidInput(t *testing.T) {
	if _, err := MintServerIdentity("", []string{"1.2.3.4"}, time.Hour); err == nil {
		t.Fatalf("expected error for empty commonName")
	}
	if _, err := MintServerIdentity("cn", nil, 0); err == nil {
		t.Fatalf("expected error for zero ttl")
	}
	if _, err := MintServerIdentity("cn", nil, -time.Second); err == nil {
		t.Fatalf("expected error for negative ttl")
	}
}

func TestMintServerIdentityIsSelfSignedAndUsableAsTrustAnchor(t *testing.T) {
	// The controller pins CertPEM directly as the trust anchor for the
	// droplet's HTTPRemoteWorker connection (playing the ci.Remote.CAFile
	// role), so the self-signed cert must verify against itself as root.
	id, err := MintServerIdentity("worker.kitsoki.local", []string{"127.0.0.1", "worker.kitsoki.local"}, time.Hour)
	if err != nil {
		t.Fatalf("MintServerIdentity: %v", err)
	}
	block, _ := pem.Decode(id.CertPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(cert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "worker.kitsoki.local"}); err != nil {
		t.Fatalf("self-signed cert failed to verify against itself as root: %v", err)
	}
}

func TestMintWorkerTokenUniqueAndURLSafe(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		tok, err := MintWorkerToken()
		if err != nil {
			t.Fatalf("MintWorkerToken: %v", err)
		}
		if tok == "" {
			t.Fatalf("expected non-empty token")
		}
		if seen[tok] {
			t.Fatalf("duplicate token minted: %q", tok)
		}
		seen[tok] = true

		if strings.ContainsAny(tok, "+/=") {
			t.Fatalf("token %q is not url-safe / unpadded base64", tok)
		}
		// 32 bytes url-safe base64 without padding -> 43 chars (ceil(32*8/6)).
		if len(tok) != 43 {
			t.Fatalf("token length = %d, want 43 (32 bytes url-safe base64 unpadded)", len(tok))
		}
	}
}
