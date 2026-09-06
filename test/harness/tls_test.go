package harness

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	// Backdated so a clock that drifted since the process started cannot reject
	// a certificate minted seconds ago; the ceiling only has to outlast a run.
	certBackdate = 5 * time.Minute
	certLifetime = 2 * time.Hour

	// A handshake against a listener already proven to be up is either immediate
	// or wrong.
	tlsProbeTimeout = 5 * time.Second
)

// The edge→gateway leg is the one leg this repository fixes at
// `insecure: false`, so CI has to supply a chain rather than switch the setting
// off. Minted per run: a committed key is a committed credential, and a fixed
// expiry becomes a build that breaks on a date nobody chose.
type gatewayChain struct {
	caFile   string
	certFile string
	keyFile  string
	roots    *x509.CertPool
}

func mintGatewayChain(t *testing.T, dir string) gatewayChain {
	t.Helper()

	notBefore := time.Now().Add(-certBackdate)
	notAfter := time.Now().Add(certLifetime)

	caKey := generateKey(t)
	caTemplate := &x509.Certificate{
		SerialNumber:          serialNumber(t),
		Subject:               pkix.Name{CommonName: "otelbox integration test CA"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("could not self-sign the per-run CA: %v", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("could not parse the CA this run just minted: %v", err)
	}

	leafKey := generateKey(t)
	leafTemplate := &x509.Certificate{
		SerialNumber: serialNumber(t),
		Subject:      pkix.Name{CommonName: "otelbox integration test gateway"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// An IP SAN, not a CommonName: Go removed the CommonName fallback, so a
		// certificate without this fails verification however it is otherwise
		// correct.
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("could not sign the gateway's leaf certificate: %v", err)
	}
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatalf("could not marshal the gateway's private key: %v", err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(ca)

	chain := gatewayChain{
		caFile:   filepath.Join(dir, "gateway-ca.pem"),
		certFile: filepath.Join(dir, "gateway-cert.pem"),
		keyFile:  filepath.Join(dir, "gateway-key.pem"),
		roots:    roots,
	}
	writePEM(t, chain.caFile, "CERTIFICATE", caDER, 0o644)
	writePEM(t, chain.certFile, "CERTIFICATE", leafDER, 0o644)
	writePEM(t, chain.keyFile, "PRIVATE KEY", leafKeyDER, 0o600)
	return chain
}

// A certificate fault — an IP SAN that does not cover 127.0.0.1, an expired
// leaf, a CA the edge was never given — stops delivery and drives
// otelcol_exporter_send_failed_* up, which is precisely the signature the token
// assertion reads. Same endpoint and same pool the edge is handed, so a pass is
// about the edge's own leg rather than some other one.
func (g gatewayChain) verifyServed(endpoint string) error {
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: tlsProbeTimeout},
		"tcp", endpoint,
		&tls.Config{
			RootCAs:    g.roots,
			MinVersion: tls.VersionTLS12,
			// What the exporter offers, so the probe cannot pass on a
			// negotiation the collector's own client would fail.
			NextProtos: []string{"h2"},
		},
	)
	if err != nil {
		return fmt.Errorf("handshaking with %s against the CA the edge trusts: %w", endpoint, err)
	}
	return conn.Close()
}

// verifyRejectsUncertified connects to the gateway without offering a client
// certificate and expects the connection to be refused. A deterministic probe
// instead of a log-file poll: it removes the gRPC reconnect-backoff timing
// dependency that made the log-based check flaky on slow CI runners.
//
// A successful dial is not evidence of acceptance. Under TLS 1.3 the client
// completes its handshake before the server has inspected the client
// certificate, so Dial returns nil and the `certificate required` alert only
// arrives as the first record afterwards. The probe therefore reads once: a
// read error is the refusal, and only readable application data proves the
// gateway let an uncertified peer through. Under TLS 1.2 the refusal still
// surfaces at Dial, which the first branch keeps covering.
func (g gatewayChain) verifyRejectsUncertified(endpoint string) error {
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: tlsProbeTimeout},
		"tcp", endpoint,
		&tls.Config{
			RootCAs:    g.roots,
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2"},
		},
	)
	if err != nil {
		return nil
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(tlsProbeTimeout)); err != nil {
		return fmt.Errorf("setting a read deadline on the probe to %s: %w", endpoint, err)
	}
	switch _, err := conn.Read(make([]byte, 1)); {
	case err == nil:
		return fmt.Errorf("the gateway at %s served an uncertified peer", endpoint)
	case os.IsTimeout(err):
		// The gateway neither rejected the peer nor spoke to it. Silence is
		// not a refusal: the connection stands open and a gRPC client would
		// carry on with it.
		return fmt.Errorf("the gateway at %s held an uncertified connection open without rejecting it", endpoint)
	default:
		return nil
	}
}

// The optional second factor on the same leg, supplied only by the scenario
// that loads test/config/gateway-mtls-ci.yaml. Both leaves are minted up front
// and written to one pair of paths in turn: the rotation claim replaces the
// material under a live exporter, which can only be stated if the paths the
// process started with never move.
type clientChain struct {
	caFile    string
	certFile  string
	keyFile   string
	trusted   clientCertificate
	untrusted clientCertificate
}

type clientCertificate struct {
	certDER []byte
	keyDER  []byte
}

func mintClientChain(t *testing.T, dir string) clientChain {
	t.Helper()

	trustedCA := mintCA(t, "otelbox integration test client CA")
	// A second authority the gateway is never given, rather than a corrupted
	// leaf: an unknown issuer is the failure a real deployment sees, and it is
	// the one the gateway's client_ca_file is there to produce.
	untrustedCA := mintCA(t, "otelbox integration test unknown client CA")

	chain := clientChain{
		caFile:    filepath.Join(dir, "client-ca.pem"),
		certFile:  filepath.Join(dir, "client-cert.pem"),
		keyFile:   filepath.Join(dir, "client-key.pem"),
		trusted:   trustedCA.signClientLeaf(t, "otelbox integration test edge"),
		untrusted: untrustedCA.signClientLeaf(t, "otelbox integration test unknown edge"),
	}
	writePEM(t, chain.caFile, "CERTIFICATE", trustedCA.der, 0o644)
	return chain
}

func (c clientChain) install(t *testing.T, certificate clientCertificate) {
	t.Helper()

	writePEM(t, c.certFile, "CERTIFICATE", certificate.certDER, 0o644)
	writePEM(t, c.keyFile, "PRIVATE KEY", certificate.keyDER, 0o600)
}

type certificateAuthority struct {
	certificate *x509.Certificate
	der         []byte
	key         *ecdsa.PrivateKey
}

func mintCA(t *testing.T, commonName string) certificateAuthority {
	t.Helper()

	key := generateKey(t)
	template := &x509.Certificate{
		SerialNumber:          serialNumber(t),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-certBackdate),
		NotAfter:              time.Now().Add(certLifetime),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("could not self-sign the %q CA: %v", commonName, err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("could not parse the %q CA this run just minted: %v", commonName, err)
	}
	return certificateAuthority{certificate: certificate, der: der, key: key}
}

func (ca certificateAuthority) signClientLeaf(t *testing.T, commonName string) clientCertificate {
	t.Helper()

	key := generateKey(t)
	template := &x509.Certificate{
		SerialNumber: serialNumber(t),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-certBackdate),
		NotAfter:     time.Now().Add(certLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		// No SAN, because a verifier checks no name on a client leaf, and this
		// extended usage, because it does check that: a leaf carrying only
		// ExtKeyUsageServerAuth is refused with an otherwise valid chain.
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.certificate, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("could not sign the %q client leaf: %v", commonName, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("could not marshal the %q client key: %v", commonName, err)
	}
	return clientCertificate{certDER: der, keyDER: keyDER}
}

func generateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("could not generate a P-256 key: %v", err)
	}
	return key
}

func serialNumber(t *testing.T) *big.Int {
	t.Helper()

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("could not draw a certificate serial number: %v", err)
	}
	return serial
}

func writePEM(t *testing.T, path, blockType string, der []byte, mode os.FileMode) {
	t.Helper()

	encoded := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if encoded == nil {
		t.Fatalf("could not PEM-encode the %s destined for %s", blockType, path)
	}
	if err := os.WriteFile(path, encoded, mode); err != nil {
		t.Fatalf("could not write %s: %v", path, err)
	}
}
