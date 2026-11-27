package runner_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/livebrowser"
	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/target"
)

// liveSession is a session that can be read from while it runs a script, the
// way docker exec and ssh channels can, and whose browser has taken a frame.
type liveSession struct {
	*fakeSession
	mu      sync.Mutex
	removed []string
}

func (s *liveSession) FetchesWhileRunning() bool { return true }

func (s *liveSession) Fetch(_ context.Context, remote string) (io.ReadCloser, error) {
	switch {
	case strings.HasSuffix(remote, "/"+livebrowser.Latest):
		return io.NopCloser(strings.NewReader("jpeg bytes")), nil
	case strings.HasSuffix(remote, "/"+livebrowser.LatestAbout):
		return io.NopCloser(strings.NewReader(`{"url":"https://app/login"}`)), nil
	}
	return nil, os.ErrNotExist
}

func (s *liveSession) Run(_ context.Context, cmd []string, _ target.RunOpts) (target.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, strings.Join(cmd, " "))
	return target.Result{}, nil
}

type liveTarget struct {
	*fakeTarget
	s target.Session
}

func (t *liveTarget) Session(context.Context) (target.Session, error) { return t.s, nil }

func TestAProbeThatDrivesPlaywrightOnItsHostIsWatchedFromHere(t *testing.T) {
	t.Parallel()

	p := plan(t, oneStep)
	browser := "#!/bin/bash\nnode -e \"require('playwright')\"\n"
	if err := os.WriteFile(filepath.Join(p.Dir, "probes", "one.sh"), []byte(browser), 0o755); err != nil {
		t.Fatal(err)
	}
	runs := t.TempDir()

	var env []string
	var during livebrowser.Frame
	var found bool
	s := &liveSession{fakeSession: &fakeSession{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		env = opt.Env
		// Long enough for the mirror to fetch once.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && !found {
			time.Sleep(100 * time.Millisecond)
			records, _ := filepath.Glob(filepath.Join(runs, "one-step-*"))
			if len(records) == 1 {
				during, found = livebrowser.Find(runs, records[0], 1)
			}
		}
		return target.Result{RC: 0}, nil
	}}}
	tgt := &liveTarget{fakeTarget: &fakeTarget{}, s: s}
	_, err := runner.Run(context.Background(), runner.Options{
		Plan:        p,
		RunsDir:     runs,
		Tool:        record.ToolRef{Version: "0.0.0-test"},
		Providers:   map[string]target.Provider{"fake": &captureSpecProvider{target: tgt}},
		LiveBrowser: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "NODE_OPTIONS=--require /tmp/testease-live-") || !strings.Contains(joined, livebrowser.DirEnv+"=/tmp/testease-live-") {
		t.Errorf("probe env = %v, want the hook loaded and its directory named", env)
	}
	if !strings.HasPrefix(s.putAt, "/tmp/testease-live-") || !strings.Contains(string(s.putBody), "testease live browser hook") {
		t.Errorf("put %q, want the hook on the host", s.putAt)
	}
	if !found || during.URL != "https://app/login" {
		t.Errorf("during the step: frame %+v found %v, want the host's frame mirrored here", during, found)
	}
	if left, _ := filepath.Glob(filepath.Join(runs, livebrowser.MirrorPattern)); len(left) != 0 {
		t.Errorf("mirrors left after the step: %v", left)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.removed) != 1 || !strings.HasPrefix(s.removed[0], "rm -rf -- /tmp/testease-live-") {
		t.Errorf("cleanup on the host = %v, want the hook and frames removed", s.removed)
	}
}

func TestAProbeThatDoesNotNamePlaywrightIsLeftAlone(t *testing.T) {
	t.Parallel()

	var env []string
	s := &liveSession{fakeSession: &fakeSession{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		env = opt.Env
		return target.Result{RC: 0}, nil
	}}}
	_, err := runner.Run(context.Background(), runner.Options{
		Plan:        plan(t, oneStep),
		RunsDir:     t.TempDir(),
		Tool:        record.ToolRef{Version: "0.0.0-test"},
		Providers:   map[string]target.Provider{"fake": &captureSpecProvider{target: &liveTarget{fakeTarget: &fakeTarget{}, s: s}}},
		LiveBrowser: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(strings.Join(env, " "), "NODE_OPTIONS") || s.putAt != "" {
		t.Errorf("env %v, put %q: want a probe that does not use Playwright untouched", env, s.putAt)
	}
}
