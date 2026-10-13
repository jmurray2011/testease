// Package targetcatalog resolves durable target names and capability tags into
// the scenario parameters needed to reach a target.
package targetcatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const Schema = 1

type Catalog struct {
	Schema  int       `yaml:"schema" json:"schema"`
	Targets []Profile `yaml:"targets" json:"targets"`

	Path   string `yaml:"-" json:"path,omitempty"`
	SHA256 string `yaml:"-" json:"sha256"`
}

type Profile struct {
	Name   string            `yaml:"name" json:"name"`
	Tags   []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	Params map[string]string `yaml:"params" json:"params"`
}

type Selector struct {
	Name string   `json:"name,omitempty"`
	Tags []string `json:"tags,omitempty"`
}

type Selection struct {
	Profile       Profile  `json:"profile"`
	Selector      Selector `json:"selector"`
	CatalogPath   string   `json:"catalog_path,omitempty"`
	CatalogSHA256 string   `json:"catalog_sha256"`
}

func Load(path string) (*Catalog, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read target catalog %s: %w", path, err)
	}
	var catalog Catalog
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("parse target catalog %s: %w", path, err)
	}
	if catalog.Schema != Schema {
		return nil, fmt.Errorf("target catalog %s has schema %d; this testease reads schema %d", path, catalog.Schema, Schema)
	}
	if len(catalog.Targets) == 0 {
		return nil, fmt.Errorf("target catalog %s has no targets", path)
	}
	seen := map[string]bool{}
	for i := range catalog.Targets {
		profile := &catalog.Targets[i]
		profile.Name = strings.TrimSpace(profile.Name)
		if profile.Name == "" {
			return nil, fmt.Errorf("target catalog %s target %d has no name", path, i+1)
		}
		if seen[profile.Name] {
			return nil, fmt.Errorf("target catalog %s names target %q more than once", path, profile.Name)
		}
		seen[profile.Name] = true
		if len(profile.Params) == 0 {
			return nil, fmt.Errorf("target catalog %s target %q has no params", path, profile.Name)
		}
		seenTags := map[string]bool{}
		for j, tag := range profile.Tags {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				return nil, fmt.Errorf("target catalog %s target %q has an empty tag", path, profile.Name)
			}
			if seenTags[tag] {
				return nil, fmt.Errorf("target catalog %s target %q repeats tag %q", path, profile.Name, tag)
			}
			seenTags[tag] = true
			profile.Tags[j] = tag
		}
		sort.Strings(profile.Tags)
		for key := range profile.Params {
			if strings.TrimSpace(key) == "" {
				return nil, fmt.Errorf("target catalog %s target %q has an empty parameter name", path, profile.Name)
			}
		}
	}
	sort.Slice(catalog.Targets, func(i, j int) bool { return catalog.Targets[i].Name < catalog.Targets[j].Name })
	sum := sha256.Sum256(body)
	catalog.Path = path
	catalog.SHA256 = hex.EncodeToString(sum[:])
	return &catalog, nil
}

func (c *Catalog) Resolve(selector Selector) (Selection, error) {
	selector.Name = strings.TrimSpace(selector.Name)
	selector.Tags = normalizedTags(selector.Tags)
	if selector.Name == "" && len(selector.Tags) == 0 {
		return Selection{}, fmt.Errorf("a target selector needs --target or at least one --target-tag")
	}

	matches := c.Matching(selector)
	if len(matches) == 0 {
		return Selection{}, fmt.Errorf("target selector %s matched no catalog target", describe(selector))
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, match := range matches {
			names[i] = match.Name
		}
		return Selection{}, fmt.Errorf("target selector %s is ambiguous; it matched %s", describe(selector), strings.Join(names, ", "))
	}
	return Selection{
		Profile:       matches[0],
		Selector:      selector,
		CatalogPath:   c.Path,
		CatalogSHA256: c.SHA256,
	}, nil
}

func (c *Catalog) Matching(selector Selector) []Profile {
	selector.Name = strings.TrimSpace(selector.Name)
	selector.Tags = normalizedTags(selector.Tags)
	matches := make([]Profile, 0)
	for _, profile := range c.Targets {
		if selector.Name != "" && profile.Name != selector.Name {
			continue
		}
		if !hasEveryTag(profile.Tags, selector.Tags) {
			continue
		}
		matches = append(matches, profile)
	}
	return matches
}

func normalizedTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func hasEveryTag(have, want []string) bool {
	for _, tag := range want {
		if !slices.Contains(have, tag) {
			return false
		}
	}
	return true
}

func describe(selector Selector) string {
	var pieces []string
	if selector.Name != "" {
		pieces = append(pieces, fmt.Sprintf("name=%q", selector.Name))
	}
	if len(selector.Tags) > 0 {
		pieces = append(pieces, "tags="+strings.Join(selector.Tags, ","))
	}
	return strings.Join(pieces, " ")
}
