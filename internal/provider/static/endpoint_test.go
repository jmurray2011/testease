package static

import (
	"net/url"
	"testing"

	"github.com/jmurray2011/testease/internal/target"
)

func TestStaticMachineKeepsExecutionAndNetworkAddressesSeparate(t *testing.T) {
	origin, err := url.Parse("https://docs.example.test:8443")
	if err != nil {
		t.Fatal(err)
	}
	m := &machine{
		spec:     target.Spec{Backend: "ssm", Address: "i-0123456789abcdef0"},
		endpoint: origin,
	}

	got, ok := m.Endpoint()
	if !ok {
		t.Fatal("machine with a declared network origin offered no endpoint")
	}
	if got.String() != origin.String() {
		t.Fatalf("endpoint = %q, want %q", got.String(), origin.String())
	}
	if m.spec.Address == got.Host {
		t.Fatal("execution address was confused with the network endpoint")
	}
}

func TestStaticMachineWithoutOriginOffersNoEndpoint(t *testing.T) {
	m := &machine{spec: target.Spec{Backend: "ssm", Address: "i-0123456789abcdef0"}}
	if _, ok := m.Endpoint(); ok {
		t.Fatal("machine without a declared network origin offered an endpoint")
	}
}
