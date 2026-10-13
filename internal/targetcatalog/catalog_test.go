package targetcatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCatalog(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "targets.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveUsesNameAndAllTags(t *testing.T) {
	t.Parallel()
	catalog, err := Load(writeCatalog(t, `
schema: 1
targets:
  - name: lab-a
    tags: [os:rhel9, posture:fips-stig, capacity:exclusive]
    params: {backend: ssh, address: 192.0.2.10}
  - name: ordinary-a
    tags: [os:rhel9]
    params: {backend: ssh, address: 192.0.2.11}
`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := catalog.Resolve(Selector{Tags: []string{"posture:fips-stig", "os:rhel9"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile.Name != "lab-a" || got.Profile.Params["address"] != "192.0.2.10" {
		t.Fatalf("selection = %#v", got)
	}
	if len(got.CatalogSHA256) != 64 {
		t.Fatalf("catalog hash = %q", got.CatalogSHA256)
	}
}

func TestResolveNeverSilentlyChoosesOneOfMany(t *testing.T) {
	t.Parallel()
	catalog, err := Load(writeCatalog(t, `
schema: 1
targets:
  - name: a
    tags: [os:rhel9]
    params: {address: 192.0.2.10}
  - name: b
    tags: [os:rhel9]
    params: {address: 192.0.2.11}
`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.Resolve(Selector{Tags: []string{"os:rhel9"}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "a, b") {
		t.Fatalf("error = %v, want an ambiguity naming both matches", err)
	}
}

func TestCatalogIsStrictAndNamesAreUnique(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		"schema: 1\ntargets:\n  - name: a\n    surprise: true\n    params: {address: x}\n",
		"schema: 1\ntargets:\n  - name: a\n    params: {address: x}\n  - name: a\n    params: {address: y}\n",
	} {
		if _, err := Load(writeCatalog(t, body)); err == nil {
			t.Fatalf("Load accepted:\n%s", body)
		}
	}
}
