package docker

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jmurray2011/testease/internal/target"
)

// Windows is what a daemon calls itself when its containers are Windows ones.
const Windows = "windows"

// Launch is a container to bring up: what it is, what it is called, and what
// has to be inside it before it starts.
//
// How that happens is this package's business and not the caller's. The two
// platforms differ in four measured ways and every one of them is answered
// here, once, so that nothing upstream has to ask what kind of machine it is
// talking to.
type Launch struct {
	Name    string
	Image   string
	Network string

	// Alias is the name other hosts on the network reach this one by - the
	// name the scenario gave it.
	Alias string

	Labels map[string]string
	Env    map[string]string
	Media  []target.Media

	// Init says the host declared how it becomes ready, which means it runs
	// its own command rather than something to keep it alive.
	Init bool
}

// Up creates the container, puts what it needs inside, and starts it.
//
// The differences, all measured:
//
//   - A bind mount resolves on the DAEMON's host, so against a remote daemon it
//     cannot see a file on this machine. Windows media is copied in instead.
//   - Copy is refused once a Hyper-V container is running - "filesystem
//     operations against a running Hyper-V container are not supported" - so
//     the copy has to happen between create and start, which is the only window
//     there is.
//   - Windows containers have no privileged mode and refuse the flag outright,
//     failing a boot that would otherwise work. A Linux init system needs it.
//   - A destination is written container-root; docker cp refuses a C:\ form.
func (c Client) Up(ctx context.Context, l Launch) error {
	windows := c.windows(ctx)

	verb := "run"
	args := []string{"--name", l.Name, "--network", l.Network, "--network-alias", l.Alias}
	if windows {
		verb = "create"
	} else {
		args = append(args, "-d")
	}
	if l.Init && !windows {
		args = append(args, "--privileged")
	}
	for _, k := range slices.Sorted(maps.Keys(l.Labels)) {
		args = append(args, "--label", k+"="+l.Labels[k])
	}
	for _, k := range slices.Sorted(maps.Keys(l.Env)) {
		args = append(args, "-e", k+"="+l.Env[k])
	}
	if !windows {
		for _, m := range l.Media {
			mount := m.Source + ":" + m.Dest
			if m.ReadOnly {
				mount += ":ro"
			}
			args = append(args, "-v", mount)
		}
	}
	args = append(args, l.Image)

	// A host that declares how it becomes ready is running its own init system,
	// so its image keeps its own command. One that does not is a box we only
	// exec into, and it needs something to stay alive for - otherwise the image
	// command exits, the container dies, and the readiness wait spends its
	// whole budget on a corpse.
	if !l.Init {
		if windows {
			args = append(args, "ping", "-t", "localhost")
		} else {
			args = append(args, "sleep", "infinity")
		}
	}

	// THE EXIT STATUS IS CHECKED, and it has to be. A leftover container of the
	// same name makes this fail, and a caller that ignored it would talk
	// happily to the OLD container - so the run measures the previous run's
	// machine and says nothing.
	if _, err := c.Output(ctx, append([]string{verb}, args...)...); err != nil {
		// The commonest way to meet that leftover is a run this one continues:
		// an interrupted run keeps its targets deliberately, and a container's
		// name is a function of the run, the cell and the host, so a resume
		// asks for a name the kept box still holds. Naming the way out here
		// keeps it from reading as a daemon fault.
		if strings.Contains(err.Error(), "already in use") {
			// With the context, because reap looks in the current one and the
			// box is on whichever this run dialled. A remedy that omits it
			// sends the operator to a daemon with nothing to collect, which
			// reads like a clean one.
			return fmt.Errorf("%w\n  the container %s is still standing from an earlier attempt at this run; collect it with `testease reap --force%s` (it is removable only once the process that made it is gone) and resume again",
				err, l.Name, c.reapContext())
		}
		return err
	}

	if !windows {
		return nil
	}
	for _, m := range l.Media {
		if _, err := c.Output(ctx, "cp", m.Source, l.Name+":"+m.Dest); err != nil {
			return fmt.Errorf("stage %s: %w", m.Source, err)
		}
	}
	if _, err := c.Output(ctx, "start", l.Name); err != nil {
		return err
	}
	return nil
}

// EnsureNetwork creates a network if it is not already there.
//
// The driver is the daemon's rather than a constant: docker defaults to bridge,
// which a Windows daemon has no plugin for - it uses nat - so a network created
// without asking fails there.
func (c Client) EnsureNetwork(ctx context.Context, name string, labels map[string]string) error {
	args := []string{"network", "create"}
	if c.windows(ctx) {
		args = append(args, "--driver", "nat")
	}
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		args = append(args, "--label", k+"="+labels[k])
	}
	args = append(args, name)

	if _, err := c.Output(ctx, args...); err != nil {
		// One that already exists is this run's own, from a host that got here
		// first, or a resumed run reusing its name. Either way it is usable;
		// anything else is not.
		if _, inspect := c.Output(ctx, "network", "inspect", name, "--format", "{{.Id}}"); inspect != nil {
			return err
		}
	}
	return nil
}

// ReadyProbe is the weakest honest claim a host can make: that its execution
// surface answers at all. What that costs to ask differs - `true` is a Unix
// command and a Windows container has none - and a host that declares its own
// readiness never uses this.
func (c Client) ReadyProbe(ctx context.Context) []string {
	if c.windows(ctx) {
		return []string{"cmd", "/c", "exit 0"}
	}
	return []string{"true"}
}

// ContainerPath is a container-root destination as this platform spells it. The
// root of a Windows container is C:\, and docker cp will only take the first
// form.
func (c Client) ContainerPath(ctx context.Context, dest string) string {
	if !c.windows(ctx) {
		return dest
	}
	return `C:\` + strings.ReplaceAll(strings.TrimPrefix(dest, "/"), "/", `\`)
}

// windows is what the daemon runs. An answer it would not give is treated as
// linux rather than guessed at, which is the older and more forgiving path.
func (c Client) windows(ctx context.Context) bool {
	out, err := c.Output(ctx, "version", "--format", "{{.Server.Os}}")
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(out), Windows)
}

// ServerOS is what the daemon says it runs, for a caller that has to record it
// or hand it to a session.
func (c Client) ServerOS(ctx context.Context) string {
	out, err := c.Output(ctx, "version", "--format", "{{.Server.Os}}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
