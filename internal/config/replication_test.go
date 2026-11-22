package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCert drops a self-signed cert and key into dir and returns their paths.
func writeCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "zdap-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	certPath = filepath.Join(dir, "node.crt")
	keyPath = filepath.Join(dir, "node.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))

	kb, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyPath,
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600))
	return certPath, keyPath
}

// Unconfigured is standalone, and must stay silent rather than erroring.
func Test_LoadTLS_noneSet_isStandalone(t *testing.T) {
	m, err := (&Config{}).LoadTLS()
	assert.NoError(t, err)
	assert.Nil(t, m)
}

func Test_LoadTLS_allSet(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeCert(t, dir)

	m, err := (&Config{TLSCa: cert, TLSCert: cert, TLSKey: key}).LoadTLS()
	require.NoError(t, err)
	require.NotNil(t, m)

	// The listener must demand a client certificate, not merely offer TLS:
	// without this a peer needs no identity at all to push a dataset.
	assert.Equal(t, "RequireAndVerifyClientCert", m.ServerTLS().ClientAuth.String())
	assert.NotNil(t, m.ClientTLS().RootCAs)
	assert.NotEmpty(t, m.ClientTLS().Certificates, "same cert is presented as client identity")

	// Pinned deliberately: a base streams for hours in a single request, and
	// h2 appearing after a refactor would change that quietly.
	assert.Equal(t, []string{"http/1.1"}, m.ServerTLS().NextProtos)
	assert.Equal(t, []string{"http/1.1"}, m.ClientTLS().NextProtos)
}

// The dangerous middle case: partially configured must be fatal, never a quiet
// fall back to running without replication.
func Test_LoadTLS_partial_isFatal(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeCert(t, dir)

	for name, cfg := range map[string]*Config{
		"only ca":        {TLSCa: cert},
		"only cert":      {TLSCert: cert},
		"only key":       {TLSKey: key},
		"ca and cert":    {TLSCa: cert, TLSCert: cert},
		"cert and key":   {TLSCert: cert, TLSKey: key},
		"missing key":    {TLSCa: cert, TLSCert: cert},
		"ca without key": {TLSCa: cert, TLSKey: key},
	} {
		m, err := cfg.LoadTLS()
		assert.Error(t, err, name)
		assert.Nil(t, m, name)
	}
}

func Test_LoadTLS_unusableFiles_areFatal(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeCert(t, dir)
	junk := filepath.Join(dir, "junk.pem")
	require.NoError(t, os.WriteFile(junk, []byte("not a certificate"), 0o600))

	for name, cfg := range map[string]*Config{
		"missing ca file":   {TLSCa: filepath.Join(dir, "nope.crt"), TLSCert: cert, TLSKey: key},
		"ca is not a cert":  {TLSCa: junk, TLSCert: cert, TLSKey: key},
		"missing cert file": {TLSCa: cert, TLSCert: filepath.Join(dir, "nope.crt"), TLSKey: key},
		"key does not match": func() *Config {
			other := t.TempDir()
			_, otherKey := writeCert(t, other)
			return &Config{TLSCa: cert, TLSCert: cert, TLSKey: otherKey}
		}(),
	} {
		m, err := cfg.LoadTLS()
		assert.Error(t, err, name)
		assert.Nil(t, m, name)
	}
}
