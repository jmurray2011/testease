package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/livebrowser"
	"github.com/jmurray2011/testease/internal/target"
)

// liveHookFile is the node hook a drive step loads when live browser is on.
// It saves what the suite's Playwright page shows about once a second; see
// package livebrowser for who reads the frames.
const liveHookFile = "hook.cjs"

//go:embed livebrowser.cjs
var liveHook []byte

// prepareLive puts the hook and the owner file in a drive step's workspace and
// sets the environment that loads it. It returns an error only for a
// workspace it could not write to; the step then runs without frames.
func prepareLive(workspace, recordDir string, seq int, label string, env map[string]string) error {
	dir := filepath.Join(workspace, livebrowser.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("make the live browser directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, liveHookFile), liveHook, 0o644); err != nil {
		return fmt.Errorf("write the live browser hook: %w", err)
	}
	abs, err := filepath.Abs(recordDir)
	if err != nil {
		return fmt.Errorf("locate the record for the live browser: %w", err)
	}
	owner, err := json.Marshal(livebrowser.Owner{Record: abs, Step: seq, Label: label})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, livebrowser.OwnerFile), owner, 0o644); err != nil {
		return fmt.Errorf("write the live browser owner: %w", err)
	}

	inside := ContainerWorkspace + "/" + livebrowser.Dir
	env[livebrowser.DirEnv] = inside
	// Added to whatever the step asked for, never in place of it.
	require := "--require " + inside + "/" + liveHookFile
	env["NODE_OPTIONS"] = strings.TrimSpace(env["NODE_OPTIONS"] + " " + require)
	return nil
}

// sessionLive watches the browser of a probe that runs on its host rather than
// beside it: the docker "browser" hosts that run node inline. The hook is put
// on the host, the probe's node loads it, and the newest frame is fetched back
// here about once a second into a directory the viewer finds by its owner.
//
// It applies only to a probe that names Playwright and a session that can
// fetch while a script runs. Anything that goes wrong leaves the step to run
// exactly as it would have, without frames.
type sessionLive struct {
	stop    context.CancelFunc
	done    chan struct{}
	mirror  string
	session target.Session
	remote  []string
}

func (r *run) startSessionLive(ctx context.Context, session target.Session, script []byte, seq int, label string, opts *target.RunOpts) *sessionLive {
	if !r.opt.LiveBrowser || !bytes.Contains(script, []byte("playwright")) {
		return nil
	}
	if f, ok := session.(target.LiveFetcher); !ok || !f.FetchesWhileRunning() {
		return nil
	}
	abs, err := filepath.Abs(r.dir)
	if err != nil {
		return nil
	}
	root, frames, err := livebrowser.Prepare(r.opt.RunsDir, livebrowser.Owner{Record: abs, Step: seq, Label: label})
	if err != nil {
		r.opt.Log.Warn("the live browser is off for this step", "step", seq, "error", err)
		return nil
	}
	var tag [6]byte
	_, _ = rand.Read(tag[:])
	name := fmt.Sprintf("/tmp/testease-live-%x-%d", tag, seq)
	hook := name + ".cjs"
	if err := session.Put(ctx, bytes.NewReader(liveHook), hook, 0o644); err != nil {
		_ = os.RemoveAll(root)
		r.opt.Log.Warn("the live browser is off for this step", "step", seq, "error", err)
		return nil
	}

	opts.Env = withNodeHook(opts.Env, name, hook)
	mirrorCtx, cancel := context.WithCancel(ctx)
	l := &sessionLive{stop: cancel, done: make(chan struct{}), mirror: root, session: session, remote: []string{hook, name}}
	fetch := func(ctx context.Context, file string) ([]byte, error) {
		rc, err := session.Fetch(ctx, name+"/"+file)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		return io.ReadAll(rc)
	}
	go func() {
		defer close(l.done)
		livebrowser.Mirror(mirrorCtx, fetch, frames)
	}()
	return l
}

// end stops fetching and takes the frames away, here and on the host: they
// were a view of the step, and the step is over.
func (l *sessionLive) end(ctx context.Context) {
	if l == nil {
		return
	}
	l.stop()
	<-l.done
	_ = os.RemoveAll(l.mirror)
	clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, _ = l.session.Run(clean, append([]string{"rm", "-rf", "--"}, l.remote...), target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
}

// withNodeHook is env with the hook loaded by any node it starts, added to a
// NODE_OPTIONS the step already sets rather than in place of it.
func withNodeHook(env []string, dir, hook string) []string {
	out := make([]string, 0, len(env)+2)
	node := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "NODE_OPTIONS="); ok {
			node = v
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		livebrowser.DirEnv+"="+dir,
		"NODE_OPTIONS="+strings.TrimSpace(node+" --require "+hook))
}

// remoteLive mirrors a remote drive's newest frame into its local workspace
// while it runs, where the viewer already looks for a drive's frames.
func remoteLive(workspace string) func(ctx context.Context, read func(ctx context.Context, path string) ([]byte, error)) {
	inside := ContainerWorkspace + "/" + livebrowser.Dir
	return func(ctx context.Context, read func(ctx context.Context, path string) ([]byte, error)) {
		livebrowser.Mirror(ctx, func(ctx context.Context, name string) ([]byte, error) {
			return read(ctx, inside+"/"+name)
		}, filepath.Join(workspace, livebrowser.Dir))
	}
}
