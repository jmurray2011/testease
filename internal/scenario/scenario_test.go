package scenario_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/scenario"
)

// The manifest is a linear list, and the fence around it is the point: the day
// it needs a conditional, the decision is revisited rather than patched. A
// fence nothing enforces is a comment, so unknown fields are rejected.

const minimal = `
schema: 1
name: package-upgrade-leaves-it-running
question: what does an upgrade refusal leave behind?
hosts:
  box:
    provider: docker
    image: neutral-base:1
steps:
  - label: install the old version
    host: box
    probe: probes/install.sh
`

func write(t *testing.T, manifest string, files ...string) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write scenario.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatalf("mkdir probes: %v", err)
	}
	files = append(files, "probes/install.sh")
	for _, f := range files {
		path := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", f, err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	return dir
}

func loadOK(t *testing.T, manifest string, files ...string) *scenario.Manifest {
	t.Helper()

	m, err := scenario.Load(write(t, manifest, files...))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return m
}

func loadErr(t *testing.T, manifest string, want string, files ...string) {
	t.Helper()

	_, err := scenario.Load(write(t, manifest, files...))
	if err == nil {
		t.Fatalf("Load accepted a manifest it should have refused (wanted %q in the error)", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error is %q, want it to mention %q", err.Error(), want)
	}
}

func TestLoadAMinimalManifest(t *testing.T) {
	t.Parallel()

	m := loadOK(t, minimal)

	if m.Name != "package-upgrade-leaves-it-running" {
		t.Errorf("Name = %q", m.Name)
	}
	if len(m.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(m.Steps))
	}
	if m.Steps[0].Host != "box" {
		t.Errorf("step host = %q, want %q", m.Steps[0].Host, "box")
	}
}

func TestStepTemplateProvidesSharedDefaults(t *testing.T) {
	t.Parallel()

	manifest := `
schema: 1
name: templated-steps
hosts:
  box: {provider: docker, image: neutral-base:1}
step_templates:
  check:
    host: box
    timeout: 300s
    expect: {rc: 0}
steps:
  - use: check
    label: install
    probe: probes/install.sh
  - use: check
    label: verify
    probe: probes/verify.sh
`
	m := loadOK(t, manifest, "probes/verify.sh")
	if got := m.Steps[0].Timeout; time.Duration(got) != 300*time.Second {
		t.Fatalf("template timeout = %s", time.Duration(got))
	}
	if m.Steps[1].Expect == nil || m.Steps[1].Expect.RC == nil || *m.Steps[1].Expect.RC != 0 {
		t.Fatalf("template expectation = %#v", m.Steps[1].Expect)
	}
}

func TestHostTemplateProvidesSharedConnectionDefaults(t *testing.T) {
	t.Parallel()

	manifest := `
schema: 1
name: templated-hosts
host_templates:
  linux:
    provider: docker
    image: neutral-base:1
    env: {MODE: test}
hosts:
  first: {use: linux}
  second: {use: linux, image: neutral-base:2}
steps:
  - label: first
    host: first
    probe: probes/install.sh
  - label: second
    host: second
    probe: probes/install.sh
`
	m := loadOK(t, manifest)
	if m.Hosts["first"].Provider != "docker" || m.Hosts["first"].Image != "neutral-base:1" {
		t.Fatalf("first host = %#v", m.Hosts["first"])
	}
	if m.Hosts["second"].Image != "neutral-base:2" || m.Hosts["second"].Env["MODE"] != "test" {
		t.Fatalf("second host = %#v", m.Hosts["second"])
	}
}

func TestStepEnvOverTemplateWithoutEnv(t *testing.T) {
	t.Parallel()

	manifest := `
schema: 1
name: step-env-over-plain-template
hosts:
  box: {provider: docker, image: neutral-base:1}
step_templates:
  check:
    host: box
    timeout: 300s
steps:
  - use: check
    label: install
    probe: probes/install.sh
    env: {MODE: test}
`
	m := loadOK(t, manifest)
	if m.Steps[0].Env["MODE"] != "test" {
		t.Fatalf("step env = %#v", m.Steps[0].Env)
	}
}

func TestHostEnvOverTemplateWithoutEnv(t *testing.T) {
	t.Parallel()

	manifest := `
schema: 1
name: host-env-over-plain-template
host_templates:
  linux:
    provider: docker
    image: neutral-base:1
hosts:
  box: {use: linux, env: {MODE: test}}
steps:
  - label: check
    host: box
    probe: probes/install.sh
`
	m := loadOK(t, manifest)
	if m.Hosts["box"].Env["MODE"] != "test" {
		t.Fatalf("host env = %#v", m.Hosts["box"].Env)
	}
}

func TestHostTemplateMustExist(t *testing.T) {
	t.Parallel()
	loadErr(t, `
schema: 1
name: missing-host-template
hosts:
  box: {use: missing}
steps:
  - label: check
    host: box
    probe: probes/install.sh
`, "host template")
}

func TestStepTemplateMustExist(t *testing.T) {
	t.Parallel()
	loadErr(t, `
schema: 1
name: missing-template
hosts:
  box: {provider: docker, image: neutral-base:1}
steps:
  - use: missing
    label: check
    host: box
    probe: probes/install.sh
`, "step template")
}

func TestMatrixExclusionsArePartOfTheScenario(t *testing.T) {
	t.Parallel()
	m := loadOK(t, `
schema: 1
name: sparse-matrix
params:
  from: {axis: true, values: "old,new"}
  to: {axis: true, values: "next,latest"}
matrix:
  exclude:
    - {from: old, to: latest}
hosts:
  box: {provider: docker, image: neutral-base:1}
steps:
  - label: check
    host: box
    probe: probes/install.sh
`)
	if len(m.Matrix.Exclude) != 1 || m.Matrix.Exclude[0]["from"] != "old" {
		t.Fatalf("matrix = %#v", m.Matrix)
	}
}

func TestLoadACrossHostCarryStep(t *testing.T) {
	t.Parallel()

	manifest := `
schema: 1
name: cross-host-carry
question: do the exact bytes produced on one host arrive on another?
hosts:
  source: {provider: docker, image: neutral-linux:1}
  destination: {provider: hyperv, image: neutral-windows:1}
steps:
  - label: make the artifact
    host: source
    probe: probes/install.sh
  - label: carry the artifact between hosts
    host: source
    carry:
      from: /out/fixture.dump
      to_host: destination
      to: /fixture.dump
    timeout: 300s
    expect: {rc: 0}
  - label: read the artifact
    host: destination
    probe: probes/install.sh
`

	m := loadOK(t, manifest)
	carry := m.Steps[1].Carry
	if carry == nil {
		t.Fatal("carry step was not retained")
	}
	if carry.From != "/out/fixture.dump" || carry.ToHost != "destination" || carry.To != "/fixture.dump" {
		t.Errorf("carry = %+v", carry)
	}
}

func TestLoadHashesTheScenarioAndItsProbes(t *testing.T) {
	t.Parallel()

	// What exactly ran is what makes a two-year-old finding citable. A repo
	// commit is only the code that WOULD run from a clean checkout.
	m := loadOK(t, minimal)

	if !strings.HasPrefix(m.Hash, "sha256:") {
		t.Errorf("scenario hash = %q, want a sha256:", m.Hash)
	}
	probe, ok := m.Probes["probes/install.sh"]
	if !ok {
		t.Fatalf("probes = %v, want probes/install.sh among them", m.Probes)
	}
	if !strings.HasPrefix(probe, "sha256:") {
		t.Errorf("probe hash = %q, want a sha256:", probe)
	}

	// A different probe is a different scenario, whatever git says.
	other := write(t, minimal)
	if err := os.WriteFile(filepath.Join(other, "probes", "install.sh"), []byte("#!/bin/bash\nfalse\n"), 0o755); err != nil {
		t.Fatalf("rewrite probe: %v", err)
	}
	changed, err := scenario.Load(other)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if changed.Hash == m.Hash {
		t.Error("editing a probe did not change the scenario hash")
	}
}

func TestTheFenceHolds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		manifest string
		want     string
	}{
		{
			name: "no when: on a step",
			manifest: minimal + `    when: always
`,
			want: "when",
		},
		{
			name: "no retry: on a step",
			manifest: minimal + `    retry: 3
`,
			want: "retry",
		},
		{
			name:     "no unknown top-level field",
			manifest: minimal + "cleanup: true\n",
			want:     "cleanup",
		},
		{
			name:     "no unknown host field",
			manifest: strings.Replace(minimal, "    image: neutral-base:1", "    image: neutral-base:1\n    reboot: yes", 1),
			want:     "reboot",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			loadErr(t, c.manifest, c.want)
		})
	}
}

func TestValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		manifest string
		want     string
	}{
		{
			name:     "an unknown schema is refused, not guessed at",
			manifest: strings.Replace(minimal, "schema: 1", "schema: 99", 1),
			want:     "schema",
		},
		{
			name:     "a step must name a host that exists",
			manifest: strings.Replace(minimal, "host: box", "host: nowhere", 1),
			want:     "nowhere",
		},
		{
			name:     "a scenario needs at least one step",
			manifest: strings.Split(minimal, "steps:")[0],
			want:     "step",
		},
		{
			name:     "a control is negative or positive, nothing else",
			manifest: minimal + "    control: sideways\n",
			want:     "sideways",
		},
		{
			name:     "a required capability must be one testease has",
			manifest: strings.Replace(minimal, "schema: 1", "schema: 1\nrequires: [telepathy]", 1),
			want:     "telepathy",
		},
		{
			// Discovered after a 20-minute run: a judge that needs subjects and
			// was given none has nothing to look at, and finding that out when
			// the measuring is over wastes the measuring.
			name:     "a compare judge has to name what it compares",
			manifest: minimal + "judge: {name: compare, version: \"1\"}\n",
			want:     "params",
		},
		{
			// A control that takes no reading is a label. The word in the
			// manifest is what makes a reader believe the instrument was
			// checked, so the manifest has to say what the reading must be -
			// otherwise the step passes by saying nothing, which is the one
			// thing a control may never do.
			name:     "a control has to say what the fingerprint must show",
			manifest: minimal + "    control: negative\n",
			want:     "expect",
		},
		{
			name: "a control that declares an expectation still has to take the reading",
			manifest: minimal + `    control: negative
    expect: {fingerprint: unchanged}
`,
			want: "fingerprint: true",
		},
		{
			// The expect judge and the record refer to a step by its label, so
			// two steps sharing one would let the later verdict silently
			// overwrite the earlier - a run with an unmet expectation reading
			// as pass.
			name: "two steps may not share a label",
			manifest: minimal + `  - label: install the old version
    host: box
    probe: probes/install.sh
`,
			want: "label",
		},
		{
			name: "an expected fingerprint is changed or unchanged, nothing else",
			manifest: minimal + `    fingerprint: true
    expect: {fingerprint: sideways}
`,
			want: "sideways",
		},
		{
			// Not only controls: any step expecting a fingerprint result needs
			// the reading taken, or the expectation compares against nothing.
			name: "an expected fingerprint needs the reading taken",
			manifest: minimal + `    expect: {fingerprint: unchanged}
`,
			want: "fingerprint: true",
		},
		{
			name:     "the name has to survive being half of a run id",
			manifest: strings.Replace(minimal, "name: package-upgrade-leaves-it-running", "name: has spaces", 1),
			want:     "name",
		},
		{
			name:     "a probe path may not climb out of the scenario",
			manifest: strings.Replace(minimal, "probe: probes/install.sh", "probe: ../../etc/passwd", 1),
			want:     "..",
		},
		{
			name:     "a probe that is not there is named with its path",
			manifest: strings.Replace(minimal, "probe: probes/install.sh", "probe: probes/missing.sh", 1),
			want:     "probes/missing.sh",
		},
		{
			name: "an artifact must name a step that exists",
			manifest: minimal + `artifacts:
  - {step: no such step, from: /tmp/a.out, as: a.out, when: always}
`,
			want: "no such step",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			loadErr(t, c.manifest, c.want)
		})
	}
}

func TestTimeoutParsesAsADuration(t *testing.T) {
	t.Parallel()

	m := loadOK(t, minimal+"    timeout: 300s\n")
	if got := time.Duration(m.Steps[0].Timeout); got != 300*time.Second {
		t.Errorf("timeout = %v, want 300s", got)
	}

	loadErr(t, minimal+"    timeout: soon\n", "soon")
}

func TestAStepWithoutATimeoutLeavesTheBudgetToTheBackend(t *testing.T) {
	t.Parallel()

	// A caller pinning a tighter budget than the substrate needs turns "still
	// starting" into a false failure, so silence means the backend's own.
	if got := loadOK(t, minimal).Steps[0].Timeout; got != 0 {
		t.Errorf("timeout = %v, want the zero value", got)
	}
}

const parameterised = `
schema: 1
name: package-upgrade-leaves-it-running
question: what does an upgrade refusal leave behind?
params:
  from: {default: "1.0"}
  to: {default: "2.0"}
hosts:
  box:
    provider: docker
    image: neutral-base:1
    media: ["{{.from}}:/media:ro"]
steps:
  - label: install the old version
    host: box
    probe: probes/install.sh
    args: ["{{.from}}", "{{.to}}"]
    env:
      VERSION: "{{.to}}"
  - label: drive it
    host: box
    probe: drive://suite:{{.to}}
    media: ["{{.from}}:/suite:ro"]
`

func TestResolveExpandsTemplatesFromParams(t *testing.T) {
	t.Parallel()

	plan, err := loadOK(t, parameterised).Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := plan.Steps[0].Args; len(got) != 2 || got[0] != "1.0" || got[1] != "2.0" {
		t.Errorf("args = %v, want [1.0 2.0]", got)
	}
	if got := plan.Steps[0].Env["VERSION"]; got != "2.0" {
		t.Errorf("env VERSION = %q, want %q", got, "2.0")
	}
	if got := plan.Hosts["box"].Media; len(got) != 1 || got[0] != "1.0:/media:ro" {
		t.Errorf("media = %v, want [1.0:/media:ro]", got)
	}
	if got := plan.Params["from"]; got != "1.0" {
		t.Errorf("resolved param from = %q, want %q", got, "1.0")
	}

	// A drive step's image and its mounts are as parameterised as everything
	// else about the step. Left unexpanded they reach the container runtime as
	// literal braces, and the run fails there rather than here.
	if got := plan.Steps[1].Probe; got != "drive://suite:2.0" {
		t.Errorf("probe = %q, want the image expanded", got)
	}
	if got := plan.Steps[1].Media; len(got) != 1 || got[0] != "1.0:/suite:ro" {
		t.Errorf("step media = %v, want [1.0:/suite:ro]", got)
	}
}

func TestResolveTakesOverridesOverDefaults(t *testing.T) {
	t.Parallel()

	plan, err := loadOK(t, parameterised).Resolve(map[string]string{"from": "1.5"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := plan.Steps[0].Args[0]; got != "1.5" {
		t.Errorf("args[0] = %q, want %q", got, "1.5")
	}
}

func TestResolveRefusesAnOverrideForAParamThatDoesNotExist(t *testing.T) {
	t.Parallel()

	// Silently accepting one means a typo in a value quietly runs the default
	// and the record says the run was parameterised when it was not.
	_, err := loadOK(t, parameterised).Resolve(map[string]string{"form": "1.5"})
	if err == nil || !strings.Contains(err.Error(), "form") {
		t.Fatalf("err = %v, want it to name the unknown param", err)
	}
}

func TestResolveNamesAParamItCannotFill(t *testing.T) {
	t.Parallel()

	m := loadOK(t, strings.Replace(parameterised, `from: {default: "1.0"}`, "from: {}", 1))

	_, err := m.Resolve(nil)
	if err == nil {
		t.Fatal("Resolve invented a value for a param with no default")
	}
	if !strings.Contains(err.Error(), "from") {
		t.Errorf("error %q does not name the param", err)
	}
}

func TestResolveNamesAllParamsItCannotFillInStableOrder(t *testing.T) {
	t.Parallel()

	manifest := strings.Replace(parameterised, `from: {default: "1.0"}`, "from: {}", 1)
	manifest = strings.Replace(manifest, `to: {default: "2.0"}`, "to: {}", 1)

	_, err := loadOK(t, manifest).Resolve(nil)
	if err == nil {
		t.Fatal("Resolve invented values for params with no defaults")
	}
	want := `params "from", "to" have no default or value; supply --param from=<value> --param to=<value>`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestATemplateThatDoesNotResolveIsAPreconditionFailure(t *testing.T) {
	t.Parallel()

	m := loadOK(t, strings.Replace(parameterised, `"{{.from}}", "{{.to}}"`, `"{{.nope}}"`, 1))

	_, err := m.Resolve(nil)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want it to name the template key that did not resolve", err)
	}
}

func TestAMatrixAxisSaysWhatIsMissingAndHow(t *testing.T) {
	t.Parallel()

	// Every precondition failure names the command that fixes it. Matrix mode
	// is a later migration step, and a scenario that needs it must be told
	// that rather than quietly running one arbitrary cell.
	m := loadOK(t, strings.Replace(parameterised, `from: {default: "1.0"}`, `from: {values: "1.0,2.0", axis: true}`, 1))

	_, err := m.Resolve(nil)
	if err == nil {
		t.Fatal("Resolve accepted a matrix axis")
	}
	if !strings.Contains(err.Error(), "matrix") {
		t.Errorf("error %q does not say what is missing", err)
	}
}

const templatedHost = `
schema: 1
name: templated-host
question: does the host get its parameters?
params:
  provider: {default: "static"}
  image: {default: "neutral-base:2"}
  probe_arg: {default: "9"}
hosts:
  box:
    provider: "{{.provider}}"
    image: "{{.image}}"
    ready: ["check", "--for", "{{.probe_arg}}"]
steps:
  - label: do the thing
    host: box
    probe: probes/install.sh
`

func TestResolveExpandsTheHostToo(t *testing.T) {
	t.Parallel()

	// A host's image is as templated as a step's arguments. An unexpanded one
	// does not fail loudly - it reaches docker as a literal {{.image}} and the
	// run dies blaming the image, which is the wrong diagnosis.
	plan, err := loadOK(t, templatedHost).Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := plan.Hosts["box"].Image; got != "neutral-base:2" {
		t.Errorf("image = %q, want it expanded", got)
	}
	if got := plan.Hosts["box"].Provider; got != "static" {
		t.Errorf("provider = %q, want it expanded", got)
	}
	if got := plan.Hosts["box"].Ready; len(got) != 3 || got[2] != "9" {
		t.Errorf("ready = %v, want it expanded", got)
	}
}

func TestResolveDerivesTheHostImageFromASparseCrossing(t *testing.T) {
	t.Parallel()

	const crossing = `
schema: 1
name: windows-restore-matrix
question: does each forward restore crossing work?
params:
  crossing: {values: "5.0.2-to-6.0.3", axis: true}
hosts:
  box:
    provider: docker
    image: "vt-installed:{{crossingTarget .crossing}}"
steps:
  - label: restore the fixture
    host: box
    probe: probes/install.sh
    args: ["{{crossingSource .crossing}}", "{{crossingTarget .crossing}}"]
`
	plan, err := loadOK(t, crossing).Resolve(map[string]string{"crossing": "6.2.4-to-7.2.1-patched"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := plan.Hosts["box"].Image; got != "vt-installed:7.2.1-patched" {
		t.Errorf("image = %q, want target rung", got)
	}
	if got := plan.Steps[0].Args; len(got) != 2 || got[0] != "6.2.4" || got[1] != "7.2.1-patched" {
		t.Errorf("args = %v, want source and target rungs", got)
	}
}

func TestResolveRefusesAMalformedSparseCrossing(t *testing.T) {
	t.Parallel()

	const crossing = `
schema: 1
name: windows-restore-matrix
question: does each forward restore crossing work?
params:
  crossing: {default: "MALFORMED"}
hosts:
  box:
    provider: docker
    image: "vt-installed:{{crossingTarget .crossing}}"
steps:
  - label: restore the fixture
    host: box
    probe: probes/install.sh
`
	m := loadOK(t, crossing)
	for _, value := range []string{"MALFORMED", "-to-6.0.3", "5.0.2-to-", "5.0.2-to-6.0.3-to-7.2.1"} {
		_, err := m.Resolve(map[string]string{"crossing": value})
		if err == nil {
			t.Errorf("Resolve accepted malformed crossing %q", value)
			continue
		}
		if !strings.Contains(err.Error(), "<source>-to-<target>") {
			t.Errorf("error %q does not explain the crossing shape", err)
		}
	}
}

func TestEveryTextFieldOnAHostIsExpanded(t *testing.T) {
	t.Parallel()

	// Twice now a host field has shipped untemplated and failed by reaching a
	// backend as a literal {{.thing}} - which fails blaming the value rather
	// than the scenario that never got its parameter. This is the whole set.
	const everything = `
schema: 1
name: templated-everything
question: is every field expanded?
params:
  who: {default: "answer"}
hosts:
  box:
    provider: static
    backend: "{{.who}}"
    address: "{{.who}}"
    origin: "https://{{.who}}"
    image: "{{.who}}"
    media: ["{{.who}}:/m:ro"]
    ready: ["{{.who}}"]
steps:
  - label: do the thing
    host: box
    probe: probes/install.sh
`
	plan, err := loadOK(t, everything).Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	host := plan.Hosts["box"]
	for name, got := range map[string]string{
		"backend": host.Backend,
		"address": host.Address,
		"origin":  host.Origin,
		"image":   host.Image,
		"media":   host.Media[0],
		"ready":   host.Ready[0],
	} {
		if strings.Contains(got, "{{") {
			t.Errorf("host %s = %q, want it expanded", name, got)
		}
	}
}

func TestAnEndpointOriginCannotCarryRequestSecretsOrPaths(t *testing.T) {
	t.Parallel()

	const endpoint = `
schema: 1
name: endpoint
question: what answers?
params:
  origin: {default: "https://example.test"}
hosts:
  app:
    provider: endpoint
    origin: "{{.origin}}"
steps:
  - label: drive it
    host: app
    probe: drive://suite:1
`
	m := loadOK(t, endpoint)
	for _, origin := range []string{
		"example.test", "ftp://example.test", "https://user:secret@example.test",
		"https://example.test/app", "https://example.test?token=secret", "https://example.test/#fragment",
	} {
		if _, err := m.Resolve(map[string]string{"origin": origin}); err == nil {
			t.Errorf("Resolve accepted %q", origin)
		}
	}
}

// --- expect blocks ---------------------------------------------------------
//
// A step declares what should be true of itself. rc and fingerprint are about
// the step; a fact expectation is about a reading the step took, named as
// `fact.<key>` because the fact's own key has dots in it too.
//
// The fence still holds around it: an expect block takes those three shapes and
// nothing else. It has no operators, no negation, and no way to refer to
// another step. Any of those would make the manifest a rule language, which is
// the DSL the non-goals forbid; the day one is genuinely needed, that is a new
// judge, not a new key here.

const expecting = `
schema: 1
name: package-upgrade-leaves-it-running
question: what does an upgrade refusal leave behind?
params:
  from: {default: "1.0"}
hosts:
  box:
    provider: docker
    image: neutral-base:1
steps:
  - label: install the old version
    host: box
    probe: probes/install.sh
    fingerprint: true
    expect:
      rc: 0
      fingerprint: changed
      fact.svc.active: active
      fact.http.8080: 000
      fact.pkg.version: "{{.from}}"
`

func TestAStepCanExpectAFact(t *testing.T) {
	t.Parallel()

	m := loadOK(t, expecting)

	e := m.Steps[0].Expect
	if e == nil {
		t.Fatal("the step's expect block did not survive loading")
	}
	if e.RC == nil || *e.RC != 0 {
		t.Errorf("rc = %v, want 0", e.RC)
	}
	if e.Fingerprint != "changed" {
		t.Errorf("fingerprint = %q, want %q", e.Fingerprint, "changed")
	}
	if got := e.Facts["svc.active"]; got != "active" {
		t.Errorf("fact svc.active = %q, want %q", got, "active")
	}
}

func TestAFactExpectationIsTakenVerbatim(t *testing.T) {
	t.Parallel()

	// The judge compares facts as strings, and yaml would happily read 000 as
	// the number zero. A probe that writes down 000 for an http code did not
	// observe 0, and an expectation that quietly became "0" would disagree with
	// a correct reading.
	m := loadOK(t, expecting)

	if got := m.Steps[0].Expect.Facts["http.8080"]; got != "000" {
		t.Errorf("fact http.8080 = %q, want %q", got, "000")
	}
}

func TestAFactExpectationIsTemplated(t *testing.T) {
	t.Parallel()

	// The rollback check is "the package is still the one we installed", and
	// the version it was installed with is a parameter. Left unexpanded the
	// expectation compares a reading against the literal {{.from}} and calls a
	// correct rollback a regression.
	plan, err := loadOK(t, expecting).Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := plan.Steps[0].Expect.Facts["pkg.version"]; got != "1.0" {
		t.Errorf("fact pkg.version = %q, want %q", got, "1.0")
	}
}

func TestTheFenceHoldsInsideAnExpectBlock(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		expect string
		want   string
	}{
		{
			name:   "a field that is not rc, fingerprint or a fact",
			expect: "      phase-of-the-moon: waxing\n",
			want:   "phase-of-the-moon",
		},
		{
			name:   "a fact prefix naming no fact",
			expect: "      fact.: active\n",
			want:   "names no fact",
		},
		{
			name:   "a fact expectation that is not a single reading",
			expect: "      fact.svc.active: [active, running]\n",
			want:   "single value",
		},
		{
			name:   "the same expectation declared twice",
			expect: "      rc: 0\n      rc: 1\n",
			want:   "twice",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			loadErr(t, minimal+"    expect:\n"+c.expect, c.want)
		})
	}
}

const hostWithEnv = `
schema: 1
name: package-upgrade-leaves-it-running
question: what does an upgrade refusal leave behind?
params:
  secret: {default: "from-a-param"}
hosts:
  box:
    provider: docker
    image: neutral-base:1
    env:
      POSTGRES_PASSWORD: "{{.secret}}"
      PLAIN: literal
steps:
  - label: install the old version
    host: box
    probe: probes/install.sh
`

func TestAHostCanCarryTheEnvironmentItBootsWith(t *testing.T) {
	t.Parallel()

	// Some images refuse to start without one - postgres will not initialise
	// without a superuser password - so a scenario that cannot say so cannot
	// use them as a host at all.
	plan, err := loadOK(t, hostWithEnv).Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	env := plan.Hosts["box"].Env
	if got := env["PLAIN"]; got != "literal" {
		t.Errorf("PLAIN = %q", got)
	}
	if got := env["POSTGRES_PASSWORD"]; got != "from-a-param" {
		t.Errorf("POSTGRES_PASSWORD = %q, want it expanded", got)
	}
}

func TestAHostMayRaiseItsBootBudgetAndNeverLowerIt(t *testing.T) {
	t.Parallel()

	// The budget is the backend's: it measured the floor. An appliance booting
	// its whole unit set needs longer, and a scenario that cannot say so cannot
	// use one - but a step pinning a TIGHTER budget than the substrate's turns
	// "still starting" into a false no-boot, which is the whole reason the
	// floor exists.
	raised := strings.Replace(minimal, "    image: neutral-base:1", "    image: neutral-base:1\n    boot_timeout: 300s", 1)
	if got := loadOK(t, raised).Hosts["box"].BootTimeout; time.Duration(got) != 5*time.Minute {
		t.Errorf("boot_timeout = %v, want 5m", time.Duration(got))
	}

	lowered := strings.Replace(minimal, "    image: neutral-base:1", "    image: neutral-base:1\n    boot_timeout: 5s", 1)
	loadErr(t, lowered, "never lower")
}

func TestReproducibilityAndNamedViewsAreScenarioDeclarations(t *testing.T) {
	t.Parallel()

	body := strings.Replace(minimal, "hosts:", `inputs:
  suite: {kind: git-archive}
reproducibility:
  stable_facts: [evidence.sha256, target.version]
views:
  team:
    kind: facts
    facts: [evidence.sha256, target.version]
hosts:`, 1)
	body = strings.Replace(body, "    image: neutral-base:1", "    image: neutral-base:1\n    media: [\"@suite:/suite.tar:ro\"]", 1)
	manifest := loadOK(t, body)
	if manifest.Inputs["suite"].Kind != "git-archive" {
		t.Errorf("inputs = %+v", manifest.Inputs)
	}
	if manifest.Reproduce == nil || len(manifest.Reproduce.Facts) != 2 {
		t.Errorf("reproducibility = %+v", manifest.Reproduce)
	}
	if got := manifest.Views["team"]; got.Kind != "facts" || len(got.Facts) != 2 {
		t.Errorf("team view = %+v", got)
	}
}

func TestAMatrixCellMaySelectOneOfDeclaredNamedInputs(t *testing.T) {
	t.Parallel()

	body := strings.Replace(minimal, "hosts:", `inputs:
  fixture-5.0.2: {kind: file}
  fixture-6.0.3: {kind: file}
params:
  crossing: {values: "5.0.2-to-6.0.3", axis: true}
hosts:`, 1)
	body = strings.Replace(body, "    image: neutral-base:1", `    image: neutral-base:1
    media: ["@fixture-{{crossingSource .crossing}}:/fixture.dump:ro"]`, 1)
	manifest := loadOK(t, body)
	plan, err := manifest.Resolve(map[string]string{"crossing": "5.0.2-to-6.0.3"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := scenario.BindMountSources(plan, "@", map[string]string{
		"fixture-5.0.2": "/content/sha256-a",
		"fixture-6.0.3": "/content/sha256-b",
	}); err != nil {
		t.Fatalf("BindMountSources: %v", err)
	}
	if got := plan.Hosts["box"].Media[0]; got != "/content/sha256-a:/fixture.dump:ro" {
		t.Errorf("mount = %q, want the source cell's declared input", got)
	}
}

func TestATemplatedNamedInputStillNeedsADeclaredFiniteSet(t *testing.T) {
	t.Parallel()

	body := strings.Replace(minimal, "hosts:", `params:
  crossing: {default: "5.0.2-to-6.0.3"}
hosts:`, 1)
	body = strings.Replace(body, "    image: neutral-base:1", `    image: neutral-base:1
    media: ["@fixture-{{crossingSource .crossing}}:/fixture.dump:ro"]`, 1)
	loadErr(t, body, "inputs declares none")
}

func TestAReproducibilityDeclarationCannotPassByComparingNothing(t *testing.T) {
	t.Parallel()
	body := strings.Replace(minimal, "hosts:", "reproducibility: {}\nhosts:", 1)
	loadErr(t, body, "pass by looking at nothing")
}

func TestASecretFileHasOneDockerProducerAndReadOnlyDriveConsumers(t *testing.T) {
	t.Parallel()

	manifest := `
schema: 1
name: secret-handoff
question: can a generated credential reach a drive step without entering the record?
secret_files:
  actors: {}
hosts:
  app:
    provider: docker
    image: target:1
    media: ["%actors:/run/testease/actors.env"]
steps:
  - label: drive the app
    host: app
    probe: drive://suite:1
    media: ["%actors:/inputs/actors.env:ro"]
`
	if got := loadOK(t, manifest).Secrets; len(got) != 1 {
		t.Errorf("secret files = %+v", got)
	}

	writableConsumer := strings.Replace(manifest, ":/inputs/actors.env:ro", ":/inputs/actors.env", 1)
	loadErr(t, writableConsumer, "drive consumers must mount it read-only")

	noProducer := strings.Replace(manifest, "%actors:/run/testease/actors.env", "%actors:/run/testease/actors.env:ro", 1)
	loadErr(t, noProducer, "target producer must be able to write")
}
