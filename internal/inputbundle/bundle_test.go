package inputbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jmurray2011/testease/internal/backend/private"
	"github.com/jmurray2011/testease/internal/scenario"
)

func TestAFileIsVerifiedStagedAndBoundByLogicalName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := filepath.Join(dir, "suite.tar")
	body := []byte("the exact suite bytes")
	if err := os.WriteFile(source, body, 0o644); err != nil {
		t.Fatal(err)
	}
	descriptor := filepath.Join(dir, "inputs.yaml")
	if err := os.WriteFile(descriptor, []byte(fmt.Sprintf(
		"schema: 1\ninputs:\n  suite:\n    path: suite.tar\n    sha256: %s\n", sha(body),
	)), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := LoadAndStage(descriptor, filepath.Join(dir, "runs"), map[string]scenario.Input{
		"suite": {Kind: "file"},
	})
	if err != nil {
		t.Fatalf("LoadAndStage: %v", err)
	}
	got := resolved["suite"]
	if got.Name != "suite" || got.SHA256 != sha(body) || got.DescriptorHash == "" {
		t.Errorf("resolved = %+v", got)
	}
	staged, err := os.ReadFile(got.StagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(staged) != string(body) {
		t.Errorf("staged bytes = %q", staged)
	}
	// The staged copy is the runner's alone, on any platform.
	if err := private.Verify(filepath.Dir(got.StagedPath), 0o700); err != nil {
		t.Errorf("input cache: %v", err)
	}
	if err := private.Verify(got.StagedPath, 0o400); err != nil {
		t.Errorf("staged input: %v", err)
	}

	plan := &scenario.Plan{
		Hosts: map[string]scenario.Host{"app": {Media: []string{"@suite:/input/suite.tar:ro"}}},
		Steps: []scenario.Step{{Label: "drive", Media: []string{"@suite:/suite.tar:ro"}}},
	}
	if err := Bind(plan, resolved); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	for _, mount := range []string{plan.Hosts["app"].Media[0], plan.Steps[0].Media[0]} {
		if !strings.HasPrefix(mount, got.StagedPath+":") || !strings.HasSuffix(mount, ":ro") {
			t.Errorf("bound mount = %q", mount)
		}
	}
}

func TestPublicObjectURLIsVerifiedStagedAndReusedFromCache(t *testing.T) {
	var requests atomic.Int32
	body := []byte("public RPM bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	dir := t.TempDir()
	descriptor := filepath.Join(dir, "inputs.yaml")
	if err := os.WriteFile(descriptor, []byte(fmt.Sprintf(
		"schema: 1\ninputs:\n  package: {url: %s/package.rpm, sha256: %s}\n", server.URL, sha(body),
	)), 0o644); err != nil {
		t.Fatal(err)
	}
	required := map[string]scenario.Input{"package": {Kind: "file"}}
	first, err := LoadAndStage(descriptor, filepath.Join(dir, "runs"), required)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(first["package"].StagedPath); err != nil || string(got) != string(body) {
		t.Fatalf("staged bytes = %q, error = %v", got, err)
	}
	server.Close()
	second, err := LoadAndStage(descriptor, filepath.Join(dir, "runs"), required)
	if err != nil {
		t.Fatalf("cached object should work offline: %v", err)
	}
	if second["package"].StagedPath != first["package"].StagedPath || requests.Load() != 1 {
		t.Fatalf("cache miss: first=%+v second=%+v requests=%d", first, second, requests.Load())
	}
}

func TestPublicObjectURLRejectsWrongDigestAndLeavesNoCachedFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "wrong bytes")
	}))
	defer server.Close()
	dir := t.TempDir()
	descriptor := filepath.Join(dir, "inputs.yaml")
	if err := os.WriteFile(descriptor, []byte(fmt.Sprintf(
		"schema: 1\ninputs:\n  package: {url: %s/package.rpm, sha256: %s}\n", server.URL, sha([]byte("expected bytes")),
	)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAndStage(descriptor, filepath.Join(dir, "runs"), map[string]scenario.Input{"package": {Kind: "file"}})
	if err == nil || !strings.Contains(err.Error(), "descriptor says") {
		t.Fatalf("error = %v, want a digest mismatch", err)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "runs", ".testease-inputs")); err != nil || len(entries) != 0 {
		t.Fatalf("cache after failed download = %v, error = %v", entries, err)
	}
}

func TestRelativeRunsDirectoryStagesAnAbsoluteMountPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	body := []byte("input bytes")
	source := filepath.Join(dir, "input.rpm")
	if err := os.WriteFile(source, body, 0o644); err != nil {
		t.Fatal(err)
	}
	descriptor := filepath.Join(dir, "inputs.yaml")
	if err := os.WriteFile(descriptor, []byte(fmt.Sprintf(
		"schema: 1\ninputs:\n  package: {path: input.rpm, sha256: %s}\n", sha(body),
	)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Under the working directory rather than relative to t.TempDir: on a
	// machine whose temporary directory is on another drive, as GitHub's
	// Windows runners have it, there is no relative path between the two.
	relativeRuns, err := os.MkdirTemp(".", ".testease-test-runs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(relativeRuns) })
	if filepath.IsAbs(relativeRuns) {
		t.Fatalf("runs directory %q is not relative, so this tests nothing", relativeRuns)
	}
	resolved, err := LoadAndStage(descriptor, relativeRuns, map[string]scenario.Input{
		"package": {Kind: "file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	staged := resolved["package"].StagedPath
	if !filepath.IsAbs(staged) {
		t.Fatalf("staged path %q must be absolute for a Docker bind mount", staged)
	}
	if got, err := os.ReadFile(staged); err != nil || string(got) != string(body) {
		t.Fatalf("staged bytes = %q, error = %v", got, err)
	}
}

func TestAnInputWhoseBytesDoNotMatchIsRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "suite.tar"), []byte("actual"), 0o644); err != nil {
		t.Fatal(err)
	}
	descriptor := filepath.Join(dir, "inputs.yaml")
	if err := os.WriteFile(descriptor, []byte(fmt.Sprintf(
		"schema: 1\ninputs:\n  suite: {path: suite.tar, sha256: %s}\n", sha([]byte("expected")),
	)), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadAndStage(descriptor, filepath.Join(dir, "runs"), map[string]scenario.Input{"suite": {Kind: "file"}})
	if err == nil || !strings.Contains(err.Error(), "descriptor says") {
		t.Fatalf("error = %v, want the actual and declared digests refused", err)
	}
}

func TestAGitArchiveMustCarryTheCommitTheDescriptorNames(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Test Ease")
	git(t, repo, "config", "user.email", "testease@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "file.txt")
	git(t, repo, "commit", "-q", "-m", "fixture")
	commit := gitOutput(t, repo, "rev-parse", "HEAD")
	archive := filepath.Join(dir, "source.tar")
	cmd := exec.Command("git", "-C", repo, "archive", "--format=tar", "-o", archive, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git archive: %v: %s", err, out)
	}
	body, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := filepath.Join(dir, "inputs.yaml")
	if err := os.WriteFile(descriptor, []byte(fmt.Sprintf(
		"schema: 1\ninputs:\n  source:\n    path: source.tar\n    sha256: %s\n    git_commit: %s\n", sha(body), commit,
	)), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := LoadAndStage(descriptor, filepath.Join(dir, "runs"), map[string]scenario.Input{
		"source": {Kind: "git-archive"},
	})
	if err != nil {
		t.Fatalf("LoadAndStage: %v", err)
	}
	if got := resolved["source"].GitCommit; got != commit {
		t.Errorf("commit = %q, want %q", got, commit)
	}
}

func sha(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	_ = gitOutput(t, repo, args...)
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
