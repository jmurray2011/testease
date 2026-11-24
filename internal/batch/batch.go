// Package batch runs independent scenarios with bounded concurrency.
package batch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const Schema = 1

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// Definition is one operator-selected set of independent scenario runs.
type Definition struct {
	Schema   int    `yaml:"schema" json:"schema"`
	Name     string `yaml:"name" json:"name"`
	Parallel int    `yaml:"parallel,omitempty" json:"parallel"`
	Tests    []Test `yaml:"tests" json:"tests"`
	SHA256   string `yaml:"-" json:"sha256"`
	Raw      []byte `yaml:"-" json:"-"`
}

type Test struct {
	Name                 string            `yaml:"name,omitempty" json:"name,omitempty"`
	Scenario             string            `yaml:"scenario" json:"scenario"`
	Params               map[string]string `yaml:"params,omitempty" json:"params,omitempty"`
	PathParams           map[string]string `yaml:"path_params,omitempty" json:"-"`
	Target               string            `yaml:"target,omitempty" json:"target,omitempty"`
	TargetCatalog        string            `yaml:"target_catalog,omitempty" json:"target_catalog,omitempty"`
	InputBundle          string            `yaml:"input_bundle,omitempty" json:"input_bundle,omitempty"`
	InputBundlePath      string            `yaml:"input_bundle_path,omitempty" json:"-"`
	AWSProfile           string            `yaml:"aws_profile,omitempty" json:"aws_profile,omitempty"`
	AWSRegion            string            `yaml:"aws_region,omitempty" json:"aws_region,omitempty"`
	ReleaseFailedTargets bool              `yaml:"release_failed_targets,omitempty" json:"release_failed_targets,omitempty"`
}

func Load(path string) (*Definition, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read batch %s: %w", path, err)
	}
	var d Definition
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(&d); err != nil {
		return nil, fmt.Errorf("parse batch %s: %w", path, err)
	}
	if d.Schema != Schema {
		return nil, fmt.Errorf("batch %s has schema %d; this testease reads schema %d", path, d.Schema, Schema)
	}
	if !safeName.MatchString(d.Name) || d.Name == "." || d.Name == ".." {
		return nil, fmt.Errorf("batch %s has invalid name %q", path, d.Name)
	}
	if len(d.Tests) == 0 {
		return nil, fmt.Errorf("batch %s has no tests", path)
	}
	if d.Parallel == 0 {
		d.Parallel = len(d.Tests)
	}
	if d.Parallel < 1 {
		return nil, fmt.Errorf("batch %s parallel must be at least 1", path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve batch path %s: %w", path, err)
	}
	batchDir := filepath.Dir(absPath)
	seen := map[string]bool{}
	for i := range d.Tests {
		test := &d.Tests[i]
		if !safeName.MatchString(test.Scenario) || test.Scenario == "." || test.Scenario == ".." {
			return nil, fmt.Errorf("batch %s has invalid scenario name %q", path, test.Scenario)
		}
		if test.Name != "" && (!safeName.MatchString(test.Name) || test.Name == "." || test.Name == "..") {
			return nil, fmt.Errorf("batch %s has invalid test name %q", path, test.Name)
		}
		name := testName(*test)
		if seen[name] {
			return nil, fmt.Errorf("batch %s has duplicate test name %q; name each entry distinctly", path, name)
		}
		seen[name] = true
		if test.Target != "" && test.TargetCatalog == "" {
			return nil, fmt.Errorf("batch %s scenario %q selects a target without target_catalog", path, test.Scenario)
		}
		for name := range test.Params {
			if name == "" || strings.TrimSpace(name) != name || strings.Contains(name, "=") {
				return nil, fmt.Errorf("batch %s scenario %q has invalid parameter name %q", path, test.Scenario, name)
			}
		}
		for name, value := range test.PathParams {
			if name == "" || strings.TrimSpace(name) != name || strings.Contains(name, "=") {
				return nil, fmt.Errorf("batch %s scenario %q has invalid path parameter name %q", path, test.Scenario, name)
			}
			if _, exists := test.Params[name]; exists {
				return nil, fmt.Errorf("batch %s scenario %q binds parameter %q in both params and path_params", path, test.Scenario, name)
			}
			resolved, err := resolvePathBinding(batchDir, value)
			if err != nil {
				return nil, fmt.Errorf("batch %s scenario %q path_params.%s: %w", path, test.Scenario, name, err)
			}
			if test.Params == nil {
				test.Params = make(map[string]string)
			}
			test.Params[name] = resolved
			test.PathParams[name] = resolved
		}
		if test.InputBundlePath != "" {
			if test.InputBundle != "" {
				return nil, fmt.Errorf("batch %s scenario %q binds both input_bundle and input_bundle_path", path, test.Scenario)
			}
			resolved, err := resolvePathBinding(batchDir, test.InputBundlePath)
			if err != nil {
				return nil, fmt.Errorf("batch %s scenario %q input_bundle_path: %w", path, test.Scenario, err)
			}
			test.InputBundle = resolved
			test.InputBundlePath = resolved
		}
	}
	sum := sha256.Sum256(body)
	d.SHA256 = "sha256:" + hex.EncodeToString(sum[:])
	d.Raw = body
	return &d, nil
}

func resolvePathBinding(batchDir, value string) (string, error) {
	if strings.TrimSpace(value) == "" || !relative(value) {
		return "", fmt.Errorf("path %q must be a nonempty relative path", value)
	}
	return filepath.Join(batchDir, value), nil
}

// relative says whether a path is relative to the directory it is joined to.
// On Windows a path can be neither absolute nor relative: /media is rooted on
// the current drive and C:media is relative to that drive's working directory,
// and joining either to the batch directory would name somewhere it is not.
func relative(value string) bool {
	return !filepath.IsAbs(value) && filepath.VolumeName(value) == "" && !os.IsPathSeparator(value[0])
}

func testName(test Test) string {
	if test.Name != "" {
		return test.Name
	}
	return test.Scenario
}

type Invoker interface {
	Invoke(context.Context, io.Writer, io.Writer, ...string) int
}

type Options struct {
	Definition   *Definition
	RunsDir      string
	ScenarioRoot string
	Invoker      Invoker
	Progress     io.Writer
	ToolVersion  string
	ToolCommit   string
	Now          func() time.Time
}

type TestResult struct {
	Name     string `json:"name,omitempty"`
	Scenario string `json:"scenario"`
	Status   string `json:"status"`
	RunID    string `json:"run_id,omitempty"`
	ExitCode int    `json:"exit_code"`
	Log      string `json:"log,omitempty"`
}

type Result struct {
	Schema           int           `json:"schema"`
	ID               string        `json:"batch_id"`
	Dir              string        `json:"dir"`
	RunsDir          string        `json:"runs_dir"`
	Status           string        `json:"status"`
	ExitCode         int           `json:"exit_code"`
	DefinitionSHA256 string        `json:"definition_sha256"`
	ToolVersion      string        `json:"tool_version,omitempty"`
	ToolCommit       string        `json:"tool_commit,omitempty"`
	PathBindings     []PathBinding `json:"path_bindings,omitempty"`
	Tests            []TestResult  `json:"tests"`
}

// PathBinding records the absolute paths passed to an entry from relative bindings.
type PathBinding struct {
	Name        string            `json:"name"`
	Params      map[string]string `json:"params,omitempty"`
	InputBundle string            `json:"input_bundle,omitempty"`
}

// Run schedules all selected scenarios. A failed child does not suppress the
// others; cancellation prevents new children and interrupts those in flight.
func Run(ctx context.Context, opt Options) (Result, error) {
	if opt.Definition == nil || opt.Invoker == nil {
		return Result{}, errors.New("batch needs a definition and invoker")
	}
	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}
	id := fmt.Sprintf("%s-%s", opt.Definition.Name, now().UTC().Format("20060102T150405.000000000Z"))
	digest := sha256.Sum256([]byte(id))
	dir := filepath.Join(opt.RunsDir, "batches", id)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create batch record %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "batch.yaml"), opt.Definition.Raw, 0o644); err != nil {
		return Result{}, fmt.Errorf("snapshot batch definition: %w", err)
	}
	result := Result{
		Schema: Schema, ID: id, Dir: dir, RunsDir: opt.RunsDir, Status: "running",
		DefinitionSHA256: opt.Definition.SHA256, ToolVersion: opt.ToolVersion, ToolCommit: opt.ToolCommit,
		Tests: make([]TestResult, len(opt.Definition.Tests)),
	}
	for i, test := range opt.Definition.Tests {
		result.Tests[i] = TestResult{Name: testName(test), Scenario: test.Scenario, Status: "pending"}
		if len(test.PathParams) > 0 || test.InputBundlePath != "" {
			result.PathBindings = append(result.PathBindings, PathBinding{
				Name: testName(test), Params: maps.Clone(test.PathParams), InputBundle: test.InputBundlePath,
			})
		}
	}
	if err := save(dir, result); err != nil {
		return result, err
	}
	var mu sync.Mutex
	var progressMu sync.Mutex
	slots := make(chan struct{}, opt.Definition.Parallel)
	var wg sync.WaitGroup
	var recordErr error
schedule:
	for i, test := range opt.Definition.Tests {
		select {
		case <-ctx.Done():
			break schedule
		case slots <- struct{}{}:
		}
		if ctx.Err() != nil {
			<-slots
			break
		}
		mu.Lock()
		result.Tests[i].Status = "running"
		err := save(dir, result)
		if err != nil {
			result.Tests[i].Status = "pending"
			recordErr = errors.Join(recordErr, err)
		}
		mu.Unlock()
		if err != nil {
			<-slots
			break
		}
		wg.Add(1)
		go func(i int, test Test) {
			defer wg.Done()
			defer func() { <-slots }()
			child := invoke(ctx, opt, dir, test, fmt.Sprintf("batch-%x-%d", digest[:12], i+1), &progressMu)
			mu.Lock()
			result.Tests[i] = child
			if err := save(dir, result); err != nil {
				recordErr = errors.Join(recordErr, err)
			}
			mu.Unlock()
		}(i, test)
	}
	wg.Wait()
	for i := range result.Tests {
		if result.Tests[i].Status == "pending" {
			result.Tests[i].Status = "skipped"
		}
		if result.ExitCode == 0 && result.Tests[i].ExitCode != 0 {
			result.ExitCode = result.Tests[i].ExitCode
		}
	}
	if ctx.Err() != nil {
		result.Status, result.ExitCode = "interrupted", 12
	} else if result.ExitCode != 0 {
		result.Status = "failed"
	} else {
		result.Status = "measured"
	}
	return result, errors.Join(recordErr, save(dir, result))
}

func invoke(ctx context.Context, opt Options, dir string, test Test, suffix string, progressMu *sync.Mutex) TestResult {
	name := testName(test)
	result := TestResult{Name: name, Scenario: test.Scenario, Status: "failed", Log: name + ".log"}
	log, err := os.Create(filepath.Join(dir, result.Log))
	if err != nil {
		result.ExitCode = 10
		return result
	}
	defer func() { _ = log.Close() }()
	args := []string{
		"--runs-dir", opt.RunsDir, "--scenario-root", opt.ScenarioRoot,
		"run", test.Scenario, "--output", "json", "--progress", "plain", "--run-suffix", suffix,
	}
	args = append(args, entryArgs(test)...)
	if test.ReleaseFailedTargets {
		args = append(args, "--release-failed-targets")
	}
	var stdout bytes.Buffer
	stderr := io.Writer(log)
	if opt.Progress != nil {
		progress := &namedProgress{out: opt.Progress, mu: progressMu, name: name}
		defer progress.flush()
		stderr = io.MultiWriter(log, progress)
	}
	result.ExitCode = opt.Invoker.Invoke(ctx, &stdout, stderr, args...)
	_, _ = log.Write(stdout.Bytes())
	var response struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &response); err == nil {
		result.RunID = response.RunID
	} else if result.ExitCode == 0 {
		result.ExitCode = 10
		_, _ = fmt.Fprintf(log, "decode child run result: %v\n", err)
	}
	if result.ExitCode == 0 {
		result.Status = "measured"
	} else if ctx.Err() != nil {
		result.Status = "interrupted"
	}
	return result
}

func entryArgs(test Test) []string {
	var args []string
	for _, name := range slices.Sorted(maps.Keys(test.Params)) {
		args = append(args, "--param", name+"="+test.Params[name])
	}
	if test.Target != "" {
		args = append(args, "--target", test.Target)
	}
	if test.TargetCatalog != "" {
		args = append(args, "--target-catalog", test.TargetCatalog)
	}
	if test.InputBundle != "" {
		args = append(args, "--input-bundle", test.InputBundle)
	}
	if test.AWSProfile != "" {
		args = append(args, "--aws-profile", test.AWSProfile)
	}
	if test.AWSRegion != "" {
		args = append(args, "--aws-region", test.AWSRegion)
	}
	return args
}

type namedProgress struct {
	out     io.Writer
	mu      *sync.Mutex
	name    string
	pending []byte
}

func (p *namedProgress) Write(data []byte) (int, error) {
	p.pending = append(p.pending, data...)
	for {
		index := bytes.IndexByte(p.pending, '\n')
		if index < 0 {
			break
		}
		if err := p.line(p.pending[:index+1]); err != nil {
			return 0, err
		}
		p.pending = p.pending[index+1:]
	}
	return len(data), nil
}

func (p *namedProgress) flush() {
	if len(p.pending) > 0 {
		_ = p.line(p.pending)
		p.pending = nil
	}
}

func (p *namedProgress) line(line []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := fmt.Fprintf(p.out, "[%s] %s", p.name, line)
	return err
}

func save(dir string, result Result) error {
	file, err := os.CreateTemp(dir, ".batch-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err := json.NewEncoder(file).Encode(result); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "batch.json"))
}
