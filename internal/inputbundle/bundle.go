// Package inputbundle binds a scenario's logical, non-secret inputs to exact
// bytes supplied for one invocation.
package inputbundle

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jmurray2011/testease/internal/scenario"
)

const (
	Schema   = 1
	cacheDir = ".testease-inputs"
)

// Descriptor is a portable binding from logical input names to files. Paths
// are relative to the descriptor unless absolute; hashes identify the bytes,
// while paths merely say where this runner found them.
type Descriptor struct {
	Schema int              `yaml:"schema"`
	Inputs map[string]Entry `yaml:"inputs"`
}

type Entry struct {
	Path      string `yaml:"path"`
	SHA256    string `yaml:"sha256"`
	GitCommit string `yaml:"git_commit,omitempty"`
}

// Resolved is one verified input and the daemon-visible copy the runner uses.
type Resolved struct {
	Name           string
	Kind           string
	RequestedPath  string
	StagedPath     string
	SHA256         string
	GitCommit      string
	Size           int64
	DescriptorHash string
}

// LoadAndStage verifies every input the scenario declares and copies its exact
// bytes into a content-addressed cache beneath runsDir. Keeping the cache next
// to the records avoids Docker installations whose mount namespace cannot see
// the process's /tmp.
func LoadAndStage(path, runsDir string, required map[string]scenario.Input) (map[string]Resolved, error) {
	if len(required) == 0 {
		if path != "" {
			return nil, fmt.Errorf("--input-bundle was given, but the scenario declares no inputs")
		}
		return nil, nil
	}
	if path == "" {
		return nil, fmt.Errorf("the scenario declares %d input(s); supply --input-bundle <descriptor>", len(required))
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read input bundle %s: %w", path, err)
	}
	var descriptor Descriptor
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	dec.KnownFields(true)
	if err := dec.Decode(&descriptor); err != nil {
		return nil, fmt.Errorf("read input bundle %s: %w", path, err)
	}
	if descriptor.Schema != Schema {
		return nil, fmt.Errorf("input bundle schema %d is not one this testease knows (%d)", descriptor.Schema, Schema)
	}

	descriptorSum := sha256.Sum256(body)
	descriptorHash := "sha256:" + hex.EncodeToString(descriptorSum[:])
	base := filepath.Dir(path)
	out := make(map[string]Resolved, len(required))
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		want := required[name]
		entry, ok := descriptor.Inputs[name]
		if !ok {
			return nil, fmt.Errorf("input bundle %s has no entry for scenario input %q", path, name)
		}
		resolved, err := stage(base, runsDir, name, want.Kind, entry, descriptorHash)
		if err != nil {
			return nil, err
		}
		out[name] = resolved
	}
	return out, nil
}

func stage(base, runsDir, name, kind string, entry Entry, descriptorHash string) (Resolved, error) {
	if entry.Path == "" {
		return Resolved{}, fmt.Errorf("input %q has no path", name)
	}
	expected, err := digest(entry.SHA256)
	if err != nil {
		return Resolved{}, fmt.Errorf("input %q: %w", name, err)
	}
	if kind == "git-archive" && entry.GitCommit == "" {
		return Resolved{}, fmt.Errorf("input %q is a git-archive and has no git_commit", name)
	}

	source := entry.Path
	if !filepath.IsAbs(source) {
		source = filepath.Join(base, source)
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return Resolved{}, fmt.Errorf("input %q path: %w", name, err)
	}
	info, err := os.Stat(source)
	if err != nil {
		return Resolved{}, fmt.Errorf("input %q at %s: %w", name, source, err)
	}
	if !info.Mode().IsRegular() {
		return Resolved{}, fmt.Errorf("input %q at %s is not a regular file", name, source)
	}

	dir := filepath.Join(runsDir, cacheDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Resolved{}, fmt.Errorf("make input cache: %w", err)
	}
	destination := filepath.Join(dir, strings.TrimPrefix(expected, "sha256:"))
	ok, err := fileHasDigest(destination, expected)
	if err != nil {
		return Resolved{}, fmt.Errorf("check cached input %q: %w", name, err)
	}
	if !ok {
		// This namespace belongs only to testease and the filename claims a
		// digest its bytes do not have. Removing that corrupt cache entry is
		// safer than letting platform-specific rename semantics choose whether
		// the verified replacement wins.
		if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Resolved{}, fmt.Errorf("replace corrupt cached input %q: %w", name, err)
		}
		if err := copyVerified(source, destination, expected); err != nil {
			return Resolved{}, fmt.Errorf("input %q: %w", name, err)
		}
	}
	if kind == "git-archive" {
		commit, err := gitArchiveCommit(destination)
		if err != nil {
			return Resolved{}, fmt.Errorf("input %q: %w", name, err)
		}
		if !strings.EqualFold(commit, entry.GitCommit) {
			return Resolved{}, fmt.Errorf("input %q git archive says commit %s, descriptor says %s", name, commit, entry.GitCommit)
		}
	}

	return Resolved{
		Name: name, Kind: kind, RequestedPath: source, StagedPath: destination,
		SHA256: expected, GitCommit: entry.GitCommit, Size: info.Size(), DescriptorHash: descriptorHash,
	}, nil
}

func digest(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimPrefix(raw, "sha256:")
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("sha256 must be 64 hexadecimal characters")
	}
	return "sha256:" + raw, nil
}

func copyVerified(source, destination, expected string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(destination), ".input-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, h), in)
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	actual := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("sha256 is %s, descriptor says %s", actual, expected)
	}
	if err := os.Chmod(tmpName, 0o400); err != nil {
		return err
	}
	if err := os.Rename(tmpName, destination); err != nil {
		if ok, _ := fileHasDigest(destination, expected); ok {
			return nil
		}
		return err
	}
	return nil
}

func fileHasDigest(path, expected string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return "sha256:"+hex.EncodeToString(h.Sum(nil)) == expected, nil
}

// gitArchiveCommit reads the commit Git writes into a tar archive's global
// pax comment. It verifies the archive itself rather than trusting a filename
// or a second sidecar generated by the same packaging script.
func gitArchiveCommit(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read git archive: %w", err)
		}
		for key, value := range h.PAXRecords {
			if strings.EqualFold(key, "comment") && value != "" {
				return strings.TrimSpace(value), nil
			}
		}
	}
	return "", fmt.Errorf("git archive has no embedded commit marker")
}

// Bind replaces @name mount sources with their verified staged paths.
func Bind(plan *scenario.Plan, inputs map[string]Resolved) error {
	paths := make(map[string]string, len(inputs))
	for name, input := range inputs {
		paths[name] = input.StagedPath
	}
	return scenario.BindMountSources(plan, "@", paths)
}
