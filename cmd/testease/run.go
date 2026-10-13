package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	backend "github.com/jmurray2011/testease/internal/backend/docker"
	"github.com/jmurray2011/testease/internal/inputbundle"
	dockerprovider "github.com/jmurray2011/testease/internal/provider/docker"
	endpointprovider "github.com/jmurray2011/testease/internal/provider/endpoint"
	hypervprovider "github.com/jmurray2011/testease/internal/provider/hyperv"
	statefileprovider "github.com/jmurray2011/testease/internal/provider/statefile"
	"github.com/jmurray2011/testease/internal/provider/static"
	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/scenario"
	"github.com/jmurray2011/testease/internal/store"
	"github.com/jmurray2011/testease/internal/target"
	"github.com/jmurray2011/testease/internal/targetcatalog"
)

func runCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <scenario>",
		Short: "run a scenario against its targets and write a record",
		Args:  cobra.ExactArgs(1),
		RunE:  doRun,
	}
	cmd.Flags().StringArray("param", nil, "set a scenario parameter, k=v (repeatable)")
	addTargetSelectionFlags(cmd)
	cmd.Flags().StringArray("matrix", nil, "sweep an axis, k=v1,v2 (repeatable); without it a scenario's own axes are used")
	cmd.Flags().Int("par", 1, "how many cells of a census run at once")
	cmd.Flags().String("resume", "", "run id of a census to continue; reruns every cell of it that is not measured")
	cmd.Flags().Bool("keep", false, "leave targets standing after a clean run")
	cmd.Flags().Bool("release-failed-targets", false, "release disposable targets after failed cells instead of retaining them for inspection")
	cmd.Flags().String("docker-context", os.Getenv("TESTEASE_DOCKER_CONTEXT"),
		"docker context to dial, as in docker --context")
	cmd.Flags().String("win-docker-context", os.Getenv("TESTEASE_WIN_DOCKER_CONTEXT"),
		"docker context for one-shot Windows cells; the ordinary docker context by default")
	addInstrumentFlags(cmd)
	cmd.Flags().String("win-memory", envOr("TESTEASE_WIN_MEMORY", "6g"),
		"memory each one-shot cell gets; Windows containers are not small")
	cmd.Flags().String("store", envOr("TESTEASE_STORE", ""),
		"artifact store that @store: axes are discovered from")
	cmd.Flags().String("aws-profile", os.Getenv("TESTEASE_AWS_PROFILE"),
		"aws profile for hosts dialled over Systems Manager; there is no default")
	cmd.Flags().String("aws-region", os.Getenv("TESTEASE_AWS_REGION"), "aws region for those hosts")
	cmd.Flags().String("ssm-transfer-store", os.Getenv("TESTEASE_SSM_TRANSFER_STORE"),
		"temporary s3:// bucket or prefix used to stage large media onto SSM hosts")
	cmd.Flags().String("statefile-dir", envOr("TESTEASE_STATEFILE_DIR", ""),
		"where an external provisioner keeps its state files")
	cmd.Flags().String("winrm-user", os.Getenv("TESTEASE_WINRM_USER"), "user for hosts dialled over WinRM")
	cmd.Flags().String("winrm-password-file", os.Getenv("TESTEASE_WINRM_PASSWORD_FILE"),
		"file holding that password; never put one in a scenario")
	cmd.Flags().Bool("winrm-https", false, "dial WinRM over TLS on 5986 rather than encrypted HTTP on 5985")
	cmd.Flags().String("ssh-user", os.Getenv("TESTEASE_SSH_USER"), "user for hosts dialled over SSH")
	cmd.Flags().String("ssh-password-file", os.Getenv("TESTEASE_SSH_PASSWORD_FILE"),
		"file holding that password; never put one in a scenario")
	cmd.Flags().String("ssh-key", os.Getenv("TESTEASE_SSH_KEY"), "private key for hosts dialled over SSH")
	cmd.Flags().Int("ssh-port", 22, "port for hosts dialled over SSH")
	cmd.Flags().Bool("ssh-sudo", false,
		"run SSH readiness checks and probes through noninteractive sudo; the target identity records this elevation")
	cmd.Flags().String("ssh-known-hosts", os.Getenv("TESTEASE_SSH_KNOWN_HOSTS"),
		"known_hosts file to verify SSH host keys against; without it the key is recorded, not verified")
	cmd.Flags().String("env-file", os.Getenv("TESTEASE_ENV_FILE"),
		"file of KEY=VALUE lines injected into drive steps; never recorded, so never put one in a scenario")
	cmd.Flags().String("input-bundle", os.Getenv("TESTEASE_INPUT_BUNDLE"),
		"descriptor binding the scenario's named, non-secret inputs to verified files")
	cmd.Flags().String("progress", "auto", "live progress on stderr: auto, plain, json or off")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func addInstrumentFlags(cmd *cobra.Command) {
	cmd.Flags().String("instrument-collect", os.Getenv("TESTEASE_INSTRUMENT_COLLECT"),
		"command run on the target to take a fingerprint reading; without it a target cannot fingerprint")
	cmd.Flags().String("instrument-diff", os.Getenv("TESTEASE_INSTRUMENT_DIFF"),
		"command run here to compare two readings; exits 0 unchanged, 1 changed")
	cmd.Flags().String("instrument-name", envOr("TESTEASE_INSTRUMENT_NAME", "instrument@0"),
		"what to record the instrument as")
}

// driveSecrets reads the env file a drive step's container needs.
//
// KEY=VALUE a line, `#` comments, blanks ignored, surrounding quotes stripped -
// enough to point straight at a suite's own .env rather than transcribing it.
// The values are injected and never recorded, which is the whole reason this is
// a file and not a --param.
func driveSecrets(cmd *cobra.Command) (map[string]string, error) {
	path := flagString(cmd, "env-file")
	if path == "" {
		return nil, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read --env-file: %w", err)
	}

	out := map[string]string{}
	for i, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("--env-file %s line %d is not KEY=VALUE", path, i+1)
		}
		v = strings.TrimSpace(v)
		if len(v) > 1 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, nil
}

func doRun(cmd *cobra.Command, args []string) error {
	stop, err := withSignals(cmd)
	if err != nil {
		return err
	}
	defer stop()

	log, err := logger(cmd)
	if err != nil {
		return err
	}

	params, selection, err := selectedParams(cmd)
	if err != nil {
		return err
	}

	dir := filepath.Join(flagString(cmd, "scenario-root"), args[0])
	manifest, err := scenario.Load(dir)
	if err != nil {
		return &runner.PreconditionError{
			Message: err.Error(),
			Remedy:  fmt.Sprintf("testease scenarios --scenario-root %s", flagString(cmd, "scenario-root")),
		}
	}
	scope, err := scopeOfPinned(cmd, manifest, params)
	if err != nil {
		return &runner.PreconditionError{Message: err.Error()}
	}

	// A census resolves the scenario once per cell, so there is no single plan
	// to make here - and a resume is always a census, whose scope comes from
	// the record it continues rather than from anything on this command line.
	var plan *scenario.Plan
	if scope == nil && flagString(cmd, "resume") == "" {
		if plan, err = manifest.Resolve(params); err != nil {
			return &runner.PreconditionError{Message: err.Error()}
		}
	}

	// Named here rather than inside the runner, because the containers this
	// run creates are labelled with it: reap has to be able to tie a target
	// back to the run that made it, and two runs of one scenario must not
	// collide on a container name.
	runID := flagString(cmd, "resume")
	if runID == "" {
		runID = record.RunID(manifest.Name, time.Now())
	}
	log = log.With("run", runID)

	keep, _ := cmd.Flags().GetBool("keep")
	releaseFailedTargets, _ := cmd.Flags().GetBool("release-failed-targets")
	if keep && releaseFailedTargets {
		return &runner.PreconditionError{Message: "--keep and --release-failed-targets ask for opposite target lifecycles"}
	}
	runsDir := flagString(cmd, "runs-dir")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return fmt.Errorf("make room for records: %w", err)
	}
	inputs, err := inputbundle.LoadAndStage(flagString(cmd, "input-bundle"), runsDir, manifest.Inputs)
	if err != nil {
		return &runner.PreconditionError{Message: err.Error(), Remedy: "testease doctor " + args[0] + " --input-bundle <descriptor>"}
	}

	client, windowsClient := dockerClients(cmd, log)
	printInstrument := instrument(cmd)
	docker := dockerprovider.New(dockerprovider.Options{
		Client:     client,
		RunID:      runID,
		Runner:     runnerName(),
		Instrument: printInstrument,
		Notices:    cmd.ErrOrStderr(),
		Log:        log,
	})

	par, _ := cmd.Flags().GetInt("par")
	oneShot := hypervprovider.New(hypervprovider.Options{
		Client:      windowsClient,
		RunID:       runID,
		Runner:      runnerName(),
		ScratchRoot: filepath.Join(runsDir, runtimeWorkDir, runID),
		Memory:      flagString(cmd, "win-memory"),
		Log:         log,
	})
	stat, err := staticProvider(cmd, runID, log, printInstrument)
	if err != nil {
		return err
	}
	state := statefileprovider.New(statefileprovider.Options{
		Dir:     flagString(cmd, "statefile-dir"),
		Profile: flagString(cmd, "aws-profile"),
		Region:  flagString(cmd, "aws-region"),
		RunID:   runID,
		Runner:  runnerName(),
		Log:     log,
	})
	endpoint := endpointprovider.New(endpointprovider.Options{
		RunID: runID, Runner: runnerName(),
	})

	secrets, err := driveSecrets(cmd)
	if err != nil {
		return &runner.PreconditionError{Message: err.Error()}
	}
	progress, err := runProgress(cmd)
	if err != nil {
		return err
	}
	defer progress.Close()

	outcome, runErr := runner.Run(cmd.Context(), runner.Options{
		Plan:                 plan,
		Manifest:             manifest,
		Scope:                scope,
		Params:               params,
		Parallel:             par,
		Resume:               flagString(cmd, "resume"),
		RunID:                runID,
		RunsDir:              runsDir,
		Tool:                 record.ToolRef{Version: version, Commit: commit},
		Measurement:          measurementConditions(cmd, selection),
		Inputs:               inputs,
		Keep:                 keep,
		ReleaseFailedTargets: releaseFailedTargets,
		Providers:            providers(docker, oneShot, stat, state, endpoint),
		Driver:               dockerDriver{client: client},
		DriveSecrets:         secrets,
		Observe:              progress.Observe,
		Log:                  log,
	})
	if outcome == nil {
		return runErr
	}

	if err := report(cmd, manifest, outcome, runErr); err != nil {
		return err
	}
	remedy := ""
	if outcome.Status == record.StatusInfraFail {
		remedy = fmt.Sprintf("testease doctor %s --scenario-root %s", args[0], flagString(cmd, "scenario-root"))
	}
	return runErrorWithRemedy(outcome, runErr, remedy)
}

func runProgress(cmd *cobra.Command) (*progressReporter, error) {
	mode := strings.ToLower(flagString(cmd, "progress"))
	if mode == "auto" {
		if strings.EqualFold(flagString(cmd, "output"), "json") {
			mode = "json"
		} else {
			mode = "plain"
		}
	}
	switch mode {
	case "plain", "json", "off":
	default:
		return nil, fmt.Errorf("--progress is %q; there is auto, plain, json and off", mode)
	}
	return newProgressReporter(cmd.ErrOrStderr(), mode, 30*time.Second), nil
}

// report says where the evidence is. It is not the record and never becomes
// one: a rendering is produced on demand from the events.
func report(cmd *cobra.Command, manifest *scenario.Manifest, outcome *runner.Outcome, runErr error) error {
	if strings.EqualFold(flagString(cmd, "output"), "json") {
		payload := map[string]any{
			"run_id":   outcome.RunID,
			"dir":      outcome.Dir,
			"status":   outcome.Status,
			"total":    outcome.Total,
			"measured": outcome.Measured,
		}
		if runErr != nil {
			payload["error"] = runErr.Error()
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			return fmt.Errorf("write json: %w", err)
		}
		return nil
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", outcome.RunID)
	unit := "steps"
	if outcome.Cells != nil {
		unit = "cells"
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%d of %d %s measured; record at %s\n",
		outcome.Measured, outcome.Total, unit, outcome.Dir)
	// MEASURED IS NOT MET. A failed expectation does not stop a cell, so this
	// line is true of a record whose expectations were all unmet - and naming
	// the judge the scenario itself declared decides nothing here, it says
	// which command does.
	if declared := declaredJudgeOf(manifest); declared != "" && (runErr == nil || outcome.Measured > 0) {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"this record declares %s and running does not judge: testease judge %s\n",
			declared, outcome.Dir)
	}
	return nil
}

// declaredJudgeOf is the judge the manifest named, or empty when it named none.
func declaredJudgeOf(m *scenario.Manifest) string {
	if m == nil || m.Judge == nil || m.Judge.Name == "" {
		return ""
	}
	if m.Judge.Version == "" {
		return m.Judge.Name
	}
	return m.Judge.Name + "@" + m.Judge.Version
}

func scopeOfPinned(cmd *cobra.Command, manifest *scenario.Manifest, pinned map[string]string) (*runner.Scope, error) {
	if flagString(cmd, "resume") != "" {
		return nil, nil
	}

	pairs, err := cmd.Flags().GetStringArray("matrix")
	if err != nil {
		return nil, err
	}
	if len(pairs) > 0 {
		axes := map[string][]string{}
		for _, p := range pairs {
			k, v, ok := strings.Cut(p, "=")
			if !ok || k == "" {
				return nil, fmt.Errorf("--matrix %q is not k=v1,v2", p)
			}
			values := strings.Split(v, ",")
			for i := range values {
				values[i] = strings.TrimSpace(values[i])
			}
			axes[k] = values
		}
		scope, err := resolveAxes(cmd, axes, "cli")
		if err != nil {
			return nil, err
		}
		scope.Exclude = manifest.Matrix.Exclude
		return scope, nil
	}

	axes, err := axesToSweep(manifest, pinned)
	if err != nil {
		return nil, err
	}
	if len(axes) == 0 {
		return nil, nil
	}
	scope, err := resolveAxes(cmd, axes, "scenario")
	if err != nil {
		return nil, err
	}
	scope.Exclude = manifest.Matrix.Exclude
	return scope, nil
}

// resolveAxes turns the axes somebody asked for into the cells that will be
// run, wherever they arrived from. An axis reaching the runner as a pattern is
// the same defect whether the scenario declared it or the operator typed it,
// so both paths come through here.
func resolveAxes(cmd *cobra.Command, axes map[string][]string, source string) (*runner.Scope, error) {
	var discoveredOn string
	for name, values := range axes {
		if len(values) != 1 || !store.IsDiscovery(values[0]) {
			continue
		}
		dir := flagString(cmd, "store")
		if dir == "" {
			return nil, fmt.Errorf(
				"axis %q is discovered from the artifact store, and none was given; --store <dir> or TESTEASE_STORE",
				name,
			)
		}
		found, err := store.Discover(dir, values[0])
		if err != nil {
			return nil, err
		}
		axes[name] = found
		// What was swept, and that a machine chose it rather than a person:
		// two hosts with different stores must not produce records that read
		// the same. The machine is named only when one of them enumerated
		// something, so a typed-out list cannot read as a discovered one.
		source = "discovery:" + values[0]
		discoveredOn, _ = os.Hostname()
	}

	// Anything still spelled @... asked for a discovery this testease does not
	// do. Swept as a literal it becomes a cell named "@whatever" that measures
	// nothing anybody asked about, so it is refused instead.
	for name, values := range axes {
		for _, v := range values {
			if strings.HasPrefix(v, "@") {
				return nil, fmt.Errorf(
					"axis %q value %q asks for a discovery testease does not have; only a single %s<pattern> value is discovered, everything else is swept literally",
					name, v, store.Prefix,
				)
			}
		}
	}
	return &runner.Scope{Axes: axes, Source: source, DiscoveredOn: discoveredOn}, nil
}

// axesToSweep is the scenario's declared axes, minus any the caller pinned.
//
// Pinning an axis with --param is how one cell of a declared census gets run,
// and doctor says so when it refuses to resolve one. It therefore removes that
// axis rather than contradicting it: sweeping a param the caller had also fixed
// is the one thing a census cannot honour, and refusing there made the tool
// reject the form its own remedy recommended.
func axesToSweep(manifest *scenario.Manifest, pinned map[string]string) (map[string][]string, error) {
	axes, err := manifest.Axes()
	if err != nil {
		return nil, err
	}
	for name := range pinned {
		delete(axes, name)
	}
	return axes, nil
}

func params(cmd *cobra.Command) (map[string]string, error) {
	pairs, err := cmd.Flags().GetStringArray("param")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--param %q is not k=v", p)
		}
		out[k] = v
	}
	return out, nil
}

// instrument builds the fingerprinting instrument from flags. Without both
// halves there is no instrument, and a target is honestly not a Fingerprinter:
// drift is then recorded unavailable, which is a statement about the target
// rather than a gap in the record.
func instrument(cmd *cobra.Command) *dockerprovider.Instrument {
	collect := strings.Fields(flagString(cmd, "instrument-collect"))
	diff := strings.Fields(flagString(cmd, "instrument-diff"))
	if len(collect) == 0 || len(diff) == 0 {
		return nil
	}
	return &dockerprovider.Instrument{
		Name:    flagString(cmd, "instrument-name"),
		Collect: collect,
		Diff:    diff,
	}
}

func runnerName() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s@%s", os.Getenv("USER"), host)
}

// providers is every way a host can come to exist. A scenario names one; if it
// names something else it is told what there is, rather than being run against
// whatever happened to be registered.
func providers(docker, oneShot, stat, state, endpoint target.Provider) map[string]target.Provider {
	return map[string]target.Provider{
		"docker":    docker,
		"endpoint":  endpoint,
		"hyperv":    oneShot,
		"static":    stat,
		"statefile": state,
	}
}

// staticProvider dials machines that already exist. A scenario names them; the
// way in comes from here, because a manifest is content in a repository and a
// password in one is a password in a repository.
func staticProvider(cmd *cobra.Command, runID string, log *slog.Logger, printInstrument *dockerprovider.Instrument) (*static.Provider, error) {
	password := ""
	if file := flagString(cmd, "winrm-password-file"); file != "" {
		body, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read the WinRM password: %w", err)
		}
		password = strings.TrimSpace(string(body))
	}
	https, _ := cmd.Flags().GetBool("winrm-https")

	sshPassword := ""
	if file := flagString(cmd, "ssh-password-file"); file != "" {
		body, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read the SSH password: %w", err)
		}
		sshPassword = strings.TrimSpace(string(body))
	}
	sshPort, _ := cmd.Flags().GetInt("ssh-port")
	sshSudo, _ := cmd.Flags().GetBool("ssh-sudo")

	return static.New(static.Options{
		SSM: static.SSM{
			Profile:       flagString(cmd, "aws-profile"),
			Region:        flagString(cmd, "aws-region"),
			TransferStore: flagString(cmd, "ssm-transfer-store"),
		},
		WinRM: static.WinRM{
			User:     flagString(cmd, "winrm-user"),
			Password: password,
			HTTPS:    https,
			Insecure: https,
		},
		SSH: static.SSH{
			User:       flagString(cmd, "ssh-user"),
			Password:   sshPassword,
			KeyFile:    flagString(cmd, "ssh-key"),
			Port:       sshPort,
			KnownHosts: flagString(cmd, "ssh-known-hosts"),
			Sudo:       sshSudo,
		},
		Instrument: printInstrument,
		RunID:      runID,
		Runner:     runnerName(),
		Log:        log,
	}), nil
}

// dockerDriver adapts the docker backend to what the runner asks for. The two
// describe the same thing in their own words on purpose: the runner says what
// it needs of a container runtime, the backend says what it can do, and
// neither has to import the other's vocabulary.
type dockerDriver struct{ client backend.Client }

func (d dockerDriver) Drive(ctx context.Context, spec runner.DriveSpec, opt target.RunOpts) (target.Result, error) {
	request := backend.DriveSpec{
		Image:         spec.Image,
		Args:          spec.Args,
		Env:           spec.Env,
		Workspace:     spec.Workspace,
		Mount:         spec.Mount,
		Scenario:      spec.Scenario,
		ScenarioMount: spec.ScenarioMount,
		Media:         spec.Media,
		Network:       spec.Network,
		Labels:        spec.Labels,
	}
	local, name, err := daemonIsLocal(ctx, d.client)
	if err != nil {
		return target.Result{RC: -1}, err
	}
	if local {
		return d.client.Drive(ctx, request, opt)
	}
	if d.client.Log != nil {
		d.client.Log.Debug("transporting a drive step to a remote docker daemon", "daemon", name)
	}
	return d.client.DriveRemote(ctx, request, opt)
}

// daemonIsLocal asks a daemon where it is and names it. Local drive steps use
// bind mounts; remote ones transport their scenario, media, and workspace
// through the Docker API. Doctor uses the same answer to check the appropriate
// path.
func daemonIsLocal(ctx context.Context, client backend.Client) (bool, string, error) {
	name, err := client.Output(ctx, "info", "--format", "{{.Name}}")
	if err != nil {
		return false, "", fmt.Errorf("ask the docker daemon where it is: %w", err)
	}
	name = strings.TrimSpace(name)
	host, _ := os.Hostname()
	return strings.EqualFold(name, host), name, nil
}

// measurementConditions is what was configured outside the scenario and
// changes what this run measures.
//
// These reach the runner from a flag or from TESTEASE_*, and until they were
// recorded a run instrumented one way and a run instrumented another were the
// same record. Credentials are deliberately absent: --ssh-key and the password
// files say how the runner got in, not what it read, and a record is published.
//
// A flag with a non-empty default is recorded whether or not anybody typed it,
// so --win-memory shows up on a Linux run that had no Windows cell to spend it
// on. That is a condition that did not apply rather than a wrong one, and it
// is the safe direction to err: the alternative is guessing at intent and
// dropping a value somebody really did set.
func measurementConditions(cmd *cobra.Command, selection *targetcatalog.Selection) map[string]string {
	failurePolicy := "retain"
	if release, _ := cmd.Flags().GetBool("release-failed-targets"); release {
		failurePolicy = "release"
	}
	conditions := map[string]string{
		"store":                 flagString(cmd, "store"),
		"instrument.name":       flagString(cmd, "instrument-name"),
		"instrument.collect":    flagString(cmd, "instrument-collect"),
		"instrument.diff":       flagString(cmd, "instrument-diff"),
		"docker.context":        flagString(cmd, "docker-context"),
		"win.docker_context":    windowsDockerContext(cmd),
		"win.memory":            flagString(cmd, "win-memory"),
		"aws.region":            flagString(cmd, "aws-region"),
		"target.failure_policy": failurePolicy,
	}
	for key, value := range targetConditions(selection) {
		conditions[key] = value
	}
	for key, value := range conditions {
		if value == "" {
			delete(conditions, key)
		}
	}
	return conditions
}

func dockerClients(cmd *cobra.Command, log *slog.Logger) (backend.Client, backend.Client) {
	ordinary := backend.Client{Context: flagString(cmd, "docker-context"), Log: log}
	windows := ordinary
	windows.Context = windowsDockerContext(cmd)
	return ordinary, windows
}

func windowsDockerContext(cmd *cobra.Command) string {
	if context := flagString(cmd, "win-docker-context"); context != "" {
		return context
	}
	return flagString(cmd, "docker-context")
}
