package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/backend/filemode"
	"github.com/jmurray2011/testease/internal/target"
)

// DriveSpec is a container to run on this machine, beside the target rather
// than on it. It mirrors what the runner asks for; the field names are the
// contract between them and nothing else travels.
type DriveSpec struct {
	Image         string
	Args          []string
	Env           map[string]string
	Workspace     string
	Mount         string
	Scenario      string
	ScenarioMount string
	Media         []target.Media
	Network       string

	// Labels go onto the container, so one that outlives its client - a suite
	// the timeout killed the CLI of keeps driving the target - can be tied
	// back to the run that made it and reaped like any other leftover.
	Labels map[string]string

	// WhileRemote, when set, is called once a remote drive's container is
	// running, with a way to read a file inside it, and its context ends when
	// the container does. A local drive shares its workspace through the bind
	// mount and never calls it. The live browser view uses it to fetch frames
	// a remote drive could otherwise only hand back after it finished.
	WhileRemote func(ctx context.Context, read func(ctx context.Context, path string) ([]byte, error))
}

// Drive runs a container here and streams its output the same way a probe's is
// streamed. It is the same docker, dialled the same way - so a suite driving a
// target on another daemon still runs where the runner is, which is the point:
// the browser is on this side of the network and the product is on the other.
func (c Client) Drive(ctx context.Context, spec DriveSpec, opt target.RunOpts) (target.Result, error) {
	if spec.Image == "" {
		return target.Result{RC: -1}, fmt.Errorf("a drive step needs an image to run")
	}

	// Named, so a container the client stopped watching can still be found
	// and removed: killing `docker run` kills the client, not the container.
	name := fmt.Sprintf("testease-drive-%d", time.Now().UnixNano())
	args := []string{"run", "--rm", "-i", "--name", name}
	for _, k := range slices.Sorted(maps.Keys(spec.Labels)) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}

	// The target's own network. Each cell's hosts get one of their own, and
	// docker isolates user-defined bridges from the default one - so a
	// container that does not join it cannot route to the target at all.
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}

	// The container writes as whoever is running testease, so the files it
	// leaves in the workspace can be read afterwards without a root-owned
	// surprise.
	owner, _, _ := driveOwner(os.Getuid(), os.Getgid())
	args = append(args, owner...)
	args = append(args, "-e", "HOME=/tmp")

	if spec.Workspace != "" {
		mount := spec.Mount
		if mount == "" {
			mount = "/workspace"
		}
		args = append(args, "-v", spec.Workspace+":"+mount, "-w", mount)
	}
	if spec.Scenario != "" && spec.ScenarioMount != "" {
		// Read-only: the scenario is content this run reads, never something a
		// suite may edit on its way past.
		args = append(args, "-v", spec.Scenario+":"+spec.ScenarioMount+":ro")
	}
	for _, m := range spec.Media {
		mount := m.Source + ":" + m.Dest
		if m.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}
	for _, k := range slices.Sorted(maps.Keys(spec.Env)) {
		args = append(args, "-e", k+"="+spec.Env[k])
	}
	args = append(args, spec.Image)
	args = append(args, spec.Args...)

	if opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Timeout)
		defer cancel()
	}

	cmd := c.command(ctx, args...)
	cmd.Stdout = opt.Stdout
	cmd.Stderr = opt.Stderr

	started := time.Now()
	err := cmd.Run()
	result := target.Result{Duration: time.Since(started)}

	if err != nil {
		if ctx.Err() != nil {
			// Killing the CLI does not kill the container: left alone it keeps
			// driving the target after the step officially ended. End it now.
			clean, done := CleanupContext(ctx)
			_, _ = c.Output(clean, "rm", "-f", name)
			done()

			result.RC = -1
			if opt.Timeout > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				// Cut short rather than finished. An exit code from something
				// we killed is not a reading of what it would have returned.
				result.TimedOut = true
				return result, nil
			}
			// Cancelled is the person at the keyboard, not the suite overrunning.
			return result, ctx.Err()
		}

		rc, reported := codeOf(err, ctx.Err() != nil)
		if !reported || rc == 125 || rc == 126 || rc == 127 {
			// docker run's own failure - the image is not there, the daemon
			// refused, the entrypoint could not start. The suite never ran, so
			// there is no measured RC to record, and handing docker's code on
			// would turn an infrastructure failure into a product verdict.
			result.RC = -1
			return result, fmt.Errorf("docker could not run the drive container: %w", err)
		}
		result.RC = rc
		return result, nil
	}
	return result, nil
}

// driveOwner is who a drive container runs as, and who owns the files staged
// into it, given the runner's uid and gid.
//
// A platform without uids answers -1, as os.Getuid does on Windows. There is
// then no runner-owned uid to match: the container keeps its image's user, and
// staged files belong to root. Windows reports its files 0666 and directories
// 0777, so the archive leaves them writable by whichever user that is.
func driveOwner(uid, gid int) (flags []string, fileUID, fileGID int) {
	if uid < 0 || gid < 0 {
		return nil, 0, 0
	}
	return []string{"-u", fmt.Sprintf("%d:%d", uid, gid)}, uid, gid
}

// DriveRemote runs a drive container on a daemon that cannot bind the
// runner's filesystem. The scenario, empty result workspace, and declared
// media travel through the Docker API before the container starts, and the
// completed workspace travels back before it is removed.
//
// The copied scenario belongs to root while the workspace belongs to the
// unprivileged suite user. That preserves the local-drive contract: the suite
// can write evidence but cannot edit the question it is running.
func (c Client) DriveRemote(ctx context.Context, spec DriveSpec, opt target.RunOpts) (target.Result, error) {
	if spec.Image == "" {
		return target.Result{RC: -1}, fmt.Errorf("a drive step needs an image to run")
	}

	name := fmt.Sprintf("testease-drive-%d", time.Now().UnixNano())
	args := []string{"create", "--name", name}
	for _, k := range slices.Sorted(maps.Keys(spec.Labels)) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	owner, uid, gid := driveOwner(os.Getuid(), os.Getgid())
	args = append(args, owner...)
	args = append(args, "-e", "HOME=/tmp")
	for _, k := range slices.Sorted(maps.Keys(spec.Env)) {
		args = append(args, "-e", k+"="+spec.Env[k])
	}
	args = append(args, spec.Image)
	args = append(args, spec.Args...)

	if _, err := c.Output(ctx, args...); err != nil {
		return target.Result{RC: -1}, fmt.Errorf("docker could not create the remote drive container: %w", err)
	}
	defer func() {
		clean, done := CleanupContext(ctx)
		defer done()
		_, _ = c.Output(clean, "rm", "-f", name)
	}()

	if spec.Scenario != "" && spec.ScenarioMount != "" {
		if err := c.copyDirectoryIn(ctx, name, spec.Scenario, spec.ScenarioMount, 0, 0); err != nil {
			return target.Result{RC: -1}, fmt.Errorf("stage the scenario for the remote drive: %w", err)
		}
	}
	if spec.Workspace != "" {
		mount := spec.Mount
		if mount == "" {
			mount = "/workspace"
		}
		if err := c.copyDirectoryIn(ctx, name, spec.Workspace, mount, uid, gid); err != nil {
			return target.Result{RC: -1}, fmt.Errorf("stage the workspace for the remote drive: %w", err)
		}
	}
	for _, media := range spec.Media {
		if err := c.copyMediaIn(ctx, name, media, uid, gid); err != nil {
			return target.Result{RC: -1}, fmt.Errorf("stage drive media %s at %s: %w", media.Source, media.Dest, err)
		}
	}

	runCtx := ctx
	if opt.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, opt.Timeout)
		defer cancel()
	}
	cmd := c.command(runCtx, "start", "-a", name)
	cmd.Stdout = opt.Stdout
	cmd.Stderr = opt.Stderr

	started := time.Now()
	stopLive := c.whileRemote(ctx, name, spec)
	runErr := cmd.Run()
	stopLive()
	result := target.Result{Duration: time.Since(started)}

	if runCtx.Err() != nil {
		// Killing the CLI does not stop the remote container. Stop it but keep
		// its writable layer until the evidence below has crossed back.
		clean, done := CleanupContext(ctx)
		_, _ = c.Output(clean, "kill", name)
		done()
	}

	if spec.Workspace != "" {
		mount := spec.Mount
		if mount == "" {
			mount = "/workspace"
		}
		clean, done := CleanupContext(ctx)
		_, copyErr := c.Output(clean, "cp", name+":"+strings.TrimRight(mount, "/")+"/.", spec.Workspace)
		done()
		if copyErr != nil {
			return target.Result{RC: -1, Duration: result.Duration}, fmt.Errorf("retrieve the remote drive workspace: %w", copyErr)
		}
	}

	if runErr != nil {
		if runCtx.Err() != nil {
			result.RC = -1
			if opt.Timeout > 0 && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				result.TimedOut = true
				return result, nil
			}
			return result, runCtx.Err()
		}

		rc, reported := codeOf(runErr, runCtx.Err() != nil)
		if !reported || rc == 125 || rc == 126 || rc == 127 {
			result.RC = -1
			return result, fmt.Errorf("docker could not start the remote drive container: %w", runErr)
		}
		result.RC = rc
		return result, nil
	}
	return result, nil
}

// copyMediaIn gives a remote drive the same bytes a local drive bind-mounts.
// A path on the runner has no meaning to a daemon reached through a Docker
// context; passing it as -v can silently mount an unrelated empty directory on
// that machine. The archive crosses the selected Docker transport instead.
func (c Client) copyMediaIn(ctx context.Context, container string, media target.Media, uid, gid int) error {
	archive, err := mediaArchive(media.Source, media.Dest, uid, gid, media.ReadOnly)
	if err != nil {
		return err
	}
	cmd := c.command(ctx, "cp", "-", container+":/")
	cmd.Stdin = bytes.NewReader(archive)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker cp: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// copyDirectoryIn writes one local directory at an exact absolute path in a
// stopped container. Streaming a tar archive through docker cp is what makes
// this work through SSH-backed Docker contexts without a shared filesystem.
func (c Client) copyDirectoryIn(ctx context.Context, container, source, destination string, uid, gid int) error {
	archive, err := directoryArchive(source, destination, uid, gid)
	if err != nil {
		return err
	}
	cmd := c.command(ctx, "cp", "-", container+":/")
	cmd.Stdin = bytes.NewReader(archive)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker cp: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

func directoryArchive(source, destination string, uid, gid int) ([]byte, error) {
	return pathArchive(source, destination, uid, gid, false, true)
}

// mediaArchive maps either a file or a directory to its exact container path.
// Read-only media is copied without write bits: a remote daemon cannot provide
// a real bind-mounted read-only filesystem, but the suite still cannot modify
// ordinary media through its unprivileged uid.
func mediaArchive(source, destination string, uid, gid int, readOnly bool) ([]byte, error) {
	return pathArchive(source, destination, uid, gid, readOnly, false)
}

func pathArchive(source, destination string, uid, gid int, readOnly, requireDirectory bool) ([]byte, error) {
	root, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if requireDirectory && !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", source)
	}
	// A path in a Linux container, whatever the runner is: path, not filepath.
	containerPath := path.Clean(destination)
	if !strings.HasPrefix(containerPath, "/") {
		return nil, fmt.Errorf("destination %q is not an absolute container path", destination)
	}
	prefix := strings.TrimPrefix(containerPath, "/")
	if prefix == "" || prefix == "." || prefix == ".." || strings.HasPrefix(prefix, "../") {
		return nil, fmt.Errorf("destination %q is not an absolute container path", destination)
	}

	var body bytes.Buffer
	tw := tar.NewWriter(&body)
	err = filepath.Walk(root, func(current string, entry os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		name := prefix
		if info.IsDir() && rel != "." {
			name = path.Join(prefix, filepath.ToSlash(rel))
		}
		link := ""
		if entry.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(current)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(entry, link)
		if err != nil {
			return err
		}
		header.Name = name
		header.Uid = uid
		header.Gid = gid
		if header.Mode&0o111 == 0 && filemode.Executable(current, entry) {
			// A runner without an executable bit still meant this to run.
			header.Mode |= 0o111
		}
		if readOnly {
			header.Mode &^= 0o222
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !entry.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(current)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		_ = tw.Close()
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

// whileRemote hands a remote drive's running container to spec.WhileRemote,
// so a caller can read files from it before the workspace crosses back. The
// returned stop ends that and waits for it.
func (c Client) whileRemote(ctx context.Context, name string, spec DriveSpec) (stop func()) {
	if spec.WhileRemote == nil {
		return func() {}
	}
	read := func(ctx context.Context, file string) ([]byte, error) {
		// Not Output, which trims the trailing newline a binary file may end in.
		cmd := c.command(ctx, "exec", name, "cat", file)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	watchCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		spec.WhileRemote(watchCtx, read)
	}()
	return func() {
		cancel()
		<-done
	}
}
