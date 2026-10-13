package main

import (
	"slices"
	"testing"
)

func TestStaticHostsAdvertiseTheMediaTheyCanStage(t *testing.T) {
	t.Parallel()

	for _, kind := range backendKinds() {
		if kind.Provider != "static" {
			continue
		}
		if !slices.Contains(kind.Capabilities, "media") {
			t.Fatal("static backend does not advertise media, but its SSH, SSM and WinRM sessions stage files")
		}
		return
	}
	t.Fatal("static backend is absent from the advertised backend contract")
}

func TestDoctorAcceptsTheInstrumentThatMakesDockerFingerprintable(t *testing.T) {
	t.Parallel()

	cmd := doctorCmd()
	if err := cmd.Flags().Set("instrument-collect", "/instrument collect"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("instrument-diff", "/instrument diff"); err != nil {
		t.Fatal(err)
	}

	for _, kind := range capabilityTableFor(cmd) {
		if kind.Provider != "docker" {
			continue
		}
		if !slices.Contains(kind.Offers, "fingerprint") {
			t.Fatal("doctor did not count its supplied instrument as Docker's fingerprint capability")
		}
		return
	}
	t.Fatal("docker backend is absent from doctor's capability table")
}

func TestDoctorAcceptsTheInstrumentThatMakesStaticHostsFingerprintable(t *testing.T) {
	t.Parallel()

	cmd := doctorCmd()
	if err := cmd.Flags().Set("instrument-collect", "/instrument collect"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("instrument-diff", "/instrument diff"); err != nil {
		t.Fatal(err)
	}

	for _, kind := range capabilityTableFor(cmd) {
		if kind.Provider != "static" {
			continue
		}
		if !slices.Contains(kind.Offers, "fingerprint") {
			t.Fatal("doctor did not count its supplied instrument as the static host's fingerprint capability")
		}
		return
	}
	t.Fatal("static backend is absent from doctor's capability table")
}
