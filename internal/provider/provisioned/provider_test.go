package provisioned

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/target"
)

type mediaSession struct{ body []byte }

func (s *mediaSession) Run(context.Context, []string, target.RunOpts) (target.Result, error) {
	return target.Result{}, nil
}

func (s *mediaSession) RunScript(context.Context, []byte, []string, target.RunOpts) (target.Result, error) {
	return target.Result{}, nil
}

func (s *mediaSession) Put(_ context.Context, body io.Reader, _ string, _ fs.FileMode) error {
	s.body, _ = io.ReadAll(body)
	return nil
}

func (s *mediaSession) Fetch(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(&reader{body: s.body}), nil
}
func (s *mediaSession) Close() error { return nil }

type reader struct{ body []byte }

func (r *reader) Read(out []byte) (int, error) {
	if len(r.body) == 0 {
		return 0, io.EOF
	}
	n := copy(out, r.body)
	r.body = r.body[n:]
	return n, nil
}

func TestProvisionedTargetStagesAndVerifiesNamedMedia(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "fixture.bin")
	body := []byte("exact fixture bytes")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	session := &mediaSession{}
	p := New(Options{
		Driver: &fakePlatform{}, Name: "ec2", RunID: "run", Runner: "user@host",
		Dial: func(context.Context, Machine) (target.Session, error) { return session, nil },
	})
	tgt, err := p.Resolve(context.Background(), target.Spec{Host: "box", Image: "ami-fictional"})
	if err != nil {
		t.Fatal(err)
	}
	stager, ok := tgt.(target.MediaStager)
	if !ok {
		t.Fatal("provisioned EC2 target cannot stage declared media")
	}
	if err := stager.Stage(context.Background(), []target.Media{{
		Source: file, Dest: "/tmp/fixture.bin",
		SHA256: fmt.Sprintf("sha256:%x", sum),
	}}); err != nil {
		t.Fatal(err)
	}
	if string(session.body) != string(body) {
		t.Errorf("staged %q", session.body)
	}
}

type fakePlatform struct {
	created   Request
	waited    time.Duration
	destroyed string
}

type fakeOneShotPlatform struct{ fakePlatform }

func (f *fakeOneShotPlatform) RunOnce(_ context.Context, _ Machine, _ []byte, _ []string,
	_ target.RunOpts, created func(Machine) error,
) (target.Result, error) {
	if err := created(Machine{ID: "task-fictional", Image: "ecr-fictional", Labels: f.created.Labels}); err != nil {
		return target.Result{RC: -1}, err
	}
	return target.Result{RC: 0}, nil
}

func TestOneShotPlatformReportsCreatedTaskBeforeReturning(t *testing.T) {
	t.Parallel()
	driver := &fakeOneShotPlatform{}
	p := New(Options{Driver: driver, Name: "ecs", RunID: "run", Runner: "user@host"})
	tgt, err := p.Resolve(context.Background(), target.Spec{Host: "box", Image: "ecr-fictional"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tgt.(target.Sessioner); ok {
		t.Fatal("one-shot target advertised a session")
	}
	shot, ok := tgt.(target.OneShotter)
	if !ok {
		t.Fatal("one-shot target does not offer RunOnce")
	}
	var ref string
	tgt.(target.CreationReporter).SetOnCreated(func(id target.Identity) error {
		ref, _ = id.Resolved["ref"].(string)
		return nil
	})
	if _, err := shot.RunOnce(context.Background(), []byte("true\n"), nil, target.RunOpts{}); err != nil {
		t.Fatal(err)
	}
	if ref != "ecs:task-fictional" {
		t.Errorf("created task ref = %q", ref)
	}
}

func (f *fakePlatform) Create(_ context.Context, req Request) (Machine, error) {
	f.created = req
	return Machine{ID: "machine-fictional", Address: "machine-fictional", Image: req.Image, Labels: req.Labels}, nil
}

func (f *fakePlatform) WaitDialable(_ context.Context, _ Machine, budget time.Duration) error {
	f.waited = budget
	return nil
}

func (f *fakePlatform) List(context.Context, map[string]string) ([]Machine, error) { return nil, nil }

func (f *fakePlatform) Destroy(_ context.Context, m Machine) error { f.destroyed = m.ID; return nil }

func TestProvisionedProviderOwnsSharedLifecycle(t *testing.T) {
	t.Parallel()
	driver := &fakePlatform{}
	p := New(Options{
		Driver: driver, Name: "ec2", RunID: "run-fictional", Runner: "operator@workstation",
		Dial: func(context.Context, Machine) (target.Session, error) { return nil, nil },
	})
	tgt, err := p.Resolve(context.Background(), target.Spec{
		Host: "box", Provider: "ec2", Image: "ami-fictional", BootTimeout: 5 * time.Minute,
		Provision: map[string]string{"ec2.instance_type": "t3.small"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if driver.waited != 0 {
		t.Fatal("Resolve waited for dialability before returning the created machine")
	}
	if driver.created.Labels[target.LabelRun] != "run-fictional" || driver.created.Provision["ec2.instance_type"] != "t3.small" {
		t.Errorf("create request = %+v", driver.created)
	}
	if tgt.Identity().Resolved["ref"] != "ec2:machine-fictional" {
		t.Errorf("resolved identity = %+v", tgt.Identity())
	}
	if _, err := tgt.(target.Sessioner).Session(context.Background()); err != nil {
		t.Fatal(err)
	}
	if driver.waited != 5*time.Minute {
		t.Errorf("dialability budget = %s", driver.waited)
	}
	if err := p.Release(context.Background(), tgt, true); err != nil {
		t.Fatal(err)
	}
	if driver.destroyed != "" {
		t.Error("kept machine was destroyed")
	}
	if err := p.Release(context.Background(), tgt, false); err != nil {
		t.Fatal(err)
	}
	if driver.destroyed != "machine-fictional" {
		t.Errorf("destroyed = %q", driver.destroyed)
	}
}
