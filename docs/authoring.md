# Writing scenarios

A scenario asks one question. It contains the hosts, the ordered steps, and
the checks for one run. Keep product-specific scenarios in their own
repository; this repository only ships the small `hello` example.

## The local loop

Check a scenario without contacting Docker or another target:

```text
testease validate hello --scenario-root examples
testease plan hello --scenario-root examples
```

`validate` checks the YAML, probe files, mounts, parameters, and expectations.
`plan` expands parameters and shows the hosts and steps that would run. For a
matrix, add explicit values when needed:

```text
testease plan upgrade --scenario-root scenarios \
  --matrix from=6.2.4,7.2.1 --matrix to=7.2.3
```

The plan command does not start a target and does not inspect images or input
files. Use `doctor` for those checks.

## Reusing step setup

Use `step_templates` when several steps share a host, timeout, environment,
media, or expectation:

```yaml
step_templates:
  check:
    host: app
    timeout: 300s
    expect: {rc: 0}

steps:
  - use: check
    label: check before upgrade
    probe: probes/before.sh
  - use: check
    label: check after upgrade
    probe: probes/after.sh
```

The step overrides the template. Maps merge by key; lists replace the
template's list. Templates cannot use other templates. This keeps the result
easy to inspect and avoids a second inheritance language.

Hosts can use the same pattern:

```yaml
host_templates:
  linux:
    provider: docker
    image: debian:12-slim
    env: {MODE: test}

hosts:
  app: {use: linux}
  worker: {use: linux, image: app-worker:dev}
```

Host templates cannot use other host templates. A host's map values merge and
its lists replace the template lists.

## Sparse matrices

Axes still form a product by default. Exclude combinations that do not make
sense instead of encoding a whole relationship into one string:

```yaml
params:
  from: {axis: true, values: "6.2.4,7.0.1"}
  to: {axis: true, values: "7.2.1,7.2.3"}
matrix:
  exclude:
    - {from: 7.0.1, to: 7.2.1}
```

An exclusion must name declared axes. Its values are still recorded in the
campaign and run scope.

## Probe contract

A probe has three separate outputs:

- exit status: the operation's result
- stdout and stderr: text kept as the probe's report
- `$TESTEASE_FACTS` (also available as file descriptor 3 for local shell
  probes): tab-separated readings

Write readings as:

```text
fact<TAB>key<TAB>value
no-reading<TAB>key<TAB>reason
```

`no-reading` is different from a value such as `unknown`: it says the probe
looked but could not take that reading. The judge will not treat it as a
passing value.

Small starting points are in [`examples/templates`](../examples/templates/).
