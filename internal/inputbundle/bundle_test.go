package inputbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
