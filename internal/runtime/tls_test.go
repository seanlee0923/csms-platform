package runtime

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seanlee0923/ocpp/protocol"
)

// testCA holds an in-memory self-signed CA used only to issue short-lived
// server/client certificates for these tests. It never touches disk except
// through writeTempPEM.
type testCA struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	return &testCA{cert: cert, key: key}
}

func (ca *testCA) pem() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw})
}

// issue signs a leaf certificate for commonName. ipSANs is only meaningful
// for server certificates (client certificates are identified by CN alone
// in these tests).
func (ca *testCA) issue(t *testing.T, commonName string, extKeyUsage x509.ExtKeyUsage, ipSANs []net.IP) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{extKeyUsage},
		IPAddresses:  ipSANs,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create leaf certificate for %q: %v", commonName, err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}

func writeTempPEM(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestRuntimeTerminatesTLSAndEnforcesMutualTLS proves the whole chain the
// Operator's CSMS.spec.tls contract promises: with a server cert/key and a
// client CA present at the configured paths, the Runtime serves HTTPS,
// rejects connections without a client certificate at the TLS handshake
// layer, and rejects a WebSocket upgrade whose client certificate CN does
// not match the requested station identity — while still letting a
// correctly identified station connect.
func TestRuntimeTerminatesTLSAndEnforcesMutualTLS(t *testing.T) {
	dir := t.TempDir()
	ca := newTestCA(t)
	caFile := writeTempPEM(t, dir, "ca.crt", ca.pem())

	serverCertPEM, serverKeyPEM := ca.issue(t, "runtime-server", x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")})
	certFile := writeTempPEM(t, dir, "tls.crt", serverCertPEM)
	keyFile := writeTempPEM(t, dir, "tls.key", serverKeyPEM)

	config := Config{
		HTTPAddr: ":0", HeartbeatInterval: 123, ShutdownTimeout: time.Second,
		Versions:        []protocol.Version{protocol.OCPP16},
		TLSCertFile:     certFile,
		TLSKeyFile:      keyFile,
		TLSClientCAFile: caFile,
	}
	server, err := New(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if server.tlsConfig == nil {
		t.Fatal("expected TLS to activate when cert/key/CA files are present")
	}
	if server.tlsConfig.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("expected mutual TLS to be required, got ClientAuth=%v", server.tlsConfig.ClientAuth)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		<-errCh
	})

	addr := listener.Addr().String()
	rootPool := x509.NewCertPool()
	rootPool.AddCert(ca.cert)

	// Plain HTTP against a TLS-only listener must never succeed. Go's
	// net/http server recognizes a plaintext request on a TLS listener and
	// answers with a readable 400 instead of just dropping the connection,
	// so either a client-side error or a non-200 status is an acceptable
	// rejection here.
	plainClient := &http.Client{Timeout: time.Second}
	if resp, err := plainClient.Get("http://" + addr + "/readyz"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("expected plain HTTP request to a TLS-only listener to not return 200")
		}
	}

	// TLS handshake without presenting any client certificate must fail —
	// enforced by tls.RequireAndVerifyClientCert itself, before any HTTP
	// request is processed.
	noCertClient := &http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: rootPool}},
	}
	if _, err := noCertClient.Get("https://" + addr + "/readyz"); err == nil {
		t.Fatal("expected TLS handshake without a client certificate to fail")
	}

	// A verified client certificate with ANY CN may reach the plain HTTP
	// endpoints (/readyz here) — CN-to-identity matching is enforced only
	// on the OCPP WebSocket upgrade path, not on health checks.
	bystanderCertPEM, bystanderKeyPEM := ca.issue(t, "some-other-station", x509.ExtKeyUsageClientAuth, nil)
	bystanderCert, err := tls.X509KeyPair(bystanderCertPEM, bystanderKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	bystanderClient := &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: rootPool, Certificates: []tls.Certificate{bystanderCert},
		}},
	}
	readyResp, err := bystanderClient.Get("https://" + addr + "/readyz")
	if err != nil {
		t.Fatalf("expected /readyz to accept any verified client certificate, got error: %v", err)
	}
	readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected /readyz 200, got %d", readyResp.StatusCode)
	}

	// A WebSocket upgrade to /{identity} with a client certificate whose CN
	// does NOT match identity must be rejected.
	const identity = "station-42"
	wrongCNDialer := websocket.Dialer{
		Subprotocols: []string{"ocpp1.6"},
		TLSClientConfig: &tls.Config{
			RootCAs: rootPool, Certificates: []tls.Certificate{bystanderCert},
		},
		HandshakeTimeout: time.Second,
	}
	if _, resp, err := wrongCNDialer.Dial("wss://"+addr+"/"+identity, nil); err == nil {
		t.Fatal("expected WebSocket upgrade with mismatched client certificate CN to fail")
	} else if resp == nil || resp.StatusCode != http.StatusForbidden {
		status := -1
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("expected 403 for CN mismatch, got status=%d err=%v", status, err)
	}

	// A WebSocket upgrade with a client certificate whose CN matches
	// identity must succeed.
	matchingCertPEM, matchingKeyPEM := ca.issue(t, identity, x509.ExtKeyUsageClientAuth, nil)
	matchingCert, err := tls.X509KeyPair(matchingCertPEM, matchingKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	matchingDialer := websocket.Dialer{
		Subprotocols: []string{"ocpp1.6"},
		TLSClientConfig: &tls.Config{
			RootCAs: rootPool, Certificates: []tls.Certificate{matchingCert},
		},
		HandshakeTimeout: time.Second,
	}
	connection, _, err := matchingDialer.Dial("wss://"+addr+"/"+identity, nil)
	if err != nil {
		t.Fatalf("expected WebSocket upgrade with matching client certificate CN to succeed: %v", err)
	}
	defer connection.Close()
	if connection.Subprotocol() != "ocpp1.6" {
		t.Fatalf("unexpected subprotocol %q", connection.Subprotocol())
	}
}

// TestBuildTLSConfigRequiresBothCertAndKey proves a half-configured TLS
// pair (only one of cert/key present) fails fast at startup instead of
// silently falling back to plain HTTP or serving with a missing key.
func TestBuildTLSConfigRequiresBothCertAndKey(t *testing.T) {
	dir := t.TempDir()
	ca := newTestCA(t)
	serverCertPEM, _ := ca.issue(t, "runtime-server", x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")})
	certFile := writeTempPEM(t, dir, "tls.crt", serverCertPEM)

	config := Config{TLSCertFile: certFile, TLSKeyFile: filepath.Join(dir, "does-not-exist.key")}
	if _, err := buildTLSConfig(config); err == nil {
		t.Fatal("expected an error when only the certificate file exists")
	}
}

// TestBuildTLSConfigDisabledWithoutFiles proves the default, most common
// case: no files at the configured paths means plain HTTP, not an error.
func TestBuildTLSConfigDisabledWithoutFiles(t *testing.T) {
	dir := t.TempDir()
	config := Config{
		TLSCertFile:     filepath.Join(dir, "tls.crt"),
		TLSKeyFile:      filepath.Join(dir, "tls.key"),
		TLSClientCAFile: filepath.Join(dir, "ca.crt"),
	}
	tlsConfig, err := buildTLSConfig(config)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if tlsConfig != nil {
		t.Fatalf("expected TLS to stay disabled without cert/key files, got %+v", tlsConfig)
	}
}
