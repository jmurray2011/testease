package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/record"
)

// side is one run in a comparison: the record as Runs reads it, and how the
// run said it started, which pins the scenario's files and the environment.
type side struct {
	d       browse.Detail
	started record.RunStarted
}

// loadSide reads one record for a comparison.
func loadSide(dir string) (side, error) {
	d, err := browse.Open(dir)
	if err != nil {
		return side{}, err
	}
	s := side{d: d}
	if rec, err := record.Read(dir); err == nil {
		for _, e := range rec.Latest().Events {
			if e.Kind != record.KindRunStarted {
				continue
			}
			if started, err := record.Decode[record.RunStarted](e); err == nil {
				s.started = started
			}
			break
		}
	}
	return s, nil
}

// The sections of a comparison. What went in comes first, then what came
// out: a difference in the first is a likely reason for one in the second.
const (
	secSetup    = "Setup"
	secScenario = "Scenario files"
	secParams   = "Params"
	secEnv      = "Environment"
	secTargets  = "Targets"
	secSteps    = "Steps"
	secTimes    = "Step times"
	secFacts    = "Facts"
	secOutcome  = "Outcome"
)

var (
	wentIn       = []string{secSetup, secScenario, secParams, secEnv, secTargets}
	cameOut      = []string{secSteps, secTimes, secFacts, secOutcome}
	diffSections = append(append([]string(nil), wentIn...), cameOut...)
)

// diffRow is one thing compared across the runs: one value per run, ""
// where a run has none. Differs is decided on what was recorded, so two
// secrets that differ still differ though neither is shown.
type diffRow struct {
	Section string
	Name    string
	Values  []string
	Differs bool
	// Changed says, run by run, whether what was recorded differs from the
	// first run's: a hidden secret looks the same either way.
	Changed []bool
	// heading marks a section's title row on screen.
	heading bool
}

// diffRuns lines the runs up, section by section.
func diffRuns(sides []side) []diffRow {
	var rows []diffRow
	add := func(section, name string, raw []string, show func(string) string) {
		r := diffRow{Section: section, Name: name, Values: make([]string, len(raw)), Changed: make([]bool, len(raw))}
		for i, v := range raw {
			if v != "" {
				r.Values[i] = show(v)
			}
			if v != raw[0] {
				r.Differs, r.Changed[i] = true, true
			}
		}
		rows = append(rows, r)
	}
	plain := func(v string) string { return v }
	each := func(f func(side) string) []string {
		out := make([]string, len(sides))
		for i, s := range sides {
			out[i] = f(s)
		}
		return out
	}
	// keyed adds a row for every key any run has, in name order.
	keyed := func(section string, maps func(side) map[string]string, show func(key string) func(string) string) {
		all := make([]map[string]string, len(sides))
		var keys []string
		for i, s := range sides {
			all[i] = maps(s)
			for k := range all[i] {
				if !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			raw := make([]string, len(sides))
			for i := range sides {
				raw[i] = all[i][k]
			}
			add(section, k, raw, show(k))
		}
	}
	masked := func(key string) func(string) string { return func(v string) string { return shown(key, v) } }
	hashed := func(string) func(string) string { return shortHash }

	add(secSetup, "Tool", each(func(s side) string { return toolText(s.d.Tool) }), plain)
	add(secSetup, "Scenario", each(func(s side) string { return s.d.Scenario }), plain)
	add(secSetup, "Scenario hash", each(func(s side) string { return s.started.Scenario.Hash }), shortHash)
	add(secSetup, "Driver", each(func(s side) string { return s.started.Driver }), plain)
	add(secSetup, "Runner", each(func(s side) string { return runnerText(s.d.Runner) }), plain)
	add(secSetup, "Judge", each(func(s side) string {
		if j := s.started.Judge; j != nil {
			return j.Name + "@" + j.Version
		}
		return ""
	}), plain)
	keyed(secScenario, func(s side) map[string]string {
		files := map[string]string{}
		for k, v := range s.started.Scenario.Files {
			files[k] = v
		}
		for k, v := range s.started.Scenario.Probes {
			files["probe "+k] = v
		}
		return files
	}, hashed)
	keyed(secParams, func(s side) map[string]string { return s.d.Params }, masked)
	keyed(secEnv, func(s side) map[string]string { return s.started.EnvFingerprint }, masked)
	keyed(secTargets, func(s side) map[string]string {
		facts := map[string]string{}
		for _, h := range s.d.Hosts {
			for k, v := range h.Facts {
				facts[h.Host+" · "+k] = v
			}
		}
		for _, t := range s.d.Targets {
			facts[t.Host+" · fate"] = fateText(t.Fate)
		}
		return facts
	}, func(key string) func(string) string {
		_, name, _ := strings.Cut(key, " · ")
		return masked(name)
	})

	// Steps are matched by label, and a label used twice by its turn.
	names := stepNames(sides)
	for _, name := range names {
		steps := make([]*browse.Step, len(sides))
		for i, s := range sides {
			steps[i] = stepNamed(s.d.StepList, name)
		}
		raw := make([]string, len(sides))
		for i, st := range steps {
			if st != nil {
				raw[i] = stepResult(*st)
			}
		}
		add(secSteps, name, raw, plain)
	}
	for _, name := range names {
		r := diffRow{Section: secTimes, Name: name, Values: make([]string, len(sides))}
		var times []float64
		for i, s := range sides {
			if st := stepNamed(s.d.StepList, name); st != nil && st.Status != "" {
				r.Values[i] = seconds(st.DurationS)
				times = append(times, st.DurationS)
			}
		}
		r.Differs = len(times) > 1 && muchSlower(times)
		r.Changed = make([]bool, len(sides))
		for i, v := range r.Values {
			r.Changed[i] = r.Differs && v != r.Values[0]
		}
		rows = append(rows, r)
	}
	for _, name := range names {
		facts := make([]map[string]string, len(sides))
		var keys []string
		for i, s := range sides {
			facts[i] = map[string]string{}
			if st := stepNamed(s.d.StepList, name); st != nil {
				for _, f := range st.Facts {
					facts[i][f.Key] = factText(f)
					if !slices.Contains(keys, f.Key) {
						keys = append(keys, f.Key)
					}
				}
			}
		}
		for _, k := range keys {
			raw := make([]string, len(sides))
			for i := range sides {
				raw[i] = facts[i][k]
			}
			add(secFacts, name+" · "+k, raw, masked(k))
		}
	}

	add(secOutcome, "Status", each(func(s side) string { return s.d.Status }), plain)
	add(secOutcome, "Verdict", each(func(s side) string {
		text, _ := verdictSummary(s.d.Verdicts)
		return text
	}), plain)
	return rows
}

// stepNames is every step across the runs, in the order they ran, a label
// used more than once numbered by its turn.
func stepNames(sides []side) []string {
	var names []string
	for _, s := range sides {
		for _, n := range turnNames(s.d.StepList) {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	return names
}

func turnNames(steps []browse.Step) []string {
	seen := map[string]int{}
	out := make([]string, len(steps))
	for i, st := range steps {
		seen[st.Label]++
		out[i] = st.Label
		if n := seen[st.Label]; n > 1 {
			out[i] = fmt.Sprintf("%s (%d)", st.Label, n)
		}
	}
	return out
}

func stepNamed(steps []browse.Step, name string) *browse.Step {
	for i, n := range turnNames(steps) {
		if n == name {
			return &steps[i]
		}
	}
	return nil
}

func stepResult(s browse.Step) string {
	text := stepStatus(s)
	if s.RC != nil {
		text += fmt.Sprintf(" · rc %d", *s.RC)
	}
	return text
}

// factText is a fact's reading, or why there is none.
func factText(f record.Fact) string {
	switch {
	case f.Value != nil:
		return *f.Value
	case f.Error != "":
		return "error: " + f.Error
	default:
		return "no reading"
	}
}

// muchSlower says whether a step's times differ by more than a run's own
// jitter: half again as long, and a second or more.
func muchSlower(times []float64) bool {
	lo, hi := slices.Min(times), slices.Max(times)
	return hi-lo >= 1 && hi >= 1.5*lo
}

func toolText(t record.ToolRef) string {
	if t.Commit == "" {
		return t.Version
	}
	return t.Version + " (" + shorten(t.Commit, 8) + ")"
}

func runnerText(h record.HostRef) string {
	if h.User == "" && h.Hostname == "" {
		return ""
	}
	return h.User + "@" + h.Hostname
}

// What each section's rows are called, one and many.
var rowNouns = map[string][2]string{
	secScenario: {"scenario file", "scenario files"},
	secParams:   {"param", "params"},
	secEnv:      {"environment value", "environment values"},
	secTargets:  {"target detail", "target details"},
	secSteps:    {"step result", "step results"},
	secTimes:    {"step time", "step times"},
	secFacts:    {"fact", "facts"},
}

// explainNames is how many differences a sentence names before it counts
// the rest.
const explainNames = 3

// explain says what differs, in words: first what went in, then what came
// out. A row of Setup or Outcome gets a sentence of its own with its values;
// the other sections are counted and their first few named.
func explain(rows []diffRow, runs int) (in, out []string) {
	say := func(secs []string) []string {
		var lines []string
		for _, sec := range secs {
			var differ []diffRow
			for _, r := range rows {
				if r.Section == sec && r.Differs {
					differ = append(differ, r)
				}
			}
			if len(differ) == 0 {
				continue
			}
			noun, ok := rowNouns[sec]
			if !ok {
				for _, r := range differ {
					lines = append(lines, valuesLine(r, runs))
				}
				continue
			}
			names := make([]string, 0, explainNames)
			for _, r := range differ[:min(len(differ), explainNames)] {
				names = append(names, r.Name)
			}
			list := strings.Join(names, ", ")
			if extra := len(differ) - len(names); extra > 0 {
				list = strings.Join(names, ", ") + fmt.Sprintf(" and %d more", extra)
			}
			if len(differ) == 1 {
				lines = append(lines, fmt.Sprintf("1 %s differs: %s", noun[0], list))
			} else {
				lines = append(lines, fmt.Sprintf("%d %s differ: %s", len(differ), noun[1], list))
			}
		}
		return lines
	}
	in, out = say(wentIn), say(cameOut)
	switch {
	case len(in) == 0 && len(out) == 0:
		in = []string{"Nothing differs in what these runs recorded, apart from when they ran."}
	case len(in) == 0:
		in = []string{"Nothing recorded about what went in differs. The difference came from the target or from the run itself."}
	}
	return in, out
}

// valuesLine is one row's values in a sentence: before and after for two
// runs, each value once for more.
func valuesLine(r diffRow, runs int) string {
	vals := make([]string, len(r.Values))
	for i, v := range r.Values {
		vals[i] = orDash(v)
	}
	if runs == 2 {
		return r.Name + ": " + vals[0] + ", then " + vals[1]
	}
	var once []string
	for _, v := range vals {
		if !slices.Contains(once, v) {
			once = append(once, v)
		}
	}
	return r.Name + " differs: " + strings.Join(once, ", ")
}
