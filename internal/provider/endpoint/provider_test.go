package endpoint_test

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	endpointprovider "github.com/jmurray2011/testease/internal/provider/endpoint"
	"github.com/jmurray2011/testease/internal/target"
)

type resolver struct{ addresses []net.IPAddr }

func (r resolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, nil
}

func TestAnHTTPEndpointRecordsTheDeclaredAndResolvedIdentity(t *testing.T) {
	t.Parallel()

	p := endpointprovider.New(endpointprovider.Options{
		Resolver: resolver{addresses: []net.IPAddr{{IP: net.ParseIP("192.0.2.9")}, {IP: net.ParseIP("192.0.2.2")}}},
		RunID:    "run-1", Runner: "tester@runner", Now: func() time.Time { return time.Unix(0, 0) },
	})
	tgt, err := p.Resolve(context.Background(), target.Spec{Host: "app", Origin: "http://example.test:8080/"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	id := tgt.Identity()
	if got := id.Requested["origin"]; got != "http://example.test:8080/" {
		t.Errorf("requested origin = %v", got)
	}
	if got := id.Resolved["endpoint"]; got != "http://example.test:8080" {
		t.Errorf("resolved endpoint = %v", got)
	}
	if got := id.Facts["tls"]; got != "not-applicable" {
		t.Errorf("tls = %q", got)
	}

	e, ok := tgt.(target.Endpointer)
	if !ok {
		t.Fatal("endpoint target does not offer its endpoint")
	}
	u, ok := e.Endpoint()
	if !ok || u.String() != "http://example.test:8080" {
		t.Errorf("Endpoint = %q, %v", u.String(), ok)
	}
	if _, ok := tgt.(target.Sessioner); ok {
		t.Fatal("an endpoint-only target offered an execution session")
	}
}

func TestHTTPSRecordsTheCertificateWithoutRequiringPublicTrust(t *testing.T) {
	t.Parallel()

	p := endpointprovider.New(endpointprovider.Options{
		Resolver: resolver{addresses: []net.IPAddr{{IP: net.ParseIP("192.0.2.8")}}},
		ObserveTLS: func(context.Context, *url.URL) (endpointprovider.TLSIdentity, error) {
			return endpointprovider.TLSIdentity{
				LeafSHA256: "012345", Subject: "CN=appliance", Issuer: "CN=lab-ca", Serial: "7",
				NotBefore: time.Unix(1, 0), NotAfter: time.Unix(2, 0), Version: "TLS1.3",
				VerifyError: "certificate signed by unknown authority",
			}, nil
		},
	})
	tgt, err := p.Resolve(context.Background(), target.Spec{Host: "app", Origin: "https://example.test:8443"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	id := tgt.Identity()
	if id.Resolved["tls_leaf_sha256"] == "" {
		t.Error("no leaf certificate fingerprint was recorded")
	}
	if got := id.Facts["tls"]; got != "observed" {
		t.Errorf("tls = %q", got)
	}
	if got := id.Facts["tls.verified"]; got != "false" {
		t.Errorf("tls.verified = %q; httptest certificate should not be publicly trusted", got)
	}
	if got := id.Resolved["port"]; got != "8443" {
		t.Errorf("port = %v, want 8443", got)
	}
}

func TestEndpointOriginsRefuseAmbiguousOrSensitiveURLs(t *testing.T) {
	t.Parallel()

	p := endpointprovider.New(endpointprovider.Options{Resolver: resolver{addresses: []net.IPAddr{{IP: net.ParseIP("192.0.2.1")}}}})
	for _, origin := range []string{
		"", "example.test", "ftp://example.test", "https://user:secret@example.test",
		"https://example.test/app", "https://example.test?token=secret", "https://example.test/#fragment",
	} {
		_, err := p.Resolve(context.Background(), target.Spec{Host: "app", Origin: origin})
		if err == nil {
			t.Errorf("Resolve accepted %q", origin)
		} else if !strings.Contains(err.Error(), "app") {
			t.Errorf("error %q does not name the host", err)
		}
	}
}

func TestReleaseNeverRemovesAnExistingEndpoint(t *testing.T) {
	t.Parallel()

	if err := endpointprovider.New(endpointprovider.Options{}).Release(context.Background(), nil, false); err != nil {
		t.Errorf("Release: %v", err)
	}
}
