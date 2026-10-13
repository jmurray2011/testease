// Package campaign orchestrates repeatable scenario runs and records the
// lifecycle around them. It deliberately does not provision machines itself:
// executable hooks are the seam to whatever owns the lab.
package campaign

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/scenario"
)

const Schema = 1

type Definition struct {
	Schema               int                 `yaml:"schema" json:"schema"`
	Name                 string              `yaml:"name" json:"name"`
	Scenario             string              `yaml:"scenario" json:"scenario"`
	Params               map[string]string   `yaml:"params,omitempty" json:"params,omitempty"`
	Matrix               map[string][]string `yaml:"matrix,omitempty" json:"matrix,omitempty"`
	Parallel             int                 `yaml:"parallel,omitempty" json:"parallel"`
	ReleaseFailedTargets bool                `yaml:"release_failed_targets,omitempty" json:"release_failed_targets"`
	Target               Selector            `yaml:"target,omitempty" json:"target,omitempty"`
	Repeat               int                 `yaml:"repeat,omitempty" json:"repeat"`
	Judge                string              `yaml:"judge,omitempty" json:"judge,omitempty"`
	Render               string              `yaml:"render,omitempty" json:"render,omitempty"`
	Reproduce            bool                `yaml:"reproduce,omitempty" json:"reproduce"`
	Preflight            Preflight           `yaml:"preflight,omitempty" json:"preflight"`
	Lifecycle            Lifecycle           `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
	InputBundle          string              `yaml:"input_bundle,omitempty" json:"input_bundle,omitempty"`

	Dir    string `yaml:"-" json:"dir,omitempty"`
	SHA256 string `yaml:"-" json:"sha256"`
}

type Selector struct {
	Name string   `yaml:"name,omitempty" json:"name,omitempty"`
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`
}

type Lifecycle struct {
	Prepare string   `yaml:"prepare,omitempty" json:"prepare,omitempty"`
	Cleanup string   `yaml:"cleanup,omitempty" json:"cleanup,omitempty"`
	Inputs  []string `yaml:"inputs,omitempty" json:"inputs,omitempty"`
}

type Preflight struct {
	Skip   bool `yaml:"skip,omitempty" json:"skip"`
	Strict bool `yaml:"strict,omitempty" json:"strict"`
}

func Load(dir string) (*Definition, error) {
	path := filepath.Join(dir, "campaign.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read campaign %s: %w", path, err)
	}
	var definition Definition
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(&definition); err != nil {
		return nil, fmt.Errorf("parse campaign %s: %w", path, err)
	}
	if definition.Schema != Schema {
		return nil, fmt.Errorf("campaign %s has schema %d; this testease reads schema %d", path, definition.Schema, Schema)
	}
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Scenario = strings.TrimSpace(definition.Scenario)
	if definition.Name == "" {
		return nil, fmt.Errorf("campaign %s has no name", path)
	}
	if definition.Scenario == "" {
		return nil, fmt.Errorf("campaign %s has no scenario", path)
	}
	if definition.Repeat == 0 {
		definition.Repeat = 1
	}
	if definition.Repeat < 1 {
		return nil, fmt.Errorf("campaign %s repeat must be at least 1", path)
	}
	if definition.Reproduce && definition.Repeat < 2 {
		return nil, fmt.Errorf("campaign %s asks for reproduction with only %d run", path, definition.Repeat)
	}
	if definition.Parallel == 0 {
		definition.Parallel = 1
	}
	if definition.Parallel < 1 {
		return nil, fmt.Errorf("campaign %s parallel must be at least 1", path)
	}
	if definition.Params == nil {
		definition.Params = map[string]string{}
	}
	if definition.Matrix == nil {
		definition.Matrix = map[string][]string{}
	}
	for name := range definition.Params {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return nil, fmt.Errorf("campaign %s has invalid parameter name %q", path, name)
		}
	}
	for name, values := range definition.Matrix {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return nil, fmt.Errorf("campaign %s has invalid matrix axis name %q", path, name)
		}
		if _, pinned := definition.Params[name]; pinned {
			return nil, fmt.Errorf("campaign %s both pins and sweeps parameter %q", path, name)
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("campaign %s matrix axis %q has no values", path, name)
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || strings.Contains(value, ",") {
				return nil, fmt.Errorf("campaign %s matrix axis %q has invalid value %q", path, name, value)
			}
		}
	}
	definition.Target.Name = strings.TrimSpace(definition.Target.Name)
	for i, tag := range definition.Target.Tags {
		definition.Target.Tags[i] = strings.TrimSpace(tag)
		if definition.Target.Tags[i] == "" {
			return nil, fmt.Errorf("campaign %s target selector has an empty tag", path)
		}
	}
	for _, input := range append([]string{definition.Lifecycle.Prepare, definition.Lifecycle.Cleanup}, definition.Lifecycle.Inputs...) {
		if input == "" {
			continue
		}
		path := input
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("campaign %s lifecycle input %s: %w", definition.Name, input, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("campaign %s lifecycle input %s is not a regular file", definition.Name, input)
		}
		if (input == definition.Lifecycle.Prepare || input == definition.Lifecycle.Cleanup) && info.Mode()&0o111 == 0 {
			return nil, fmt.Errorf("campaign %s hook %s is not an executable file", definition.Name, input)
		}
	}
	if definition.InputBundle != "" && !filepath.IsAbs(definition.InputBundle) {
		definition.InputBundle = filepath.Join(dir, definition.InputBundle)
	}
	sum := sha256.Sum256(body)
	definition.Dir = dir
	definition.SHA256 = hex.EncodeToString(sum[:])
	return &definition, nil
}

func HookPath(definition *Definition, hook string) string {
	if hook == "" || filepath.IsAbs(hook) {
		return hook
	}
	return filepath.Join(definition.Dir, hook)
}

// ValidateAgainstScenario checks the part of a campaign that only makes
// sense in the context of its scenario. Keeping this separate from Load lets
// callers load and inspect a campaign without needing its scenario tree, but
// running a campaign must do this before hooks or resources are touched.
func (d *Definition) ValidateAgainstScenario(manifest *scenario.Manifest) error {
	if manifest == nil {
		return fmt.Errorf("campaign %q has no scenario to validate", d.Name)
	}
	if d.Scenario != manifest.Name {
		return fmt.Errorf("campaign %q names scenario %q, but the loaded scenario is %q", d.Name, d.Scenario, manifest.Name)
	}
	for name := range d.Params {
		if _, ok := manifest.Params[name]; !ok {
			return fmt.Errorf("campaign parameter %q is not declared by scenario %q", name, manifest.Name)
		}
	}
	for name := range d.Matrix {
		param, ok := manifest.Params[name]
		if !ok {
			return fmt.Errorf("campaign matrix axis %q is not declared by scenario %q", name, manifest.Name)
		}
		if !param.Axis {
			return fmt.Errorf("campaign matrix axis %q is declared by the scenario but is not marked axis: true", name)
		}
	}
	if d.Judge != "" {
		name, version, hasVersion := strings.Cut(d.Judge, "@")
		selected, err := judge.ByName(name)
		if err != nil {
			return fmt.Errorf("campaign judge %q: %w", d.Judge, err)
		}
		if hasVersion && version != "" && version != selected.Version() {
			return fmt.Errorf("campaign judge %q names version %q, but testease ships %s@%s", d.Judge, version, selected.Name(), selected.Version())
		}
	}
	if d.Render != "" {
		if _, ok := manifest.Views[d.Render]; !ok {
			return fmt.Errorf("campaign render preset %q is not declared by scenario %q", d.Render, manifest.Name)
		}
	}
	return nil
}
