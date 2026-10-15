package engine

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func parseCert(t *testing.T, data []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func pool(t *testing.T, ca []byte) *x509.CertPool {
	t.Helper()
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(ca) {
		t.Fatal("bad CA")
	}
	return p
}

func newCerts(t *testing.T, now time.Time) Certs {
	t.Helper()
	c, err := NewCerts(now, []string{"engine", "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClientCertOnlyForItsOwnCA(t *testing.T) {
	now := time.Now()
	a, b := newCerts(t, now), newCerts(t, now)
	client := parseCert(t, a.ClientCert)
	opts := x509.VerifyOptions{Roots: pool(t, a.CA), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: now}
	if _, err := client.Verify(opts); err != nil {
		t.Errorf("client cert not accepted by its own CA: %v", err)
	}
	opts.Roots = pool(t, b.CA)
	if _, err := client.Verify(opts); err == nil {
		t.Error("client cert accepted by another session's CA")
	}
	// The server certificate can't stand in for a client one.
	opts.Roots = pool(t, a.CA)
	if _, err := parseCert(t, a.ServerCert).Verify(opts); err == nil {
		t.Error("server cert accepted as a client cert")
	}
}

func TestServerCertNames(t *testing.T) {
	now := time.Now()
	c := newCerts(t, now)
	server := parseCert(t, c.ServerCert)
	for _, name := range []string{"engine", "127.0.0.1"} {
		opts := x509.VerifyOptions{Roots: pool(t, c.CA), DNSName: name, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, CurrentTime: now}
		if _, err := server.Verify(opts); err != nil {
			t.Errorf("server cert not valid for %s: %v", name, err)
		}
	}
	opts := x509.VerifyOptions{Roots: pool(t, c.CA), DNSName: "host.docker.internal", CurrentTime: now}
	if _, err := server.Verify(opts); err == nil {
		t.Error("server cert valid for a name it wasn't made for")
	}
	if _, err := server.Verify(x509.VerifyOptions{Roots: pool(t, c.CA), DNSName: "engine", CurrentTime: now.Add(certLifetime + time.Hour)}); err == nil {
		t.Error("server cert valid after its lifetime")
	}
}

func TestKeysMatchCerts(t *testing.T) {
	c := newCerts(t, time.Now())
	if _, err := tls.X509KeyPair(c.ServerCert, c.ServerKey); err != nil {
		t.Errorf("server pair: %v", err)
	}
	if _, err := tls.X509KeyPair(c.ClientCert, c.ClientKey); err != nil {
		t.Errorf("client pair: %v", err)
	}
}

func TestWriteKeepsServerKeyOutOfClientDir(t *testing.T) {
	c := newCerts(t, time.Now())
	dir := t.TempDir()
	server, err := c.WriteServer(filepath.Join(dir, "server"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteClient(filepath.Join(dir, "client")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "client"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		data, _ := os.ReadFile(filepath.Join(dir, "client", e.Name()))
		if string(data) == string(c.ServerKey) || string(data) == string(c.ServerCert) {
			t.Errorf("client dir holds server material in %s", e.Name())
		}
	}
	if len(names) != 3 {
		t.Errorf("client dir = %q, want ca.pem, cert.pem, key.pem", names)
	}
	for _, f := range []string{server.CA, server.Cert, server.Key} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", f, info.Mode().Perm())
		}
	}
}
