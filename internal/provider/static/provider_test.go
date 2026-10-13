package static_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/provider/static"
	"github.com/jmurray2011/testease/internal/target"
)

// A static host is one that was there before the run and will be there after
// it. What it needs from a scenario is which machine; what it needs from the
// caller is the way in, and the two must never swap places.

func TestAHostThatNamesNoMachine(t *testing.T) {
	t.Parallel()

	p := static.New(static.Options{})
	_, err := p.Resolve(context.Background(), target.Spec{Host: "box", Backend: "ssm"})
	if err == nil {
		t.Fatal("resolved a host that named no address")
	}
	if !strings.Contains(err.Error(), "box") {
		t.Errorf("error %q does not name the host", err)
	}
}

func TestABackendTestEaseDoesNotDial(t *testing.T) {
	t.Parallel()

	p := static.New(static.Options{})
	_, err := p.Resolve(context.Background(), target.Spec{
		Host: "box", Backend: "carrier-pigeon", Address: "somewhere",
	})
	if err == nil {
		t.Fatal("resolved a host asking for a backend that does not exist")
	}
	// The refusal says what there is, so somebody can act on it rather than
	// guess at the spelling.
	for _, want := range []string{"carrier-pigeon", "ssm", "winrm"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestAnSSMHostWithNoProfileIsRefusedRatherThanGuessed(t *testing.T) {
	t.Parallel()

	// Ten profiles on one machine is normal, and picking one silently is how a
	// command lands in an account nobody meant.
	p := static.New(static.Options{})
	_, err := p.Resolve(context.Background(), target.Spec{
		Host: "box", Backend: "ssm", Address: "i-0123456789abcdef0",
	})
	if err == nil {
		t.Fatal("dialled Systems Manager without being told which account")
	}
	if !strings.Contains(err.Error(), "profile") {
		t.Errorf("error %q does not say what is missing", err)
	}
}

func TestReleaseLetsGoOfNothing(t *testing.T) {
	t.Parallel()

	// The machine was there before this run. Removing it is not testease's to
	// do, and neither is keeping it.
	if err := static.New(static.Options{}).Release(context.Background(), nil, false); err != nil {
		t.Errorf("Release: %v", err)
	}
}
