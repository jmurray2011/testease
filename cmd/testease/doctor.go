package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	backend "github.com/jmurray2011/testease/internal/backend/docker"
	ssmbackend "github.com/jmurray2011/testease/internal/backend/ssm"
	"github.com/jmurray2011/testease/internal/doctor"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/scenario"
)

func doctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor [scenario]",
		Short: "check what a run needs, before the run",
		Long: "Every check names the command that fixes it. Named with a scenario, it also\n" +
			"checks what that scenario asks for: its media, its images, and whether this\n" +
			"binary still ships the judge it declares.",
		Args: cobra.MaximumNArgs(1),
		RunE: doDoctor,
	}
	// The same default run uses: doctor answering about a different daemon
	// than the run dials is a clean bill about the wrong machine.
	cmd.Flags().String("docker-context", os.Getenv("TESTEASE_DOCKER_CONTEXT"),
		"docker context to ask, as in docker --context")
	cmd.Flags().String("win-docker-context", os.Getenv("TESTEASE_WIN_DOCKER_CONTEXT"),
		"docker context to ask for one-shot Windows cells; the ordinary docker context by default")
	addInstrumentFlags(cmd)
	cmd.Flags().StringArray("param", nil, "set a scenario parameter, k=v (repeatable)")
	addTargetSelectionFlags(cmd)
	cmd.Flags().StringArray("matrix", nil, "check every cell of an axis, k=v1,v2 (repeatable); without it a scenario's own axes are used")
	cmd.Flags().String("store", envOr("TESTEASE_STORE", ""),
		"artifact store that @store: axes are discovered from")
	cmd.Flags().String("input-bundle", os.Getenv("TESTEASE_INPUT_BUNDLE"),
		"descriptor binding the scenario's named, non-secret inputs to verified files")
	cmd.Flags().String("ssm-transfer-store", os.Getenv("TESTEASE_SSM_TRANSFER_STORE"),
		"temporary s3:// bucket or prefix used to stage large media onto SSM hosts")
	cmd.Flags().Bool("strict", false, "count a warning as a failure")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func doDoctor(cmd *cobra.Command, args []string) error {
	var scenarioName string
	if len(args) == 1 {
		scenarioName = args[0]
	}

	overrides, _, err := selectedParams(cmd)
	if err != nil {
		return err
	}
	var cells []map[string]string
	if scenarioName != "" {
		// Doctor preflights the same scope run will schedule. In particular, a
		// sparse crossing can name a different image and different media in
		// every cell; resolving one arbitrary cell would certify the wrong run.
		manifest, loadErr := scenario.Load(filepath.Join(flagString(cmd, "scenario-root"), scenarioName))
		if loadErr == nil {
			scope, scopeErr := scopeOfPinned(cmd, manifest, overrides)
			if scopeErr != nil {
				return &runner.PreconditionError{Message: scopeErr.Error()}
			}
			if scope != nil {
				scheduled, cellErr := scope.Cells()
				if cellErr != nil {
					return &runner.PreconditionError{Message: cellErr.Error()}
				}
				cells = make([]map[string]string, 0, len(scheduled))
				for _, cell := range scheduled {
					values := maps.Clone(overrides)
					for name, value := range cell.Params {
						values[name] = value
					}
					cells = append(cells, values)
				}
			}
		}
	}

	ssmTransferStore := flagString(cmd, "ssm-transfer-store")
	report := doctor.Run(cmd.Context(), doctor.Options{
		RunsDir:               flagString(cmd, "runs-dir"),
		ScenarioRoot:          flagString(cmd, "scenario-root"),
		Scenario:              scenarioName,
		Params:                overrides,
		Cells:                 cells,
		InputBundle:           flagString(cmd, "input-bundle"),
		SSMTransferStore:      ssmTransferStore,
		SSMTransferStoreValid: ssmbackend.ValidTransferStore(ssmTransferStore),
		SSMInlineMediaLimit:   ssmbackend.InlinePutLimit,
		Docker:                dockerFor(cmd),
		DockerContext:         flagString(cmd, "docker-context"),
		OneShotDocker:         windowsDockerFor(cmd),
		OneShotDockerContext:  windowsDockerContext(cmd),
		Backends:              capabilityTableFor(cmd),
	})

	if flagString(cmd, "output") == "json" {
		if err := writeJSON(cmd, report); err != nil {
			return err
		}
	} else {
		writeReport(cmd, report)
	}

	strict, _ := cmd.Flags().GetBool("strict")
	if report.Failed(strict) {
		return &runner.PreconditionError{
			Message: fmt.Sprintf("doctor: %s", report.Worst()),
			Remedy:  "the remedy for each check is beside it",
		}
	}
	return nil
}

func capabilityTableFor(cmd *cobra.Command) []doctor.Backend {
	table := capabilityTable()
	if instrument(cmd) == nil {
		return table
	}
	for i := range table {
		if table[i].Provider == "docker" || table[i].Provider == "static" {
			table[i].Offers = append(table[i].Offers, string(scenario.NeedsFingerprint))
		}
	}
	return table
}

func writeReport(cmd *cobra.Command, report doctor.Report) {
	out := cmd.OutOrStdout()
	for _, c := range report.Checks {
		_, _ = fmt.Fprintf(out, "%-10s %-13s %s\n", c.Status, c.Name, c.Found)
		if c.Remedy != "" && c.Status != doctor.OK {
			_, _ = fmt.Fprintf(out, "%-10s %-13s try: %s\n", "", "", c.Remedy)
		}
	}
}

// dockerDaemon asks a docker daemon the two questions a check needs.
type dockerDaemon struct{ client backend.Client }

func (d dockerDaemon) ServerVersion(ctx context.Context) (string, error) {
	return d.client.Output(ctx, "version", "--format", "{{.Server.Version}}")
}

// HasImage separates "this daemon does not have it" from "this daemon did not
// answer". Both come back from inspect as an error, and they are fixed by
// different people.
func (d dockerDaemon) HasImage(ctx context.Context, ref string) (bool, error) {
	_, err := d.client.Output(ctx, "image", "inspect", ref, "--format", "{{.Id}}")
	if err == nil {
		return true, nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "no such image") {
		return false, nil
	}
	return false, err
}

func (d dockerDaemon) ReadMount(ctx context.Context, source, image string) ([]byte, error) {
	name := fmt.Sprintf("testease-doctor-mount-%d-%d", os.Getpid(), time.Now().UnixNano())
	id, err := d.client.Output(ctx, "create", "--name", name, "-v", source+":/testease-canary:ro", image)
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := backend.CleanupContext(ctx)
		defer cancel()
		_, _ = d.client.Output(cleanup, "rm", "-f", strings.TrimSpace(id))
	}()

	packed, err := d.client.Output(ctx, "cp", strings.TrimSpace(id)+":/testease-canary", "-")
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(strings.NewReader(packed))
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("docker cp returned no mounted file")
		}
		if err != nil {
			return nil, fmt.Errorf("read docker cp output: %w", err)
		}
		if header.FileInfo().Mode().IsRegular() {
			return io.ReadAll(tr)
		}
	}
}

// MountType asks the daemon to resolve the same Linux bind source a run will
// use and says whether it is a file or directory. --mount refuses an absent
// source instead of creating an empty directory, which is the dangerous
// behavior of -v. Windows host media takes the other measured path in Launch:
// docker cp reads it from this machine before start.
func (d dockerDaemon) MountType(ctx context.Context, source, image string) (string, error) {
	if strings.EqualFold(d.client.ServerOS(ctx), backend.Windows) {
		info, err := os.Stat(source)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return "directory", nil
		}
		return "file", nil
	}

	mount := "type=bind,source=" + source + ",target=/testease-media,readonly"
	kind, err := d.client.Output(ctx, "run", "--rm", "--mount", mount,
		"--entrypoint", "/bin/sh", image, "-c",
		"if test -f /testease-media; then echo file; elif test -d /testease-media; then echo directory; else exit 1; fi")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(kind), nil
}

// CopyRoundTrip proves the transport used by remote drive steps before a
// target is started. The bytes cross into a stopped container and back through
// the selected Docker context; no shared host path is involved.
func (d dockerDaemon) CopyRoundTrip(ctx context.Context, image string) error {
	canary, err := os.CreateTemp("", ".testease-doctor-copy-*")
	if err != nil {
		return err
	}
	canaryPath := canary.Name()
	defer func() { _ = os.Remove(canaryPath) }()
	token := []byte(fmt.Sprintf("testease-copy-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if _, err := canary.Write(token); err != nil {
		_ = canary.Close()
		return err
	}
	if err := canary.Close(); err != nil {
		return err
	}

	name := fmt.Sprintf("testease-doctor-copy-%d-%d", os.Getpid(), time.Now().UnixNano())
	id, err := d.client.Output(ctx, "create", "--name", name, image)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := backend.CleanupContext(ctx)
		defer cancel()
		_, _ = d.client.Output(cleanup, "rm", "-f", strings.TrimSpace(id))
	}()
	if _, err := d.client.Output(ctx, "cp", canaryPath, strings.TrimSpace(id)+":/testease-canary"); err != nil {
		return err
	}
	packed, err := d.client.Output(ctx, "cp", strings.TrimSpace(id)+":/testease-canary", "-")
	if err != nil {
		return err
	}
	tr := tar.NewReader(strings.NewReader(packed))
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("docker cp returned no canary")
		}
		if err != nil {
			return fmt.Errorf("read docker cp output: %w", err)
		}
		if !header.FileInfo().Mode().IsRegular() {
			continue
		}
		seen, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		if !bytes.Equal(seen, token) {
			return fmt.Errorf("docker cp returned different bytes")
		}
		return nil
	}
}

// IsLocal answers the question the runner asks before it drives, through the
// same rule, so doctor refuses what the run would refuse rather than something
// nearly the same.
func (d dockerDaemon) IsLocal(ctx context.Context) (bool, string, error) {
	return daemonIsLocal(ctx, d.client)
}

func dockerFor(cmd *cobra.Command) doctor.Docker {
	return dockerDaemon{client: backend.Client{Context: flagString(cmd, "docker-context")}}
}

func windowsDockerFor(cmd *cobra.Command) doctor.Docker {
	return dockerDaemon{client: backend.Client{Context: windowsDockerContext(cmd)}}
}

// capabilityTable is `testease backends` in the shape doctor checks against.
// One table, two readers: the person who asked what a backend can do, and the
// check that answers it for them.
func capabilityTable() []doctor.Backend {
	kinds := backendKinds()
	out := make([]doctor.Backend, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, doctor.Backend{
			Provider:    k.Provider,
			Offers:      k.Capabilities,
			Conditional: k.Conditional,
		})
	}
	return out
}
