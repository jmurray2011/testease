// Package scenario reads the question a run is asking: a manifest naming
// steps, the hosts they run on, and what is expected of them.
//
// The manifest is deliberately a linear list. Sequencing, capture, and
// recording belong to the runner; control flow belongs inside a probe, or the
// scenario splits in two. The fence around the field set is enforced here
// rather than described in a comment, because the day the yaml needs a
// conditional is a decision to revisit, not a field to add.
//
// Nothing in this package does I/O beyond reading the scenario directory, and
// it imports nothing else internal.
package scenario

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Schema is the manifest schema this package understands. A manifest carrying
// another is refused rather than guessed at.
const Schema = 1

// MinBootTimeout is the floor a host may not go under. The backends measured
// their own; this is the lowest of them, enforced where the manifest is read so
// a scenario cannot quietly tighten one.
const MinBootTimeout = 180 * time.Second

// File is the manifest's name inside a scenario directory.
const File = "scenario.yaml"

// nameShape is what a scenario name must satisfy: it becomes the first half of
// every run id this scenario produces, and a run id is cited, sorted, and
// grepped as an opaque key.
var nameShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Capability is something a scenario needs of its target. The runner refuses
// early when a target cannot offer one, and prints what would fix it.
type Capability string

// The capabilities a scenario may require. This is a list, not a registry.
const (
	NeedsSession     Capability = "session"
	NeedsOneShot     Capability = "oneshot"
	NeedsEndpoint    Capability = "endpoint"
	NeedsFingerprint Capability = "fingerprint"
	NeedsMedia       Capability = "media"
	NeedsCarry       Capability = "carry"
)

var capabilities = map[Capability]bool{
	NeedsSession:     true,
	NeedsOneShot:     true,
	NeedsEndpoint:    true,
	NeedsFingerprint: true,
	NeedsMedia:       true,
	NeedsCarry:       true,
}

// Control marks a step whose purpose is to check the instruments rather than
// the product.
type Control string

// The controls. A negative control does nothing and must show nothing; a
// positive control does something known and must show it.
const (
	ControlNegative Control = "negative"
	ControlPositive Control = "positive"
)

// Manifest is a scenario as written, before its templates are resolved.
type Manifest struct {
	Schema        int                   `yaml:"schema"`
	Name          string                `yaml:"name"`
	Question      string                `yaml:"question"`
	Requires      []Capability          `yaml:"requires"`
	Params        map[string]Param      `yaml:"params"`
	Inputs        map[string]Input      `yaml:"inputs"`
	Secrets       map[string]SecretFile `yaml:"secret_files"`
	Hosts         map[string]Host       `yaml:"hosts"`
	HostTemplates map[string]Host       `yaml:"host_templates"`
	StepTemplates map[string]Step       `yaml:"step_templates"`
	Matrix        Matrix                `yaml:"matrix"`
	Steps         []Step                `yaml:"steps"`
	Judge         *JudgeRef             `yaml:"judge"`
	Reproduce     *Reproduce            `yaml:"reproducibility"`
	Views         map[string]View       `yaml:"views"`
	Artifacts     []Artifact            `yaml:"artifacts"`

	// Dir is where the scenario was read from. Hash and Probes identify the
	// runtime inputs; TreeHash and Files independently pin the complete tree.
	Dir             string `yaml:"-"`
	ContentIdentity `yaml:"-"`
}

// Input declares external, non-secret bytes a scenario needs. The invocation
// binds the logical name to a content-addressed entry in an input bundle; the
// scenario never hard-codes the workstation path where those bytes happen to
// live.
type Input struct {
	Kind string `yaml:"kind"`
}

// SecretFile is an empty declaration on purpose. Testease owns its mode and
// lifetime; the scenario only gives the file a logical name and mount points.
type SecretFile struct{}

// Reproduce declares readings expected to remain identical across independent
// runs. It is recorded with each run so the comparison never needs the old
// scenario checkout.
type Reproduce struct {
	Facts []string `yaml:"stable_facts"`
}

// View is a named, judgment-free projection suitable for a runbook or demo.
// It selects readings but never assigns meaning to their values.
type View struct {
	Kind  string   `yaml:"kind"`
	Facts []string `yaml:"facts"`
}

// Param is one value a scenario is parameterised by.
type Param struct {
	Values  string `yaml:"values"`
	Axis    bool   `yaml:"axis"`
	Default string `yaml:"default"`
}

// Matrix describes combinations that should not be scheduled from the
// Cartesian product of axis values.
type Matrix struct {
	Exclude []map[string]string `yaml:"exclude"`
}

// Host is one machine a scenario's steps run on.
type Host struct {
	Use string `yaml:"use"`

	// Provider may be parameterized when the same evidence question runs on a
	// disposable container and on a machine-shaped substrate.
	Provider string `yaml:"provider"`

	// Origin is the complete http(s) origin of the target's network surface.
	// It may accompany a separate execution address: an SSM machine is dialled
	// by instance id while a drive step observes its application through here.
	Origin string `yaml:"origin"`

	// Backend is how a machine that already exists is dialled: ssm, winrm.
	// A provider that creates its own targets knows its backend already and
	// leaves this empty.
	Backend string `yaml:"backend"`

	// Address is where that machine is - a hostname, or an instance id for
	// something reached through a service rather than over the network.
	Address string `yaml:"address"`

	Image string   `yaml:"image"`
	Media []string `yaml:"media"`

	// Env is what this host boots with. Some images refuse to start without
	// one, so a scenario that cannot say so cannot use them as a host at all.
	// It is how the machine is made, not what a step does with it.
	Env map[string]string `yaml:"env"`

	// Port is the port the product answers on. A host that declares one has a
	// network endpoint, which is a different capability from an execution
	// surface: a browser-driving step uses the endpoint and never the surface,
	// and a probe uses the surface and never the endpoint.
	Port int `yaml:"port"`

	// Scheme is how that port is spoken to, http or https. It is declared
	// rather than inferred from the number: this lab serves TLS on 8443 as
	// readily as on 443, so guessing from the port is wrong on the box most
	// likely to be measured.
	Scheme string `yaml:"scheme"`

	// BootTimeout raises how long this host gets to become ready. The budget
	// belongs to the backend, which measured its own floor; a host that boots a
	// whole unit set needs longer and says so here. It may only be raised: a
	// caller pinning a tighter budget than the substrate's turns "still
	// starting" into a false no-boot.
	BootTimeout Duration `yaml:"boot_timeout"`

	// Ready is how this host says it has finished starting. Empty means the
	// backend's own default, which is the weakest honest claim it can make -
	// that the execution surface answers at all. A host running an init
	// system says so here, because what counts as started is the scenario's
	// knowledge; how long to wait for it is the backend's.
	Ready []string `yaml:"ready"`
}

// Step is one thing a scenario does, in order.
type Step struct {
	Use   string            `yaml:"use"`
	Label string            `yaml:"label"`
	Host  string            `yaml:"host"`
	Probe string            `yaml:"probe"`
	Carry *Carry            `yaml:"carry"`
	Args  []string          `yaml:"args"`
	Env   map[string]string `yaml:"env"`

	// Media is source:dest[:ro] mounts for a DRIVE step's container. A suite
	// is usually a checkout of its own - too large and too separate to sit in
	// the scenario directory - and baking one into an image would turn its
	// version into a tag nobody can audit. Mounting it by path keeps the path
	// in the record.
	Media       []string `yaml:"media"`
	Timeout     Duration `yaml:"timeout"`
	Fingerprint bool     `yaml:"fingerprint"`
	Expect      *Expect  `yaml:"expect"`
	Control     Control  `yaml:"control"`
}

// Carry moves the exact bytes produced on one host to another host between
// two ordinary steps. Host on the step is the source; ToHost names the
// destination. The backend owns how the destination accepts the bytes, which
// matters for a Hyper-V container that can only be seeded before it starts.
type Carry struct {
	From   string `yaml:"from"`
	ToHost string `yaml:"to_host"`
	To     string `yaml:"to"`
}

// Expect is what a step declares should be true of itself. The runner checks
// it for fail-fast visibility; the expect judge re-derives the same conclusion
// from the events, and the judge is the authority.
//
// Facts are keyed by the fact's own key, written in the manifest as
// `fact.svc.active`. Three shapes and no more: an expect block has no
// operators, no negation, and no way to name another step. Each of those would
// turn the manifest into a rule language a step at a time, and the one that is
// genuinely wanted - "the service was active before and is not now" - is a
// question about a run rather than about a step, so it belongs to a judge that
// says it is asking it.
type Expect struct {
	RC          *int
	Fingerprint string
	Facts       map[string]string
}

// factPrefix marks an expectation about a reading rather than about the step.
// The judge spells the same prefix and deliberately not by importing it from
// here: the arrow Observation -> Record -> Judge -> Verdict is a build
// constraint, and scenario imports nothing internal.
const factPrefix = "fact."

// UnmarshalYAML reads an expect block.
//
// It is walked by hand rather than decoded into the struct because a fact
// expectation names its fact in the key, and because the value has to survive
// as written: yaml reads 000 as the number zero, and a probe that wrote down
// 000 for an http code did not observe 0.
func (e *Expect) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("expect must be a mapping such as {rc: 0}")
	}

	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if seen[key.Value] {
			// The second one silently winning would leave a declared check
			// never run, with the manifest saying it was.
			return fmt.Errorf("expect declares %q twice", key.Value)
		}
		seen[key.Value] = true

		switch {
		case key.Value == "rc":
			var rc int
			if err := value.Decode(&rc); err != nil {
				return fmt.Errorf("expect rc must be an exit code: %w", err)
			}
			e.RC = &rc

		case key.Value == "fingerprint":
			if err := value.Decode(&e.Fingerprint); err != nil {
				return fmt.Errorf("expect fingerprint must be a result such as unchanged: %w", err)
			}

		case strings.HasPrefix(key.Value, factPrefix):
			name := strings.TrimPrefix(key.Value, factPrefix)
			if name == "" {
				return fmt.Errorf("expect %q names no fact: the key is %s<key>, as in %ssvc.active", key.Value, factPrefix, factPrefix)
			}
			if value.Kind != yaml.ScalarNode {
				return fmt.Errorf("expect %q must be a single value: a fact is one reading", key.Value)
			}
			if e.Facts == nil {
				e.Facts = map[string]string{}
			}
			e.Facts[name] = value.Value

		default:
			return fmt.Errorf("expect %q is not something a step can declare: a step expects rc, fingerprint, or %s<key>", key.Value, factPrefix)
		}
	}
	return nil
}

// JudgeRef names the judge a scenario's records are meant to be read by.
type JudgeRef struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`

	// Params is what a judge needs that the evidence cannot supply: which
	// facts a comparison is about, and which way. It lives here rather than on
	// the steps because the question is about the run, and it is recorded, so
	// re-judging an old record needs the record and nothing else.
	Params map[string][]string `yaml:"params"`
}

// Artifact is a file worth keeping out of a step.
type Artifact struct {
	Step string `yaml:"step"`
	From string `yaml:"from"`
	As   string `yaml:"as"`
	When string `yaml:"when"`
}

// Duration is a yaml duration written the way a person writes one: 300s, 10m.
type Duration time.Duration

// UnmarshalYAML parses a duration, naming what it could not read.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var text string
	if err := node.Decode(&text); err != nil {
		return fmt.Errorf("timeout must be a duration such as 300s: %w", err)
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("timeout %q is not a duration such as 300s", text)
	}
	*d = Duration(parsed)
	return nil
}

// Load reads and validates the scenario in dir.
func Load(dir string) (*Manifest, error) {
	f, err := os.Open(filepath.Join(dir, File))
	if err != nil {
		return nil, fmt.Errorf("read scenario: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec := yaml.NewDecoder(f)
	// The fence, enforced. An unknown field is a scenario asking for
	// behaviour testease decided not to have.
	dec.KnownFields(true)

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, File), err)
	}
	m.Dir = dir
	if err := m.applyHostTemplates(); err != nil {
		return nil, err
	}
	if err := m.applyStepTemplates(); err != nil {
		return nil, err
	}

	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := m.hash(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) applyHostTemplates() error {
	for name, template := range m.HostTemplates {
		if strings.TrimSpace(name) == "" || !nameShape.MatchString(name) {
			return fmt.Errorf("host template name %q must match %s", name, nameShape)
		}
		if template.Use != "" {
			return fmt.Errorf("host template %q cannot use another host template", name)
		}
	}
	for name, host := range m.Hosts {
		if host.Use == "" {
			continue
		}
		template, ok := m.HostTemplates[host.Use]
		if !ok {
			return fmt.Errorf("host %q uses unknown host template %q", name, host.Use)
		}
		m.Hosts[name] = mergeHostTemplate(template, host)
	}
	return nil
}

func mergeHostTemplate(base, override Host) Host {
	merged := base
	if override.Provider != "" {
		merged.Provider = override.Provider
	}
	if override.Origin != "" {
		merged.Origin = override.Origin
	}
	if override.Backend != "" {
		merged.Backend = override.Backend
	}
	if override.Address != "" {
		merged.Address = override.Address
	}
	if override.Image != "" {
		merged.Image = override.Image
	}
	if override.Media != nil {
		merged.Media = override.Media
	}
	if override.Env != nil {
		merged.Env = maps.Clone(merged.Env)
		for key, value := range override.Env {
			merged.Env[key] = value
		}
	}
	if override.Port != 0 {
		merged.Port = override.Port
	}
	if override.Scheme != "" {
		merged.Scheme = override.Scheme
	}
	if override.BootTimeout != 0 {
		merged.BootTimeout = override.BootTimeout
	}
	if override.Ready != nil {
		merged.Ready = override.Ready
	}
	merged.Use = ""
	return merged
}

// applyStepTemplates expands the small reuse mechanism in the manifest before
// validation and planning. A template is just a partial step; it cannot refer
// to another template, which keeps the merge order obvious and avoids turning
// YAML into a hidden inheritance language.
func (m *Manifest) applyStepTemplates() error {
	for name, template := range m.StepTemplates {
		if strings.TrimSpace(name) == "" || !nameShape.MatchString(name) {
			return fmt.Errorf("step template name %q must match %s", name, nameShape)
		}
		if template.Use != "" {
			return fmt.Errorf("step template %q cannot use another step template", name)
		}
	}
	for i, step := range m.Steps {
		if step.Use == "" {
			continue
		}
		template, ok := m.StepTemplates[step.Use]
		if !ok {
			return fmt.Errorf("step %d uses unknown step template %q", i+1, step.Use)
		}
		m.Steps[i] = mergeStepTemplate(template, step)
	}
	return nil
}

func mergeStepTemplate(base, override Step) Step {
	merged := base
	if override.Label != "" {
		merged.Label = override.Label
	}
	if override.Host != "" {
		merged.Host = override.Host
	}
	if override.Probe != "" {
		merged.Probe = override.Probe
	}
	if override.Carry != nil {
		merged.Carry = override.Carry
	}
	if override.Args != nil {
		merged.Args = override.Args
	}
	if override.Env != nil {
		merged.Env = maps.Clone(merged.Env)
		for key, value := range override.Env {
			merged.Env[key] = value
		}
	}
	if override.Media != nil {
		merged.Media = override.Media
	}
	if override.Timeout != 0 {
		merged.Timeout = override.Timeout
	}
	if override.Fingerprint {
		merged.Fingerprint = true
	}
	if override.Expect != nil {
		merged.Expect = override.Expect
	}
	if override.Control != "" {
		merged.Control = override.Control
	}
	merged.Use = ""
	return merged
}

func (m *Manifest) validate() error {
	if m.Schema != Schema {
		return fmt.Errorf("scenario schema %d is not one this testease knows (%d)", m.Schema, Schema)
	}
	if !nameShape.MatchString(m.Name) {
		return fmt.Errorf("scenario name %q must match %s: it becomes half of every run id this scenario writes", m.Name, nameShape)
	}
	for _, c := range m.Requires {
		if !capabilities[c] {
			return fmt.Errorf("scenario requires capability %q, which testease has no such thing as", c)
		}
	}
	for name, input := range m.Inputs {
		if !nameShape.MatchString(name) {
			return fmt.Errorf("input name %q must match %s", name, nameShape)
		}
		switch input.Kind {
		case "file", "git-archive":
		default:
			return fmt.Errorf("input %q has kind %q; there is file and git-archive", name, input.Kind)
		}
	}
	for i, exclusion := range m.Matrix.Exclude {
		if len(exclusion) == 0 {
			return fmt.Errorf("matrix exclusion %d is empty", i+1)
		}
		for name, value := range exclusion {
			param, ok := m.Params[name]
			if !ok || !param.Axis {
				return fmt.Errorf("matrix exclusion %d names %q, which is not a declared matrix axis", i+1, name)
			}
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("matrix exclusion %d has an empty value for %q", i+1, name)
			}
		}
	}
	for name := range m.Secrets {
		if !nameShape.MatchString(name) {
			return fmt.Errorf("secret file name %q must match %s", name, nameShape)
		}
	}
	if err := m.validateNamedMounts(); err != nil {
		return err
	}
	if m.Reproduce != nil {
		if err := uniqueNames("reproducibility stable fact", m.Reproduce.Facts, true); err != nil {
			return err
		}
	}
	for name, view := range m.Views {
		if !nameShape.MatchString(name) {
			return fmt.Errorf("view name %q must match %s", name, nameShape)
		}
		switch view.Kind {
		case "facts", "rows":
		default:
			return fmt.Errorf("view %q has kind %q; named views may project facts or rows", name, view.Kind)
		}
		if err := uniqueNames("view "+name+" fact", view.Facts, true); err != nil {
			return err
		}
	}
	if len(m.Steps) == 0 {
		return errors.New("scenario has no steps: a question nothing measures is not a scenario")
	}

	for name, h := range m.Hosts {
		if h.BootTimeout != 0 && time.Duration(h.BootTimeout) < MinBootTimeout {
			return fmt.Errorf("host %q sets boot_timeout %s, below the backend's own floor of %s: a budget may be raised and never lowered, because a tighter one turns a slow boot into a false no-boot",
				name, time.Duration(h.BootTimeout), MinBootTimeout)
		}
	}

	labels := make(map[string]bool, len(m.Steps))
	for i, s := range m.Steps {
		where := fmt.Sprintf("step %d (%q)", i+1, s.Label)
		if s.Label == "" {
			return fmt.Errorf("step %d has no label: the label is what a record and a verdict refer to it by", i+1)
		}
		if labels[s.Label] {
			// The expect judge keys per-step verdicts by label, so a shared one
			// lets a later step's verdict silently overwrite an earlier step's.
			return fmt.Errorf("%s reuses a label another step already has: a record and a verdict refer to steps by label, and two steps sharing one cannot be told apart", where)
		}
		labels[s.Label] = true
		if _, ok := m.Hosts[s.Host]; !ok {
			return fmt.Errorf("%s runs on host %q, which the scenario does not declare", where, s.Host)
		}
		switch s.Control {
		case "":
		case ControlNegative, ControlPositive:
			// A control is the one step whose whole purpose is to find out
			// whether the instrument can answer. Left undeclared it takes no
			// reading and declares nothing, so it passes by being silent - and
			// the word CONTROL in the record tells every later reader that the
			// drift column was checked when nothing checked it. Refused at
			// load, because a run that has already happened cannot be told
			// apart from one where the control genuinely held.
			if s.Expect == nil || s.Expect.Fingerprint == "" {
				return fmt.Errorf("%s is a %s control and declares no expect.fingerprint: a control that says nothing cannot fail, and its label claims the instrument was checked",
					where, s.Control)
			}
		default:
			return fmt.Errorf("%s has control %q; a control is %q or %q", where, s.Control, ControlNegative, ControlPositive)
		}
		if s.Expect != nil && s.Expect.Fingerprint != "" {
			// Spelled as literals: the values are the record's, and scenario
			// imports nothing internal.
			if s.Expect.Fingerprint != "changed" && s.Expect.Fingerprint != "unchanged" {
				return fmt.Errorf("%s expects fingerprint %q; an instrument reading a step can expect is %q or %q",
					where, s.Expect.Fingerprint, "changed", "unchanged")
			}
			if !s.Fingerprint {
				return fmt.Errorf("%s expects fingerprint %q and does not set fingerprint: true, so no reading is taken to compare it against",
					where, s.Expect.Fingerprint)
			}
		}
		if s.Carry != nil {
			if s.Probe != "" {
				return fmt.Errorf("%s names both probe and carry: one step performs one operation", where)
			}
			switch {
			case len(s.Args) > 0:
				return fmt.Errorf("%s is a carry and also declares args, which no transfer consumes", where)
			case len(s.Env) > 0:
				return fmt.Errorf("%s is a carry and also declares env, which no transfer consumes", where)
			case len(s.Media) > 0:
				return fmt.Errorf("%s is a carry and also declares media, which belongs to a drive operation", where)
			case s.Control != "":
				return fmt.Errorf("%s is a carry and also declares control; controls exercise probe instruments", where)
			case s.Fingerprint:
				return fmt.Errorf("%s is a carry and also requests fingerprinting; fingerprint applies to a probe operation", where)
			}
			if s.Carry.From == "" || s.Carry.ToHost == "" || s.Carry.To == "" {
				return fmt.Errorf("%s carry needs from, to_host, and to", where)
			}
			if _, ok := m.Hosts[s.Carry.ToHost]; !ok {
				return fmt.Errorf("%s carries to host %q, which the scenario does not declare", where, s.Carry.ToHost)
			}
		} else if err := validProbe(s.Probe); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if s.Carry != nil {
			continue
		}
		if IsDrive(s.Probe) {
			// A drive step names an image, not a file in the scenario. There
			// is nothing on disk to check here; whether the image exists is
			// the runtime's answer at run time.
			continue
		}
		if _, err := os.Stat(filepath.Join(m.Dir, filepath.FromSlash(s.Probe))); err != nil {
			return fmt.Errorf("%s needs probe %s, which is not in the scenario directory", where, s.Probe)
		}
	}

	// A judge that needs subjects and was given none looks at nothing and would
	// say so only after the measuring is over. Which judges need what is not
	// this package's to know - it imports nothing internal - so the rule is the
	// narrow one: compare's whole question is which facts, and a manifest that
	// names it without them has not asked anything.
	if m.Judge != nil && m.Judge.Name == "compare" && len(m.Judge.Params) == 0 {
		return errors.New("judge compare names no params: it compares the facts the scenario names, and a comparison over nothing passes by looking at nothing")
	}

	for _, a := range m.Artifacts {
		if !labels[a.Step] {
			return fmt.Errorf("artifact %q is taken from step %q, which the scenario does not have", a.As, a.Step)
		}
	}
	return nil
}

func uniqueNames(what string, values []string, required bool) error {
	if required && len(values) == 0 {
		return fmt.Errorf("%s list is empty: a declaration that compares nothing would pass by looking at nothing", what)
	}
	seen := map[string]bool{}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s name is empty", what)
		}
		if seen[value] {
			return fmt.Errorf("%s %q is declared twice", what, value)
		}
		seen[value] = true
	}
	return nil
}

func (m *Manifest) validateNamedMounts() error {
	usedInputs := map[string]bool{}
	dynamicInputs := false
	writers := map[string]int{}
	readers := map[string]int{}

	check := func(item, where, provider string, drive bool) error {
		source, _, readOnly := ParseMount(item)
		switch {
		case strings.HasPrefix(source, "@"):
			name := strings.TrimPrefix(source, "@")
			if strings.Contains(name, "{{") {
				// Resolution will turn this into one exact logical name per
				// matrix cell, and inputbundle.Bind refuses it unless that name
				// is in the finite declared set. Mark the set dynamic here: no
				// individual declaration can be called unused before the axis
				// has selected its cell.
				if len(m.Inputs) == 0 {
					return fmt.Errorf("%s mounts a templated input %q, but inputs declares none", where, name)
				}
				dynamicInputs = true
				return nil
			}
			if _, ok := m.Inputs[name]; !ok {
				return fmt.Errorf("%s mounts input %q, which inputs does not declare", where, name)
			}
			usedInputs[name] = true

		case strings.HasPrefix(source, "%"):
			name := strings.TrimPrefix(source, "%")
			if _, ok := m.Secrets[name]; !ok {
				return fmt.Errorf("%s mounts secret file %q, which secret_files does not declare", where, name)
			}
			if drive {
				if !readOnly {
					return fmt.Errorf("%s mounts secret file %q read-write; drive consumers must mount it read-only", where, name)
				}
				readers[name]++
				return nil
			}
			if provider != "docker" {
				return fmt.Errorf("%s mounts secret file %q, but generated secret handoff initially requires a docker host", where, name)
			}
			if readOnly {
				return fmt.Errorf("%s mounts secret file %q read-only; its target producer must be able to write it", where, name)
			}
			writers[name]++
		}
		return nil
	}

	for name, host := range m.Hosts {
		for _, item := range host.Media {
			if err := check(item, fmt.Sprintf("host %q", name), host.Provider, false); err != nil {
				return err
			}
		}
	}
	for _, step := range m.Steps {
		for _, item := range step.Media {
			if err := check(item, fmt.Sprintf("step %q", step.Label), "", IsDrive(step.Probe)); err != nil {
				return err
			}
		}
	}
	for name := range m.Inputs {
		if !usedInputs[name] && !dynamicInputs {
			return fmt.Errorf("input %q is declared but never mounted", name)
		}
	}
	for name := range m.Secrets {
		switch {
		case writers[name] != 1:
			return fmt.Errorf("secret file %q needs exactly one read-write docker host mount; found %d", name, writers[name])
		case readers[name] == 0:
			return fmt.Errorf("secret file %q has no read-only drive consumer", name)
		}
	}
	return nil
}

// DrivePrefix marks a step that runs beside the target rather than on it: a
// container here, pointed at the target's network endpoint.
const DrivePrefix = "drive://"

// IsDrive reports whether a probe names a container to run here.
func IsDrive(probe string) bool { return strings.HasPrefix(probe, DrivePrefix) }

func validProbe(p string) error {
	if p == "" {
		return errors.New("a step needs a probe")
	}
	if IsDrive(p) {
		if strings.TrimPrefix(p, DrivePrefix) == "" {
			return fmt.Errorf("probe %q names no image to run", p)
		}
		return nil
	}
	if path.IsAbs(p) || strings.HasPrefix(p, "..") || strings.Contains(p, "/../") {
		return fmt.Errorf("probe %q must be a path inside the scenario: a probe is content-addressed with the scenario that ships it, and one reached through .. is not", p)
	}
	return nil
}

// hash pins two deliberately different identities. Runtime-v2 covers the
// manifest and probe inputs that can affect measurement. TreeHash covers every
// file for checkout provenance without making a README citation recursively
// change the identity of what executed.
func (m *Manifest) hash() error {
	m.Probes = make(map[string]string)
	m.Files = make(map[string]string)
	runtime := sha256.New()
	wholeTree := sha256.New()
	runtimeFiles := map[string]bool{File: true}
	for _, step := range m.Steps {
		if step.Carry == nil && !IsDrive(step.Probe) {
			runtimeFiles[filepath.ToSlash(step.Probe)] = true
		}
	}

	err := filepath.WalkDir(m.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(m.Dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		sum, err := hashFile(p)
		if err != nil {
			return err
		}
		m.Files[rel] = sum
		if rel != File && (runtimeFiles[rel] || strings.HasPrefix(rel, "probes/")) {
			m.Probes[rel] = sum
		}
		// A hash.Hash never fails a write.
		_, _ = fmt.Fprintf(wholeTree, "%s\n%s\n", rel, sum)
		if runtimeFiles[rel] || strings.HasPrefix(rel, "probes/") {
			_, _ = fmt.Fprintf(runtime, "%s\n%s\n", rel, sum)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("hash scenario %s: %w", m.Dir, err)
	}

	m.HashVersion = "runtime-v2"
	m.Hash = "sha256:" + hex.EncodeToString(runtime.Sum(nil))
	m.TreeHash = "sha256:" + hex.EncodeToString(wholeTree.Sum(nil))
	return nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Names lists the scenarios under a root directory.
func Names(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("list scenarios: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), File)); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}
