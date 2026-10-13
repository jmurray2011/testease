package scenario

import (
	"bytes"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"text/template"
	"time"
)

// Plan is a scenario with its templates resolved: what this run will actually
// do, with the parameter values it will do it with.
type Plan struct {
	Name     string
	Question string
	Dir      string
	ContentIdentity
	Requires  []Capability
	Params    map[string]string
	Inputs    map[string]Input
	Secrets   map[string]SecretFile
	Hosts     map[string]Host
	Steps     []Step
	Judge     *JudgeRef
	Reproduce *Reproduce
	Views     map[string]View
	Artifacts []Artifact
}

// Resolve fills the scenario's parameters and expands every template in it.
// Overrides win over declared defaults.
//
// A template that does not resolve is a precondition failure rather than an
// empty string reaching a probe, and every failure here names what is missing.
func (m *Manifest) Resolve(overrides map[string]string) (*Plan, error) {
	params, err := m.params(overrides)
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		Name:            m.Name,
		Question:        m.Question,
		Dir:             m.Dir,
		ContentIdentity: m.ContentIdentity,
		Requires:        m.Requires,
		Params:          params,
		Inputs:          m.Inputs,
		Secrets:         m.Secrets,
		Hosts:           make(map[string]Host, len(m.Hosts)),
		Steps:           make([]Step, 0, len(m.Steps)),
		Judge:           m.Judge,
		Reproduce:       m.Reproduce,
		Views:           m.Views,

		// Artifacts name steps and paths on the target; neither is templated,
		// so they carry through as written.
		Artifacts: m.Artifacts,
	}

	for name, h := range m.Hosts {
		// Everything a host says about itself is templated, image included. An
		// image left unexpanded does not fail loudly: it reaches the backend as
		// a literal {{.image}} and the run dies blaming the image, which is the
		// wrong diagnosis for a scenario that simply never got its parameter.
		where := fmt.Sprintf("host %q", name)
		providerName, err := expand(h.Provider, params, where+" provider")
		if err != nil {
			return nil, err
		}
		image, err := expand(h.Image, params, where+" image")
		if err != nil {
			return nil, err
		}
		address, err := expand(h.Address, params, where+" address")
		if err != nil {
			return nil, err
		}
		origin, err := expand(h.Origin, params, where+" origin")
		if err != nil {
			return nil, err
		}
		if err := validOrigin(origin); err != nil {
			return nil, fmt.Errorf("%s origin: %w", where, err)
		}
		backendName, err := expand(h.Backend, params, where+" backend")
		if err != nil {
			return nil, err
		}
		media, err := expandAll(h.Media, params, where+" media")
		if err != nil {
			return nil, err
		}
		ready, err := expandAll(h.Ready, params, where+" ready")
		if err != nil {
			return nil, err
		}
		if len(h.Env) > 0 {
			env := make(map[string]string, len(h.Env))
			for k, v := range h.Env {
				expanded, err := expand(v, params, fmt.Sprintf("%s env %s", where, k))
				if err != nil {
					return nil, err
				}
				env[k] = expanded
			}
			h.Env = env
		}
		h.Provider, h.Image, h.Address, h.Origin, h.Backend, h.Media, h.Ready = providerName, image, address, origin, backendName, media, ready
		plan.Hosts[name] = h
	}

	for _, s := range m.Steps {
		where := fmt.Sprintf("step %q", s.Label)
		args, err := expandAll(s.Args, params, where+" args")
		if err != nil {
			return nil, err
		}
		s.Args = args

		// The probe too, so a drive step's image can be a parameter the way
		// everything else about the step already is. Left unexpanded it reaches
		// the container runtime as a literal `drive://{{.image}}` and fails
		// there as "invalid reference format" - a resolve-time mistake reported
		// by docker, four steps into a run.
		probe, err := expand(s.Probe, params, where+" probe")
		if err != nil {
			return nil, err
		}
		s.Probe = probe

		media, err := expandAll(s.Media, params, where+" media")
		if err != nil {
			return nil, err
		}
		s.Media = media

		if s.Expect != nil && len(s.Expect.Facts) > 0 {
			// The rollback check is "the package is still the one we
			// installed", and which one that was is a parameter. Left
			// unexpanded the expectation compares a reading against the
			// literal {{.from}} and calls a correct rollback a regression.
			facts := make(map[string]string, len(s.Expect.Facts))
			for k, v := range s.Expect.Facts {
				expanded, err := expand(v, params, fmt.Sprintf("%s expect %s%s", where, factPrefix, k))
				if err != nil {
					return nil, err
				}
				facts[k] = expanded
			}
			expect := *s.Expect
			expect.Facts = facts
			s.Expect = &expect
		}

		if len(s.Env) > 0 {
			env := make(map[string]string, len(s.Env))
			for k, v := range s.Env {
				expanded, err := expand(v, params, fmt.Sprintf("%s env %s", where, k))
				if err != nil {
					return nil, err
				}
				env[k] = expanded
			}
			s.Env = env
		}
		plan.Steps = append(plan.Steps, s)
	}

	return plan, nil
}

// validOrigin keeps credentials and request-specific material out of the
// manifest and therefore out of the immutable record. Empty means this host
// is not an endpoint-only target.
func validOrigin(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("must be an absolute http(s) origin")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("must contain only scheme, host and optional port")
	}
	return nil
}

// StepTimeout is the step's declared budget, or zero to leave it to the
// backend. A step may raise a backend's budget and may never lower it.
func (s Step) StepTimeout() time.Duration { return time.Duration(s.Timeout) }

func (m *Manifest) params(overrides map[string]string) (map[string]string, error) {
	overrideNames := make([]string, 0, len(overrides))
	for name := range overrides {
		overrideNames = append(overrideNames, name)
	}
	sort.Strings(overrideNames)
	for _, name := range overrideNames {
		if _, ok := m.Params[name]; !ok {
			return nil, fmt.Errorf("no param %q in scenario %s; it declares %s", name, m.Name, declared(m.Params))
		}
	}

	params := make(map[string]string, len(m.Params))
	names := make([]string, 0, len(m.Params))
	for name := range m.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	missing := make([]string, 0)
	for _, name := range names {
		p := m.Params[name]
		if v, ok := overrides[name]; ok {
			params[name] = v
			continue
		}
		if p.Axis {
			// An axis is a set, and resolving one plan means picking a single
			// value out of it. Which value is the census scheduler's to say,
			// or the caller's - never a default quietly standing in for a
			// sweep nobody ran.
			return nil, fmt.Errorf("param %q is a matrix axis: sweep it with --matrix %s=<values> or pick one with --param %s=<value>", name, name, name)
		}
		if p.Default == "" {
			missing = append(missing, name)
			continue
		}
		params[name] = p.Default
	}
	if len(missing) > 0 {
		quoted := make([]string, len(missing))
		hints := make([]string, len(missing))
		for i, name := range missing {
			quoted[i] = fmt.Sprintf("%q", name)
			hints[i] = fmt.Sprintf("--param %s=<value>", name)
		}
		return nil, fmt.Errorf("params %s have no default or value; supply %s", strings.Join(quoted, ", "), strings.Join(hints, " "))
	}
	return params, nil
}

func declared(params map[string]Param) string {
	if len(params) == 0 {
		return "none"
	}
	names := make([]string, 0, len(params))
	for k := range params {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func expandAll(in []string, params map[string]string, where string) ([]string, error) {
	if len(in) == 0 {
		return in, nil
	}
	out := make([]string, 0, len(in))
	for i, v := range in {
		expanded, err := expand(v, params, fmt.Sprintf("%s [%d]", where, i))
		if err != nil {
			return nil, err
		}
		out = append(out, expanded)
	}
	return out, nil
}

func expand(in string, params map[string]string, where string) (string, error) {
	// Missing keys are an error rather than "<no value>" in a probe argument:
	// a probe run against an empty string measures something nobody asked for
	// and records it as if it had been asked for.
	t, err := template.New("scenario").Option("missingkey=error").Funcs(funcs).Parse(in)
	if err != nil {
		return "", fmt.Errorf("%s: %q is not a template: %w", where, in, err)
	}
	var out bytes.Buffer
	if err := t.Execute(&out, params); err != nil {
		return "", fmt.Errorf("%s: %q did not resolve: %w", where, in, err)
	}
	return out.String(), nil
}

// funcs is the fixed function set a manifest template may use. It is fixed on
// purpose: a manifest is a declaration, and a function set that grows is a DSL
// arriving one helper at a time.
var funcs = template.FuncMap{
	// A crossing is one sparse matrix cell, rather than two axes whose
	// Cartesian product would also schedule same-version and backward cells.
	// Keep these functions structural and narrow: general string operations in
	// manifests would be a programming language arriving by degrees.
	"crossingSource": func(crossing string) (string, error) {
		from, _, err := splitCrossing(crossing)
		return from, err
	},
	"crossingTarget": func(crossing string) (string, error) {
		_, to, err := splitCrossing(crossing)
		return to, err
	},
	"store": func(...string) (string, error) {
		return "", fmt.Errorf("store discovery is an axis value spelled @store:<pattern>, not a template function")
	},
}

func splitCrossing(crossing string) (string, string, error) {
	const separator = "-to-"
	from, to, ok := strings.Cut(strings.TrimSpace(crossing), separator)
	if !ok || strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" || strings.Contains(to, separator) {
		return "", "", fmt.Errorf("crossing %q must be exactly <source>-to-<target>", crossing)
	}
	return strings.TrimSpace(from), strings.TrimSpace(to), nil
}

// Axes is the scope a scenario declares for itself: the parameters marked as
// matrix axes, with the values each was declared over.
//
// Values are a comma-separated list. Discovery - naming a source and letting
// the runner enumerate it - is a later step, and a scenario asking for one now
// is told so rather than quietly swept over nothing.
func (m *Manifest) Axes() (map[string][]string, error) {
	axes := map[string][]string{}
	for name, p := range m.Params {
		if !p.Axis {
			continue
		}
		if strings.HasPrefix(p.Values, "@") {
			// Discovery is host-dependent, so the runner does it and records
			// what it found. The manifest only says what to look for.
			axes[name] = []string{p.Values}
			continue
		}
		values := splitValues(p.Values)
		if len(values) == 0 {
			return nil, fmt.Errorf("param %q is an axis with no values", name)
		}
		axes[name] = values
	}
	return axes, nil
}

func splitValues(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
