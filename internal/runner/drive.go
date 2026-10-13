package runner

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/scenario"
	"github.com/jmurray2011/testease/internal/target"
)

// DrivePrefix marks a step that runs on the runner rather than on the target.
//
// A browser driving an application uses the target's network endpoint and
// never its execution surface - those are different capabilities on purpose,
// and a step that confused them would be measuring the box rather than the
// product. Saying so in the probe keeps the manifest's field list exactly as
// it is: what a step runs is already what `probe:` names.
const DrivePrefix = scenario.DrivePrefix

// The contract a drive image runs under. Everything a suite needs to know
// arrives as environment, because that is the one interface every container
// runtime already has.
const (
	// EndpointEnv is the target's endpoint, which is the whole point of the
	// step: the suite drives this and knows nothing else about the machine.
	EndpointEnv = "TESTEASE_ENDPOINT"

	// WorkspaceEnv is a directory the suite may write to, mounted from this
	// machine. Reports and anything else worth keeping go there.
	WorkspaceEnv = "TESTEASE_WORKSPACE"

	// FactsEnv names a file the suite may write readings to, in the same
	// format a probe writes to fd 3. A container cannot be handed a third
	// descriptor as simply as a shell can, so it is handed a path.
	FactsEnv = "TESTEASE_FACTS"

	// ReportMark is how a suite says which file its report ended up in.
	//
	// The LAST such line wins, and the file is never chosen by modification
	// time. A suite that chains its projects can re-run a dependency which
	// writes its own report in the same millisecond, so only the log says
	// which file this run asked for. That was measured: picking the newest
	// file quietly compares the wrong report against the baseline.
	ReportMark = "testease-report ->"

	// ContainerWorkspace is where the workspace is mounted inside the image.
	ContainerWorkspace = "/workspace"

	// ScenarioEnv and ContainerScenario are where the scenario directory is,
	// read-only, for a suite that ships beside the question it answers.
	ScenarioEnv       = "TESTEASE_SCENARIO"
	ContainerScenario = "/scenario"
)

const factsFile = ".testease-facts"

// Driver runs a container on this machine. The interface lives here because
// the runner is what needs one; a backend implements it without knowing that
// anything called a scenario exists.
type Driver interface {
	Drive(ctx context.Context, spec DriveSpec, opt target.RunOpts) (target.Result, error)
}

// DriveSpec is a container to run beside the target, not on it.
type DriveSpec struct {
	Image string
	Args  []string
	Env   map[string]string

	// Workspace is a directory on this machine, mounted into the container at
	// Mount.
	Workspace string
	Mount     string

	// Scenario is the scenario directory, mounted read-only. A suite usually
	// travels with the question it answers, and mounting it read-only says
	// which side of the line it is on: the scenario is content this run reads,
	// never something the run may edit.
	Scenario      string
	ScenarioMount string

	// Media is what the step asked to be mounted beside those two - a suite
	// checkout, a fixture directory. Parsed the same way a host's media is, so
	// one spelling covers both sides of a run.
	Media []target.Media

	// Network is the target's own network, joined so the container can reach
	// it. Empty means the runtime's default, which is right for a target that
	// is not on one.
	Network string

	// Labels carry the run's ownership onto the container, the way a target's
	// do: a drive container the timeout orphaned keeps driving the target,
	// and reap has to be able to prove whose it was.
	Labels map[string]string
}

// Networked is a target that lives on a network a container can be attached to.
//
// Defined here rather than beside the provider because it is the runner that
// needs it: a drive step's container has to be able to REACH the target, and a
// docker provider puts each cell's hosts on a network of their own. Without
// joining it the container lands on the default bridge, which docker isolates
// from user-defined ones - so the browser cannot route to the box at all.
type Networked interface {
	Network() (string, bool)
}

// isDrive reports whether a step runs here rather than on the target.
func isDrive(probe string) bool { return scenario.IsDrive(probe) }

// driveStep runs one step as a container beside the target.
//
// The step's logs are flushed and closed before the report is harvested,
// because the report is named in the log and the log has to be readable to
// find it.
func (r *run) driveStep(ctx context.Context, seq int, step scenario.Step, opts target.RunOpts,
	stdout, stderr *os.File, outPath, errPath string, facts *[]record.Fact, warnings *[]record.Warning,
) (target.Result, error) {
	result, err := r.drive(ctx, step, opts)

	_ = stdout.Sync()
	_ = stderr.Sync()
	*facts, *warnings = r.collectDriven(seq, step, outPath, errPath)
	return result, err
}

// drive runs the container.
func (r *run) drive(ctx context.Context, step scenario.Step, opts target.RunOpts) (target.Result, error) {
	if r.opt.Driver == nil {
		return target.Result{RC: -1}, &PreconditionError{
			Message: fmt.Sprintf("step %q drives a container on this machine, and this testease has no way to run one", step.Label),
			Remedy:  RemedyBackends + "   # a drive step needs a container runtime here, not only on the target",
		}
	}

	endpoint, err := r.endpoint(step.Host)
	if err != nil {
		return target.Result{RC: -1}, err
	}

	// ABSOLUTE, because the path leaves this process. Docker reads a bind
	// source that is not absolute as a VOLUME NAME and creates an empty volume
	// under it, so a scenario reached by a relative root - `--scenario-root .`,
	// which is what the corpus's own invocations use - arrives inside the
	// container as an empty directory. The suite that travels with the question
	// is then not there at all, and the step fails for a reason that has
	// nothing to do with the product. Measured.
	scenarioDir, err := filepath.Abs(r.plan.Dir)
	if err != nil {
		return target.Result{RC: -1}, fmt.Errorf("locate the scenario directory for step %q: %w", step.Label, err)
	}

	// Keep the bind source beside the run rather than under the process's
	// default temporary directory. Snap-packaged Docker has a private /tmp
	// namespace: a directory created in the host's /tmp is mounted as an empty,
	// root-owned directory in the drive container. The runs directory is already
	// checked for writability and is deliberately chosen by the operator, so it
	// is the stable shared namespace for this short-lived workspace too.
	workspaceRoot, err := filepath.Abs(r.opt.RunsDir)
	if err != nil {
		return target.Result{RC: -1}, fmt.Errorf("locate drive workspace root for step %q: %w", step.Label, err)
	}
	workspace, err := os.MkdirTemp(workspaceRoot, ".testease-drive-")
	if err != nil {
		return target.Result{RC: -1}, fmt.Errorf("make a workspace for step %q: %w", step.Label, err)
	}
	r.workspace = workspace

	env := make(map[string]string, len(step.Env)+len(r.opt.DriveSecrets)+3)
	for k, v := range step.Env {
		env[k] = v
	}
	// Injected, never recorded. A credential a suite needs has no honest home
	// in a committed manifest or in the params a record keeps, so it travels
	// beside them - the same bargain --ssh-password-file already makes.
	for k, v := range r.opt.DriveSecrets {
		env[k] = v
	}
	env[EndpointEnv] = endpoint
	env[WorkspaceEnv] = ContainerWorkspace
	env[FactsEnv] = ContainerWorkspace + "/" + factsFile
	env[ScenarioEnv] = ContainerScenario

	return r.opt.Driver.Drive(ctx, DriveSpec{
		Image:         strings.TrimPrefix(step.Probe, DrivePrefix),
		Args:          step.Args,
		Env:           env,
		Workspace:     workspace,
		Mount:         ContainerWorkspace,
		Scenario:      scenarioDir,
		ScenarioMount: ContainerScenario,
		Media:         r.media(step.Media),
		Network:       targetNetwork(r.targets[step.Host]),
		Labels:        r.driveLabels(step),
	}, opts)
}

// driveLabels is the same ownership statement a provider puts on a target,
// because an orphaned drive container is reaped by the same proof.
func (r *run) driveLabels(step scenario.Step) map[string]string {
	host := hostRef()
	return map[string]string{
		target.LabelRunner:  host.User + "@" + host.Hostname,
		target.LabelRun:     r.runID,
		target.LabelPID:     fmt.Sprint(os.Getpid()),
		target.LabelCreated: time.Now().UTC().Format(time.RFC3339),
		target.LabelBackend: "drive",
		target.LabelHost:    step.Host,
		target.LabelCell:    r.cell,
	}
}

// collectDriven reads what the suite left behind: its readings, and the report
// it named. The workspace survives until the step's artifacts have been taken
// out of it, and goes at the end of the step.
func (r *run) collectDriven(seq int, step scenario.Step, outPath, errPath string) ([]record.Fact, []record.Warning) {
	workspace := r.workspace
	if workspace == "" {
		return nil, nil
	}

	var warnings []record.Warning
	facts, err := readFacts(filepath.Join(workspace, factsFile))
	if err != nil {
		// INTO THE RECORD, not only into the log. The facts read before the
		// scan stopped are kept, so the set is short - and a fact that was
		// measured and dropped by the reader is otherwise indistinguishable
		// from one that was never measured.
		r.opt.Log.Warn("the suite's readings could not all be read",
			"step", seq, "label", step.Label, "error", err)
		warnings = append(warnings, record.Warning{
			About:    fmt.Sprintf("the suite's readings for step %d (%s)", seq, step.Label),
			Message:  err.Error(),
			Recorded: fmt.Sprintf("%d reading(s) taken before the reader stopped", len(facts)),
		})
	}
	if report := r.harvest(seq, step, workspace, outPath, errPath); report != "" {
		value := report
		facts = append(facts, record.Fact{Key: record.ReportFact, Value: &value, Probe: step.Probe})
	}
	return facts, warnings
}

// harvest copies out the report the suite named and returns where it landed in
// the record, relative to its root.
//
// Nothing is guessed. A suite that named no report produced none as far as
// testease is concerned, and inventing one by picking the newest file in the
// workspace is exactly the mistake this contract exists to avoid.
func (r *run) harvest(seq int, step scenario.Step, workspace, outPath, errPath string) string {
	named := ""
	for _, log := range []string{outPath, errPath} {
		found, err := reportLine(filepath.Join(r.dir, filepath.FromSlash(log)))
		if err != nil {
			// A scan that stopped early may have missed a later mark, and the
			// mark it kept may be stale. Say so rather than quietly comparing
			// the wrong report against the baseline.
			r.opt.Log.Warn("the step's log could not be fully scanned for its report",
				"step", seq, "label", step.Label, "log", log, "error", err)
			return ""
		}
		if found != "" {
			named = found
		}
	}
	if named == "" {
		return ""
	}

	rel := strings.TrimPrefix(named, ContainerWorkspace+"/")
	if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		r.opt.Log.Warn("the suite named a report outside its workspace",
			"step", seq, "label", step.Label, "report", named)
		return ""
	}

	body, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(rel)))
	if err != nil {
		r.opt.Log.Warn("the suite named a report that is not there",
			"step", seq, "label", step.Label, "report", named, "error", err)
		return ""
	}

	dest := r.freshPath(ArtifactsDir + "/" + record.StepSlug(seq, step.Label) + "-" + filepath.Base(rel))
	if err := os.WriteFile(filepath.Join(r.dir, filepath.FromSlash(dest)), body, 0o644); err != nil {
		r.opt.Log.Warn("could not keep the report", "step", seq, "error", err)
		return ""
	}
	return dest
}

// endpoint is the target's network endpoint. A target that has none cannot be
// driven, and refusing early beats a suite that cannot connect and says so in
// its own words half an hour later.
func (r *run) endpoint(host string) (string, error) {
	endpointer, ok := r.targets[host].(target.Endpointer)
	if !ok {
		return "", &PreconditionError{
			Message: fmt.Sprintf("host %q has no network endpoint, and a drive step needs one", host),
			Remedy:  fmt.Sprintf("give host %q a port: in the scenario, or point the step at a host that has one", host),
		}
	}
	u, ok := endpointer.Endpoint()
	if !ok {
		return "", &PreconditionError{
			Message: fmt.Sprintf("host %q could not say what its endpoint is", host),
			Remedy:  RemedyBackends + "   # which providers offer an endpoint, and what each needs",
		}
	}
	return u.String(), nil
}

// scanBuffer is a scanner sized for suite output. The default 64KB cap
// silently ends a scan at the first longer line - a progress-bar burst, a
// one-line JSON dump - and everything after it is lost without a word.
func scanBuffer(scanner *bufio.Scanner) {
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
}

// reportLine is the last report a log names. Last wins, and modification time
// is never consulted.
func reportLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer func() { _ = f.Close() }()

	named := ""
	scanner := bufio.NewScanner(f)
	scanBuffer(scanner)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.Index(line, ReportMark); i >= 0 {
			named = strings.TrimSpace(line[i+len(ReportMark):])
		}
	}
	if err := scanner.Err(); err != nil {
		return named, fmt.Errorf("scan %s: %w", path, err)
	}
	return named, nil
}

// readFacts reads the readings a suite wrote, in the shape a probe writes to
// fd 3.
func readFacts(path string) ([]record.Fact, error) {
	f, err := os.Open(path)
	if err != nil {
		// A suite that wrote no readings wrote no readings.
		return nil, nil
	}
	defer func() { _ = f.Close() }()

	var facts []record.Fact
	scanner := bufio.NewScanner(f)
	scanBuffer(scanner)
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), "\t")
		if len(parts) < 3 {
			continue
		}
		// The file and fd 3 are two spellings of one channel, so both verbs
		// have to mean the same thing here as they do there.
		switch parts[0] {
		case "fact":
			value := strings.Join(parts[2:], "\t")
			facts = append(facts, record.Fact{Key: parts[1], Value: &value})
		case "no-reading":
			facts = append(facts, record.Fact{Key: parts[1], Error: strings.Join(parts[2:], "\t")})
		}
	}
	if err := scanner.Err(); err != nil {
		return facts, fmt.Errorf("scan %s: %w", path, err)
	}
	return facts, nil
}

// fromWorkspace reads a drive step's artifact off this machine. Its files are
// in the workspace: the container they were written in is gone, and the target
// never had them.
func (r *run) fromWorkspace(a scenario.Artifact) ([]byte, error) {
	if r.workspace == "" {
		return nil, fmt.Errorf("this step left no workspace to take %s from", a.From)
	}
	rel := strings.TrimPrefix(a.From, ContainerWorkspace+"/")
	if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return nil, fmt.Errorf("artifact %s is outside the step's workspace", a.From)
	}
	return os.ReadFile(filepath.Join(r.workspace, filepath.FromSlash(rel)))
}

// dropWorkspace lets the working material go, once what was worth keeping is
// in the record.
func (r *run) dropWorkspace() {
	if r.workspace == "" {
		return
	}
	_ = os.RemoveAll(r.workspace)
	r.workspace = ""
}

// targetNetwork is the network the target sits on, when it sits on one.
func targetNetwork(t target.Target) string {
	n, ok := t.(Networked)
	if !ok {
		return ""
	}
	name, on := n.Network()
	if !on {
		return ""
	}
	return name
}
