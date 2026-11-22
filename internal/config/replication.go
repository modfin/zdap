package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// Replication is deliberately all-or-nothing.
//
// Three states, and only three:
//
//	none of the TLS paths set   -> replication off, standalone, today's behaviour
//	all set and loadable        -> replication on
//	some set, or a file bad     -> hard error, refuse to start
//
// The middle case is the dangerous one: a daemon that comes up looking healthy
// with replication quietly disabled would let bases go stale on every receiving
// node with nothing to show for it.

// ReplicationRequested reports whether any TLS path is set at all.
func (c *Config) ReplicationRequested() bool {
	return c.TLSCa != "" || c.TLSCert != "" || c.TLSKey != ""
}

// ReplicationEnabled reports whether all three TLS paths are set. It does not
// check that they load; call LoadTLS for that.
func (c *Config) ReplicationEnabled() bool {
	return c.TLSCa != "" && c.TLSCert != "" && c.TLSKey != ""
}

// TLSMaterial is a node's own certificate plus the CA it trusts peers under.
type TLSMaterial struct {
	Cert tls.Certificate
	CAs  *x509.CertPool
}

// LoadTLS reads the configured certificate, key and CA.
//
// It returns (nil, nil) when replication is not configured at all, and an error
// when it is configured incompletely or the files cannot be used. Callers must
// treat that error as fatal rather than falling back to running without
// replication.
func (c *Config) LoadTLS() (*TLSMaterial, error) {
	if !c.ReplicationRequested() {
		return nil, nil
	}
	var missing []string
	for _, f := range []struct{ name, value string }{
		{"ZDAP_TLS_CA", c.TLSCa},
		{"ZDAP_TLS_CERT", c.TLSCert},
		{"ZDAP_TLS_KEY", c.TLSKey},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("replication TLS is partially configured; missing %v "+
			"(set all three, or none to run standalone)", missing)
	}

	cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("could not load replication certificate %s / key %s: %w",
			c.TLSCert, c.TLSKey, err)
	}

	pem, err := os.ReadFile(c.TLSCa)
	if err != nil {
		return nil, fmt.Errorf("could not read replication CA %s: %w", c.TLSCa, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("replication CA %s contains no usable certificate", c.TLSCa)
	}

	return &TLSMaterial{Cert: cert, CAs: pool}, nil
}

// http1Only pins the protocol rather than leaving it to chance.
//
// A base is one request that streams for hours, and HTTP/1.1 is the simple case
// for that: one connection, one body, no stream flow control between us and the
// socket. Today h2 is off anyway, but only by omission -- the server uses
// Serve on a tls.Listener rather than ServeTLS, and the client sets an explicit
// TLSClientConfig, both of which suppress Go's automatic HTTP/2. Either could
// be undone by an innocuous refactor, silently changing how a 117 GB upload
// behaves. Saying it out loud costs nothing and makes it a decision.
var http1Only = []string{"http/1.1"}

// ServerTLS is the config for the replication listener: every peer must present
// a certificate signed by our CA.
func (m *TLSMaterial) ServerTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.Cert},
		ClientCAs:    m.CAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   http1Only,
	}
}

// ClientTLS is the config used when pushing to a peer. The same certificate
// serves as the client identity, so a node proves who it is in both directions.
func (m *TLSMaterial) ClientTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.Cert},
		RootCAs:      m.CAs,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   http1Only,
	}
}
