package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestPublicationStageDoesNotTouchGit(t *testing.T) {
	repo := gitRepo(t)
	head := gitText(t, repo, "rev-parse", "HEAD")
	runID := "evidence-20260902T120000Z"
	writeTestFile(t, filepath.Join(repo, runID, "events.jsonl"), "{}\n")

	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	if err := recordPublication(cmd, repo, []string{runID}, false, false); err != nil {
		t.Fatalf("stage-only publication: %v", err)
	}

	if got := gitText(t, repo, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD changed from %s to %s", head, got)
	}
	if got := gitText(t, repo, "diff", "--cached", "--name-only"); got != "" {
		t.Errorf("stage-only publication changed the index: %s", got)
	}
}

func TestPublicationCommitContainsOnlyCopiedRecords(t *testing.T) {
	repo := gitRepo(t)
	runID := "evidence-20260902T120000Z"
	writeTestFile(t, filepath.Join(repo, runID, "events.jsonl"), "{}\n")
	writeTestFile(t, filepath.Join(repo, "somebody-elses-notes.txt"), "leave me alone\n")

	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	if err := recordPublication(cmd, repo, []string{runID}, true, false); err != nil {
		t.Fatalf("commit publication: %v", err)
	}

	if got := gitText(t, repo, "show", "--pretty=format:", "--name-only", "HEAD"); got != runID+"/events.jsonl" {
		t.Errorf("committed paths = %q, want only the copied record", got)
	}
	if got := gitText(t, repo, "status", "--short"); got != "?? somebody-elses-notes.txt" {
		t.Errorf("unrelated work was touched: %q", got)
	}
}

func TestPublicationCommitRefusesAnExistingStagedChange(t *testing.T) {
	repo := gitRepo(t)
	runID := "evidence-20260902T120000Z"
	writeTestFile(t, filepath.Join(repo, runID, "events.jsonl"), "{}\n")
	writeTestFile(t, filepath.Join(repo, "already-staged.txt"), "mine\n")
	gitRun(t, repo, "add", "already-staged.txt")
	head := gitText(t, repo, "rev-parse", "HEAD")

	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	err := recordPublication(cmd, repo, []string{runID}, true, false)
	if err == nil || !strings.Contains(err.Error(), "already has staged changes") {
		t.Fatalf("error = %v, want a refusal naming staged changes", err)
	}
	if got := gitText(t, repo, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD changed despite refusal: %s", got)
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "config", "user.name", "Test Ease")
	gitRun(t, repo, "config", "user.email", "testease@example.invalid")
	writeTestFile(t, filepath.Join(repo, ".gitignore"), "\n")
	gitRun(t, repo, "add", ".gitignore")
	gitRun(t, repo, "commit", "-q", "-m", "initial")
	return repo
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, repo string, args ...string) {
	t.Helper()
	full := append([]string{"-C", repo}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func gitText(t *testing.T, repo string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
