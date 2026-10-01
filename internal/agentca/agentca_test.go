package agentca

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/store/storetest"
)

func TestLoadOrCreatePersists(t *testing.T) {
	pool := storetest.New(t)
	k, _ := crypto.GenerateKey()
	kr, _ := crypto.NewKeyring(k)
	a, err := LoadOrCreate(context.Background(), pool, kr)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreate(context.Background(), pool, kr)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.CertPEM()) != string(b.CertPEM()) {
		t.Fatal("CA regenerated instead of loaded")
	}
	other, _ := crypto.GenerateKey()
	kr2, _ := crypto.NewKeyring(other)
	if _, err := LoadOrCreate(context.Background(), pool, kr2); err == nil {
		t.Fatal("CA key opened with the wrong master key")
	}
}

// TestMutualTLS wires an agent-style server and a server-style client
// exactly as pgdock does, and checks both directions of verification.
func TestMutualTLS(t *testing.T) {
	ca, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	csr, keyPEM, err := NewCSR()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, fp, err := ca.SignAgent(csr, "1234", []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	agentCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := pem.Decode(certPEM)
	if Fingerprint(b.Bytes) != fp {
		t.Fatal("fingerprint mismatch")
	}
	leaf, _ := x509.ParseCertificate(b.Bytes)
	if id, ok := NodeIDFromCert(leaf); !ok || id != "1234" {
		t.Fatalf("node id %q", id)
	}

	agent := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	agent.TLS = &tls.Config{
		Certificates: []tls.Certificate{agentCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca.Pool(),
		MinVersion:   tls.VersionTLS13,
	}
	agent.StartTLS()
	defer agent.Close()

	clientCert, err := ca.ServerClientCert()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		Certificates: []tls.Certificate{clientCert}, RootCAs: ca.Pool(), MinVersion: tls.VersionTLS13,
	}}}
	res, err := client.Get(agent.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if string(body) != ServerCN {
		t.Fatalf("agent saw %q", body)
	}

	// A client without the CA's certificate is refused.
	noCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca.Pool()}}}
	if res, err := noCert.Get(agent.URL); err == nil {
		_ = res.Body.Close()
		t.Fatal("agent accepted a client without a certificate")
	}
	// A different CA's client certificate is refused.
	rogue, _ := generate()
	rogueCert, _ := rogue.ServerClientCert()
	rc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{rogueCert}, RootCAs: ca.Pool()}}}
	if res, err := rc.Get(agent.URL); err == nil {
		_ = res.Body.Close()
		t.Fatal("agent accepted a client from another CA")
	}
}
