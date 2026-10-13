// Package endpoint resolves an already-running HTTP endpoint.
//
// It creates nothing, opens no execution session, and records the network
// identity that answered: DNS addresses and, for HTTPS, the leaf certificate.
// The application suite remains responsible for recording the product build
// it observed because a transport cannot infer application identity honestly.
package endpoint

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/jmurray2011/testease/internal/target"
)

// Resolver is the part of net.Resolver used here, named so resolution can be
// tested without depending on whichever DNS happens to answer the test host.
type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// Options is everything resolution needs that is not part of the scenario.
type Options struct {
	Resolver   Resolver
	ObserveTLS func(context.Context, *url.URL) (TLSIdentity, error)
	RunID      string
	Runner     string
	Now        func() time.Time
}

// TLSIdentity is the transport identity observed during a TLS handshake.
// VerifyError is empty only when the leaf verifies for the origin's hostname
// against this runner's trust store.
type TLSIdentity struct {
	LeafSHA256  string
	Subject     string
	Issuer      string
	Serial      string
	NotBefore   time.Time
	NotAfter    time.Time
	Version     string
	VerifyError string
}

// Provider locates endpoints that already exist.
type Provider struct{ opt Options }

var _ target.Provider = (*Provider)(nil)

// New builds an endpoint provider.
func New(opt Options) *Provider {
	if opt.Resolver == nil {
		opt.Resolver = net.DefaultResolver
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.ObserveTLS == nil {
		opt.ObserveTLS = observeTLS
	}
	return &Provider{opt: opt}
}

// Resolve parses the declared origin and records which network identity
// answers it. Certificate verification is evidence, not an admission gate: a
// lab appliance with a private CA is still a target, but the record must say
// that this runner could not verify its certificate.
func (p *Provider) Resolve(ctx context.Context, spec target.Spec) (target.Target, error) {
	u, err := parseOrigin(spec.Origin)
	if err != nil {
		return nil, fmt.Errorf("host %q: %w", spec.Host, err)
	}

	addrs, err := p.opt.Resolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil {
		return nil, fmt.Errorf("host %q: resolve endpoint %s: %w", spec.Host, u.Hostname(), err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("host %q: endpoint %s resolved no addresses", spec.Host, u.Hostname())
	}
	addresses := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		addresses = append(addresses, addr.IP.String())
	}
	sort.Strings(addresses)

	resolved := map[string]any{
		"endpoint":  u.String(),
		"host":      u.Hostname(),
		"port":      originPort(u),
		"addresses": addresses,
	}
	facts := map[string]string{
		"transport": u.Scheme,
		"dns":       "resolved",
	}
	if u.Scheme == "https" {
		identity, err := p.opt.ObserveTLS(ctx, u)
		if err != nil {
			return nil, fmt.Errorf("host %q: observe TLS identity at %s: %w", spec.Host, u.Redacted(), err)
		}
		recordTLS(identity, resolved, facts)
	} else {
		facts["tls"] = "not-applicable"
	}

	return &endpointTarget{
		origin: *u,
		identity: target.Identity{
			Requested: map[string]any{"provider": "endpoint", "origin": spec.Origin},
			Resolved:  resolved,
			Facts:     facts,
		},
		labels: map[string]string{
			target.LabelRunner:  p.opt.Runner,
			target.LabelRun:     p.opt.RunID,
			target.LabelPID:     fmt.Sprint(os.Getpid()),
			target.LabelCreated: p.opt.Now().UTC().Format(time.RFC3339),
			target.LabelBackend: "endpoint",
			target.LabelHost:    spec.Host,
			target.LabelCell:    spec.Cell,
		},
	}, nil
}

// Release does nothing: the endpoint existed before the run.
func (*Provider) Release(context.Context, target.Target, bool) error { return nil }

func parseOrigin(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("endpoint provider needs origin")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("origin is not a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("origin must use http or https")
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("origin names no host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("origin must contain only scheme, host and optional port")
	}
	if _, err := strconv.Atoi(originPort(u)); err != nil {
		return nil, fmt.Errorf("origin has invalid port: %w", err)
	}
	// Normalize a trailing slash so the same origin has one recorded spelling.
	u.Path = ""
	return u, nil
}

func originPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func observeTLS(ctx context.Context, u *url.URL) (TLSIdentity, error) {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{},
		Config: &tls.Config{
			ServerName:         u.Hostname(),
			InsecureSkipVerify: true, // verification is performed and recorded below
			MinVersion:         tls.VersionTLS12,
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), originPort(u)))
	if err != nil {
		return TLSIdentity{}, err
	}
	defer func() { _ = conn.Close() }()

	state := conn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return TLSIdentity{}, fmt.Errorf("peer supplied no certificate")
	}
	leaf := state.PeerCertificates[0]
	sum := sha256.Sum256(leaf.Raw)
	intermediates := leafPool(state.PeerCertificates[1:])
	_, verifyErr := leaf.Verify(x509.VerifyOptions{DNSName: u.Hostname(), Intermediates: intermediates})
	identity := TLSIdentity{
		LeafSHA256: hex.EncodeToString(sum[:]),
		Subject:    leaf.Subject.String(), Issuer: leaf.Issuer.String(), Serial: leaf.SerialNumber.Text(16),
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, Version: tlsVersion(state.Version),
	}
	if verifyErr != nil {
		identity.VerifyError = verifyErr.Error()
	}
	return identity, nil
}

func recordTLS(identity TLSIdentity, resolved map[string]any, facts map[string]string) {
	resolved["tls_leaf_sha256"] = identity.LeafSHA256
	resolved["tls_subject"] = identity.Subject
	resolved["tls_issuer"] = identity.Issuer
	resolved["tls_serial"] = identity.Serial
	resolved["tls_not_before"] = identity.NotBefore.UTC().Format(time.RFC3339)
	resolved["tls_not_after"] = identity.NotAfter.UTC().Format(time.RFC3339)
	facts["tls"] = "observed"
	facts["tls.version"] = identity.Version
	if identity.VerifyError == "" {
		facts["tls.verified"] = "true"
		return
	}
	facts["tls.verified"] = "false"
	facts["tls.verify_error"] = identity.VerifyError
}

func leafPool(certs []*x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, cert := range certs {
		pool.AddCert(cert)
	}
	return pool
}

func tlsVersion(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "TLS1.3"
	case tls.VersionTLS12:
		return "TLS1.2"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

type endpointTarget struct {
	origin   url.URL
	identity target.Identity
	labels   map[string]string
}

var (
	_ target.Target     = (*endpointTarget)(nil)
	_ target.Endpointer = (*endpointTarget)(nil)
)

func (e *endpointTarget) Identity() target.Identity { return e.identity }
func (e *endpointTarget) Labels() map[string]string { return e.labels }
func (e *endpointTarget) Endpoint() (url.URL, bool) { return e.origin, true }
