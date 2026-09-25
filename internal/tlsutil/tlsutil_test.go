package tlsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelfSignedGeneration(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "certs", "fullchain.pem"), filepath.Join(dir, "certs", "privkey.pem")
	c, generated, err := Load(cert, key, []string{"vds.example.com", "203.0.113.5"})
	if err != nil || !generated {
		t.Fatalf("load: %v generated=%v", err, generated)
	}
	info := c.Info()
	if info.Type != "self-signed" || len(info.DNSNames) != 2 || info.SHA256 == "" {
		t.Fatalf("info: %+v", info)
	}
	// Second start reuses the files.
	c2, generated, err := Load(cert, key, nil)
	if err != nil || generated || c2.Info().SHA256 != info.SHA256 {
		t.Fatalf("reload: %v generated=%v", err, generated)
	}
	// Only one of the pair present is an error.
	os.Remove(key)
	if _, _, err := Load(cert, key, nil); err == nil {
		t.Fatal("expected error when the key is missing")
	}
}

func TestWritePairRollsBackPartialInstall(t *testing.T) {
	dir := t.TempDir()
	certTarget := filepath.Join(dir, "cert-target")
	if err := os.Mkdir(certTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	keyTarget := filepath.Join(dir, "privkey.pem")
	if err := writePair(certTarget, []byte("cert"), keyTarget, []byte("key")); err == nil {
		t.Fatal("expected certificate install to fail")
	}
	if _, err := os.Stat(keyTarget); !os.IsNotExist(err) {
		t.Fatalf("partial key was not removed: %v", err)
	}
}
