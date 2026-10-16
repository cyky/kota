package engine

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Certs is the TLS material of one session's Engine VM: a CA made for the
// session, the server certificate dockerd presents, and the one client
// certificate it accepts. Nothing is reused, so a certificate from one
// session or project opens no other.
type Certs struct {
	CA, ServerCert, ServerKey, ClientCert, ClientKey []byte
}

// certLifetime covers a long session. The VM's clock is the host's.
const certLifetime = 30 * 24 * time.Hour

// NewCerts makes a CA and signs a server certificate for hosts (names and
// IP addresses the API is reached by) and a client certificate. Keys are
// EC P-256.
func NewCerts(now time.Time, hosts []string) (Certs, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Certs{}, err
	}
	ca := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "kota engine CA"},
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, caCert, err := sign(now, ca, nil, &caKey.PublicKey, caKey)
	if err != nil {
		return Certs{}, err
	}
	server := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "kota engine"},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			server.IPAddresses = append(server.IPAddresses, ip)
		} else {
			server.DNSNames = append(server.DNSNames, h)
		}
	}
	client := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "kota sandbox"},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	c := Certs{CA: pemBlock("CERTIFICATE", caDER)}
	for _, leaf := range []struct {
		tmpl      *x509.Certificate
		cert, key *[]byte
	}{{server, &c.ServerCert, &c.ServerKey}, {client, &c.ClientCert, &c.ClientKey}} {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return Certs{}, err
		}
		der, _, err := sign(now, leaf.tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			return Certs{}, err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return Certs{}, err
		}
		*leaf.cert = pemBlock("CERTIFICATE", der)
		*leaf.key = pemBlock("PRIVATE KEY", keyDER)
	}
	return c, nil
}

// sign fills in the serial number and validity of tmpl and signs it with
// parent (tmpl itself when nil).
func sign(now time.Time, tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) ([]byte, *x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	if err != nil {
		return nil, nil, err
	}
	tmpl.SerialNumber = serial
	tmpl.NotBefore = now.Add(-time.Hour)
	tmpl.NotAfter = now.Add(certLifetime)
	if parent == nil {
		parent = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return der, cert, err
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

// WriteServer writes the files the Engine VM gets through fw_cfg into dir
// and returns their paths. The server key never goes into the sandbox.
func (c Certs) WriteServer(dir string) (ServerCerts, error) {
	s := ServerCerts{CA: filepath.Join(dir, "ca.pem"), Cert: filepath.Join(dir, "server.pem"), Key: filepath.Join(dir, "server-key.pem")}
	return s, writeFiles(dir, map[string][]byte{s.CA: c.CA, s.Cert: c.ServerCert, s.Key: c.ServerKey})
}

// WriteClient writes the client side into dir under the names the Docker
// CLI and testcontainers expect in DOCKER_CERT_PATH.
func (c Certs) WriteClient(dir string) error {
	return writeFiles(dir, map[string][]byte{
		filepath.Join(dir, "ca.pem"):   c.CA,
		filepath.Join(dir, "cert.pem"): c.ClientCert,
		filepath.Join(dir, "key.pem"):  c.ClientKey,
	})
}

func writeFiles(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, data := range files {
		if err := os.WriteFile(name, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}
