package scenario_test

import (
	"strings"
	"testing"
)

const validCarry = `
schema: 1
name: strict-carry
question: is a carry exactly one transfer operation?
hosts:
  source: {provider: docker, image: neutral-linux:1}
  destination: {provider: hyperv, image: neutral-other:1}
steps:
  - label: carry it
    host: source
    carry: {from: /out.bin, to_host: destination, to: /in.bin}
    expect: {rc: 0}
`

func TestCarryValidationRejectsMalformedAndUnusedFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		manifest string
		want     string
	}{
		{"probe too", strings.Replace(validCarry, "    carry:", "    probe: probes/install.sh\n    carry:", 1), "both probe and carry"},
		{"missing source", strings.Replace(validCarry, "from: /out.bin", "from: ''", 1), "from, to_host, and to"},
		{"unknown destination", strings.Replace(validCarry, "to_host: destination", "to_host: nowhere", 1), "nowhere"},
		{"args", strings.Replace(validCarry, "    expect:", "    args: [unused]\n    expect:", 1), "args"},
		{"environment", strings.Replace(validCarry, "    expect:", "    env: {UNUSED: value}\n    expect:", 1), "env"},
		{"media", strings.Replace(validCarry, "    expect:", "    media: [/tmp/a:/a]\n    expect:", 1), "media"},
		{"fingerprint", strings.Replace(validCarry, "    expect:", "    fingerprint: true\n    expect:", 1), "fingerprint"},
		{"control", strings.Replace(validCarry, "    expect: {rc: 0}", "    fingerprint: true\n    control: negative\n    expect: {rc: 0, fingerprint: unchanged}", 1), "control"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loadErr(t, test.manifest, test.want)
		})
	}
}
