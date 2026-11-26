package main

import (
	"encoding/json"
	"sort"
	"strings"

	"fyne.io/fyne/v2"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/record"
)

// preset is a run's form saved under a name, for one scenario. It holds no
// secret: a param whose name says it is one is left out when saved.
type preset struct {
	Params map[string]string `json:"params,omitempty"`
	Matrix map[string]string `json:"matrix,omitempty"`
	Target string            `json:"target,omitempty"`
	Keep   bool              `json:"keep,omitempty"`
	Extra  string            `json:"extra,omitempty"`
}

func presetOf(r runRequest) preset {
	p := preset{Params: map[string]string{}, Matrix: map[string]string{}, Target: r.Target, Keep: r.Keep, Extra: r.Extra}
	for k, v := range r.Params {
		if v != "" && !looksSecret(k) {
			p.Params[k] = v
		}
	}
	for k, v := range r.Matrix {
		if v != "" && !looksSecret(k) {
			p.Matrix[k] = v
		}
	}
	return p
}

func (p preset) request() runRequest {
	return runRequest{Params: p.Params, Matrix: p.Matrix, Target: p.Target, Keep: p.Keep, Extra: p.Extra}
}

// presetKey is where a scenario's presets are kept in the app's preferences.
func presetKey(scenario string) string { return "presets." + scenario }

func loadPresets(scenario string) map[string]preset {
	out := map[string]preset{}
	if raw := fyne.CurrentApp().Preferences().String(presetKey(scenario)); raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

func savePresets(scenario string, all map[string]preset) {
	prefs := fyne.CurrentApp().Preferences()
	if len(all) == 0 {
		prefs.RemoveValue(presetKey(scenario))
		return
	}
	if raw, err := json.Marshal(all); err == nil {
		prefs.SetString(presetKey(scenario), string(raw))
	}
}

// againRequest is what a record was run with, as a form fills from it: its
// params, and each axis's values as the run recorded them. A secret is not
// copied out of a record; dropped names the ones left for a person to type.
func againRequest(d browse.Detail, m *record.Matrix) (r runRequest, dropped []string) {
	r = runRequest{Params: map[string]string{}, Matrix: map[string]string{}}
	for k, v := range d.Params {
		if looksSecret(k) {
			dropped = append(dropped, k)
			continue
		}
		r.Params[k] = v
	}
	if m != nil {
		for k, vs := range m.Axes {
			r.Matrix[k] = strings.Join(vs, ",")
		}
	}
	sort.Strings(dropped)
	return r, dropped
}
