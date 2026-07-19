package vmpool

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// clockSkewBackdate absorbs modest clock drift between the controller (which
// mints the certificate) and the droplet (which will validate NotBefore
// shortly after boot).
const clockSkewBackdate = 5 * time.Minute

// workerTokenBytes is the amount of entropy backing a minted worker bearer
// token, matching the sizing used elsewhere in kitsoki for bearer tokens.
const workerTokenBytes = 32

// ServerIdentity is a self-signed TLS server identity minted by the
// controller for a single ephemeral droplet worker. The controller retains
// CertPEM as the pinned trust anchor for the corresponding
// executor.HTTPRemoteWorker / ci.Remote.CAFile, and ships both CertPEM and
// KeyPEM to the droplet via user data so the worker service can terminate
// TLS with a certificate the controller already trusts. The identity is
// single-job: it is minted fresh per droplet and discarded when the droplet
// is destroyed.
type ServerIdentity struct {
	CertPEM []byte
	KeyPEM  []byte
}

// MintServerIdentity creates a fresh ECDSA P-256 self-signed server
// certificate for commonName, valid for the given hosts (IP addresses become
// IP SANs, everything else becomes a DNS SAN), expiring after ttl. It uses
// only the Go standard library crypto stack.
func MintServerIdentity(commonName string, hosts []string, ttl time.Duration) (ServerIdentity, error) {
	if commonName == "" {
		return ServerIdentity{}, fmt.Errorf("vmpool: mint server identity: commonName is required")
	}
	if ttl <= 0 {
		return ServerIdentity{}, fmt.Errorf("vmpool: mint server identity: ttl must be positive")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return ServerIdentity{}, fmt.Errorf("vmpool: mint server identity: generate key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return ServerIdentity{}, fmt.Errorf("vmpool: mint server identity: generate serial: %w", err)
	}

	now := time.Now().UTC()
	notBefore := now.Add(-clockSkewBackdate)
	notAfter := now.Add(ttl)

	var ipSANs []net.IP
	var dnsSANs []string
	for _, host := range hosts {
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			ipSANs = append(ipSANs, ip)
			continue
		}
		dnsSANs = append(dnsSANs, host)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           ipSANs,
		DNSNames:              dnsSANs,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return ServerIdentity{}, fmt.Errorf("vmpool: mint server identity: create certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return ServerIdentity{}, fmt.Errorf("vmpool: mint server identity: marshal key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return ServerIdentity{CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// MintWorkerToken generates a fresh single-job bearer token: 32 bytes of
// crypto/rand entropy, URL-safe base64 encoded without padding. The token is
// shipped to the droplet via user data and used by the controller's
// executor.HTTPRemoteWorker as the Authorization bearer credential.
func MintWorkerToken() (string, error) {
	buf := make([]byte, workerTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("vmpool: mint worker token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
