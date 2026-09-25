// Package tlsutil loads the TLS listener certificate, generates a
// self-signed one when none exists and reloads it when the files change.
package tlsutil

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const reloadCheckInterval = time.Minute

type Certs struct {
	certFile, keyFile string

	mu        sync.Mutex
	cert      *tls.Certificate
	certMod   time.Time
	keyMod    time.Time
	lastCheck time.Time
}

// Load reads the certificate pair. If both files are missing, a self-signed
// RSA-2048 certificate valid for 10 years is generated for hosts. If only
// one of them exists, that is an error: the proxy does not guess.
func Load(certFile, keyFile string, hosts []string) (c *Certs, generated bool, err error) {
	_, certErr := os.Stat(certFile)
	_, keyErr := os.Stat(keyFile)
	certMissing, keyMissing := errors.Is(certErr, fs.ErrNotExist), errors.Is(keyErr, fs.ErrNotExist)
	switch {
	case certMissing && keyMissing:
		if err := generate(certFile, keyFile, hosts); err != nil {
			return nil, false, fmt.Errorf("generate self-signed certificate: %w", err)
		}
		generated = true
	case certMissing || keyMissing:
		return nil, false, fmt.Errorf("TLS certificate %s and key %s must both exist or both be absent", certFile, keyFile)
	}
	c = &Certs{certFile: certFile, keyFile: keyFile}
	if err := c.reload(); err != nil {
		return nil, false, err
	}
	return c, generated, nil
}

func (c *Certs) reload() error {
	ci, err := os.Stat(c.certFile)
	if err != nil {
		return err
	}
	ki, err := os.Stat(c.keyFile)
	if err != nil {
		return err
	}
	pair, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	c.cert = &pair
	c.certMod, c.keyMod = ci.ModTime(), ki.ModTime()
	return nil
}

// GetCertificate serves the cached certificate and, at most once a minute,
// re-reads the files if their modification time changed.
func (c *Certs) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if now.Sub(c.lastCheck) >= reloadCheckInterval {
		c.lastCheck = now
		ci, err1 := os.Stat(c.certFile)
		ki, err2 := os.Stat(c.keyFile)
		if err1 == nil && err2 == nil && (!ci.ModTime().Equal(c.certMod) || !ki.ModTime().Equal(c.keyMod)) {
			old := c.cert
			if err := c.reload(); err != nil {
				c.cert = old
				slog.Warn("TLS certificate changed but cannot be loaded; keeping the old one", "err", err)
			} else {
				slog.Info("TLS certificate reloaded", "sha256", fingerprint(c.cert.Leaf))
			}
		}
	}
	return c.cert, nil
}

type Info struct {
	Type     string    `json:"type"` // self-signed | loaded
	SHA256   string    `json:"sha256"`
	Subject  string    `json:"subject"`
	DNSNames []string  `json:"dns_names"`
	NotAfter time.Time `json:"not_after"`
}

func (c *Certs) Info() Info {
	c.mu.Lock()
	leaf := c.cert.Leaf
	c.mu.Unlock()
	if leaf == nil {
		return Info{}
	}
	typ := "loaded"
	if bytes.Equal(leaf.RawIssuer, leaf.RawSubject) && leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil {
		typ = "self-signed"
	}
	names := append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		names = append(names, ip.String())
	}
	return Info{Type: typ, SHA256: fingerprint(leaf), Subject: leaf.Subject.CommonName, DNSNames: names, NotAfter: leaf.NotAfter}
}

func fingerprint(leaf *x509.Certificate) string {
	if leaf == nil {
		return ""
	}
	sum := sha256.Sum256(leaf.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

func generate(certFile, keyFile string, hosts []string) error {
	if len(hosts) == 0 {
		h, _ := os.Hostname()
		if h == "" {
			h = "localhost"
		}
		hosts = []string{h}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hosts[0], Organization: []string{"sha256-stratum-proxy"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	for _, f := range []string{certFile, keyFile} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			return err
		}
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return writePair(certFile, certPEM, keyFile, keyPEM)
}

// writePair stages both files before installing either one. If the second
// rename fails, the first is removed so the next startup can regenerate the
// pair instead of being stuck with only one file.
func writePair(certFile string, certPEM []byte, keyFile string, keyPEM []byte) (err error) {
	keyTmp, err := writeTemp(keyFile, keyPEM)
	if err != nil {
		return err
	}
	defer func() {
		if keyTmp != "" {
			os.Remove(keyTmp)
		}
	}()
	certTmp, err := writeTemp(certFile, certPEM)
	if err != nil {
		return err
	}
	defer func() {
		if certTmp != "" {
			os.Remove(certTmp)
		}
	}()

	if err := os.Rename(keyTmp, keyFile); err != nil {
		return fmt.Errorf("install TLS key: %w", err)
	}
	keyTmp = ""
	if err := os.Rename(certTmp, certFile); err != nil {
		removeErr := os.Remove(keyFile)
		if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			return errors.Join(fmt.Errorf("install TLS certificate: %w", err), fmt.Errorf("remove partial TLS key: %w", removeErr))
		}
		return fmt.Errorf("install TLS certificate: %w", err)
	}
	certTmp = ""

	seen := map[string]bool{}
	for _, dir := range []string{filepath.Dir(keyFile), filepath.Dir(certFile)} {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if d, openErr := os.Open(dir); openErr == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
}

func writeTemp(path string, data []byte) (name string, err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return "", err
	}
	name = f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(name)
		}
	}()
	if err := f.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return name, nil
}

// MinVersion maps the tls_min_version setting to a crypto/tls constant.
func MinVersion(v string) uint16 {
	switch v {
	case "1.0":
		return tls.VersionTLS10
	case "1.1":
		return tls.VersionTLS11
	case "1.3":
		return tls.VersionTLS13
	}
	return tls.VersionTLS12
}
