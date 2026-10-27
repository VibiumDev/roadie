package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

// certReloader serves a certificate and key from files and picks up new
// ones when the files change, so a renewal needs no restart.
type certReloader struct {
	certFile, keyFile string

	mu      sync.RWMutex
	cert    *tls.Certificate
	checked time.Time
	certMod time.Time
	keyMod  time.Time
}

// recheck is how often the files' timestamps are looked at, at most.
const recheck = 30 * time.Second

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) load() error {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	}
	certMod, keyMod := modTime(r.certFile), modTime(r.keyFile)
	r.mu.Lock()
	r.cert, r.certMod, r.keyMod, r.checked = &cert, certMod, keyMod, time.Now()
	r.mu.Unlock()
	return nil
}

func modTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// GetCertificate is for tls.Config.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.RLock()
	cert, checked, certMod, keyMod := r.cert, r.checked, r.certMod, r.keyMod
	r.mu.RUnlock()
	if time.Since(checked) < recheck {
		return cert, nil
	}
	if modTime(r.certFile) != certMod || modTime(r.keyFile) != keyMod {
		if err := r.load(); err != nil {
			log.Printf("tls: new certificate files not loaded: %v", err)
		} else {
			log.Printf("tls: certificate reloaded from %s", r.certFile)
			r.mu.RLock()
			cert = r.cert
			r.mu.RUnlock()
		}
	}
	r.mu.Lock()
	r.checked = time.Now()
	r.mu.Unlock()
	return cert, nil
}

// Leaf is the certificate being served.
func (r *certReloader) Leaf() *x509.Certificate {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cert == nil {
		return nil
	}
	return r.cert.Leaf
}

// tlsStatus describes a certificate file: `roadie tls status`.
func tlsStatus(w io.Writer, certFile, keyFile string) error {
	pemData, err := os.ReadFile(certFile)
	if err != nil {
		return err
	}
	if keyFile != "" {
		if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
			return fmt.Errorf("certificate and key: %w", err)
		}
	}
	leaf, err := parseFirstPEMCert(pemData)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Certificate: %s\n", certFile)
	fmt.Fprintf(w, "Subject:     %s\n", leaf.Subject)
	if len(leaf.DNSNames) > 0 {
		fmt.Fprintf(w, "Names:       %v\n", leaf.DNSNames)
	}
	fmt.Fprintf(w, "Issuer:      %s\n", leaf.Issuer)
	fmt.Fprintf(w, "Valid from:  %s\n", leaf.NotBefore.Local().Format(time.RFC1123))
	fmt.Fprintf(w, "Valid until: %s\n", leaf.NotAfter.Local().Format(time.RFC1123))
	left := time.Until(leaf.NotAfter)
	switch {
	case left < 0:
		fmt.Fprintf(w, "Expired %s ago\n", (-left).Round(time.Hour))
	case left < 14*24*time.Hour:
		fmt.Fprintf(w, "Expires in %d days: renew soon\n", int(left.Hours()/24))
	default:
		fmt.Fprintf(w, "Expires in %d days\n", int(left.Hours()/24))
	}
	if keyFile != "" {
		fmt.Fprintf(w, "Key:         %s (matches)\n", keyFile)
	}
	return nil
}
