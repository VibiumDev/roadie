package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCert makes a self-signed certificate for names into dir.
func writeCert(t *testing.T, dir string, serial int64, names ...string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(60 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return
}

func TestCertReloader(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeCert(t, dir, 1, "roadie.example")
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.GetCertificate(&tls.ClientHelloInfo{})
	if c.Leaf.SerialNumber.Int64() != 1 {
		t.Fatalf("serial %v", c.Leaf.SerialNumber)
	}
	// New files: picked up once the recheck interval has passed.
	time.Sleep(20 * time.Millisecond)
	writeCert(t, dir, 2, "roadie.example")
	later := time.Now().Add(time.Hour)
	r.mu.Lock()
	r.checked = r.checked.Add(-recheck)
	r.mu.Unlock()
	_ = later
	c, _ = r.GetCertificate(&tls.ClientHelloInfo{})
	if c.Leaf.SerialNumber.Int64() != 2 {
		t.Errorf("after rewrite: serial %v, want 2", c.Leaf.SerialNumber)
	}

	var out bytes.Buffer
	if err := tlsStatus(&out, certFile, keyFile); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"roadie.example", "Expires in 59 days", "matches"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
	if err := tlsStatus(&out, certFile, filepath.Join(dir, "missing.pem")); err == nil {
		t.Error("a missing key passed")
	}
}
