package runner_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/target"
)

type sessionTarget struct{ session target.Session }

func (t sessionTarget) Identity() target.Identity { return target.Identity{} }
func (t sessionTarget) Labels() map[string]string { return map[string]string{} }
func (t sessionTarget) Session(context.Context) (target.Session, error) {
	return t.session, nil
}

type fetchErrorSession struct{ *fakeSession }

func (s fetchErrorSession) Fetch(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("source disappeared")
}

type receiveErrorTarget struct{ *shotTarget }

func (t receiveErrorTarget) ReceiveCarry(context.Context, io.Reader, string) (target.CarryReceipt, error) {
	return target.CarryReceipt{}, errors.New("destination refused bytes")
}

type deadlineFetchSession struct{ *fakeSession }

func (s deadlineFetchSession) Fetch(ctx context.Context, _ string) (io.ReadCloser, error) {
	reader, writer := io.Pipe()
	go func() {
		<-ctx.Done()
		_ = writer.CloseWithError(ctx.Err())
	}()
	return reader, nil
}

type copyingSession struct{ *fakeSession }

func (s copyingSession) Put(_ context.Context, body io.Reader, remote string, _ fs.FileMode) error {
	s.putAt = remote
	var err error
	s.putBody, err = io.ReadAll(body)
	return err
}

func TestCarryTransferFailuresAreInfrastructureEvidenceWithoutIdentityFacts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		manifest    string
		source      target.Target
		destination target.Target
	}{
		{
			name:        "fetch",
			source:      sessionTarget{session: fetchErrorSession{fakeSession: &fakeSession{run: exits(0, "made\n")}}},
			destination: sessionTarget{session: &fakeSession{run: exits(0, "inspect\n")}},
		},
		{
			name:        "receive",
			source:      sessionTarget{session: &fakeSession{run: exits(0, "made\n")}},
			destination: receiveErrorTarget{shotTarget: &shotTarget{}},
		},
		{
			name:        "deadline",
			manifest:    "    timeout: 1ms\n",
			source:      sessionTarget{session: deadlineFetchSession{fakeSession: &fakeSession{run: exits(0, "made\n")}}},
			destination: sessionTarget{session: copyingSession{fakeSession: &fakeSession{run: exits(0, "inspect\n")}}},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifest := carryScenario
			if test.manifest != "" {
				manifest = replaceCarryExpectation(manifest, test.manifest)
			}
			out, err := runner.Run(context.Background(), runner.Options{
				Plan: plan(t, manifest), RunsDir: t.TempDir(),
				Tool: record.ToolRef{Version: "0.0.0-test"},
				Providers: map[string]target.Provider{
					"source-provider":      fixedProvider{target: test.source},
					"destination-provider": fixedProvider{target: test.destination},
				},
			})
			if err != nil {
				t.Fatalf("Run returned a precondition error for an execution failure: %v", err)
			}
			if out.Status != record.StatusInfraFail {
				t.Fatalf("status = %s, want infra-fail", out.Status)
			}
			rec, readErr := record.Read(out.Dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, ok := factIn(t, rec, "carry.sha256"); ok {
				t.Fatal("a failed transfer recorded a successful carry identity")
			}
		})
	}
}

func replaceCarryExpectation(manifest, insertion string) string {
	const marker = "    expect: {rc: 0}\n"
	return strings.Replace(manifest, marker, insertion+marker, 1)
}
