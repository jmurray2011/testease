# testease

Run scenarios against machines and save what happened.

Test results and the conclusion drawn from them are separate. You can inspect
or rejudge a saved run without running it again.

## Two minutes, from a clone

You need a Docker daemon and Go. Nothing else.

From a checkout, `make build` writes `./testease`. To put it on `PATH`, use
`make install` with `GOBIN` set to a directory already on `PATH`.

    make build
    ./testease doctor hello --scenario-root examples --strict

`doctor` checks what a run needs before the run. On a new machine it will
tell you the image is missing and print the command that fixes it:

`--strict` also makes warnings fail the check, which is useful in CI. Without it,
`doctor` reports warnings but allows a run to proceed.

    fail  images  not on the configured daemon(s) - debian:12-slim
                  try: docker pull debian:12-slim

Do that, then measure something:

    ./testease run hello --scenario-root examples

    hello-20260822T013007Z
    1 of 1 steps measured; record at scenario-runs/hello-20260822T013007Z
    this record declares expect@3 and running does not judge: testease judge scenario-runs/hello-20260822T013007Z

**Running is not judging.** The run measured; it did not produce a verdict. Run:

    ./testease judge scenario-runs/hello-20260822T013007Z

    expect@3   pass
      met      ask the box what it is

And read what was actually recorded:

    testease render --view tsv   scenario-runs/hello-...    # a row per step
    testease render --view facts scenario-runs/hello-...    # what the probes read

    fact        hello-20260822T013007Z
    os.arch     x86_64
    os.id       debian
    os.kernel   linux
    os.version  12

Now break it. Edit `examples/hello/probes/whoami.sh` to exit 1, run it again,
and judge it: the verdict becomes `regressed` and names the step and the
reading.

## Where the real scenarios are

`examples/hello` is only an example. It shows the scenario file format and the
run/judge/render commands. Put product-specific scenarios in a separate
repository and point at them once:

    export TESTEASE_SCENARIO_ROOT=/path/to/scenarios
    export TESTEASE_RUNS_DIR=/path/to/scenario-runs

Testease runs scenarios and writes records. Your CI system can handle machine
allocation, scheduling, and publishing. The same commands work from a terminal
or from a job.

## What is in a record

A record is a directory. `events.jsonl` is the evidence - append-only, never
rewritten, one JSON object per line. Everything else is derived from it and can
be thrown away:

    events.jsonl     the record itself
    run.json         a cache of it, rebuildable with `render --view meta`
    steps/           each step's stdout and stderr
    artifacts/       files a step declared it wanted kept
    verdicts/        what a judge concluded, written once per judge@version
    cells/           for a census, one record per cell, same shape

Repairs are read-time and reported. A damaged line is never fixed on disk; the
reader says what it had to tolerate and every projection repeats it.

## The rest of the manual

- [Writing scenarios](docs/authoring.md) - the authoring loop, step and host
  templates, sparse matrices, and the probe contract.
- [Running against real machines](docs/operations.md) - target catalogs,
  campaigns, already-running SSH/WinRM/SSM hosts, large media, carry steps,
  secret handoffs, and endpoint targets.
- [Judging and reproducibility](docs/judging.md) - census judges, re-judging a
  saved record, and comparing stable facts across independent runs.
- [Commands](docs/commands.md) - the full command list, exit codes, and the
  external judge contract.
- [Configuration](docs/configuration.md) - every `TESTEASE_*` variable, and
  which values must stay on the command line so the record can show them.

## Building

`make validate` is the gate: format, vet, lint, test under `-race`, and
`govulncheck`. Nothing is done until it is green, and it is never weakened to
get there. The first validation run downloads the pinned formatter, linter, and
vulnerability scanner into the ignored `.tools/` directory; they do not need to
be installed globally. The Go version comes from `go.mod`, so the Go toolchain
may be downloaded automatically by Go. `make build` produces the static trimmed
binary. To install it as `testease`, set `GOBIN` to a directory already on your
PATH before running `make install`.

Some tests need real substrate and skip without it - a docker daemon for the
provider tests, `TESTEASE_TEST_WINDOWS_CONTEXT` for the Windows probe. Skips
are reported.

## License

MIT. See `LICENSE`.
