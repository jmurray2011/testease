# Commands

The common loop is:

```text
testease validate <scenario>
testease plan <scenario>
testease doctor <scenario> --strict
testease run <scenario>
testease judge <run-directory>
testease render --view facts <run-directory>
```

`validate` and `plan` are local checks. `doctor` checks the selected
provider, images, media, and other runtime requirements. `run` writes an
append-only record. `judge` reads that record and writes a verdict beside it.
`render` only displays recorded data.

For a campaign, validate the definition before running it:

```text
testease campaign validate <campaign>
testease campaign run <campaign>
```

Use `-o json` when another program is consuming the result. See
[Running against real machines](operations.md) for provider-specific flags and
the main README for the record layout.

## All commands

    validate   check scenario files without running them.
    plan       show resolved steps and matrix cells without running them.
    doctor     check what a run needs, before the run.
    run        measure. Writes a record; reaches no conclusion.
    judge      read a record and reach a verdict, without re-measuring.
    render     project a record: tsv, table, rows for a census, facts across
               several runs, meta to rebuild run.json. -o json or html.
    scenarios  the scenarios under the scenario root.
    runs       the records under the runs directory.
    backends   what a host can be, and what each kind can do.

    campaign validate  check a campaign against its scenario without running hooks.
    reproduce  compare stable facts across runs.
    campaign   run and inspect a group of related runs.
    follow     show campaign progress.
    package    write an archive of campaign records.
    publish    copy cited run records to a shared directory.
    reap       remove targets whose run is provably over.
    import     read a run.tsv written before testease into a record.
    targets    the targets standing right now, and how to get onto them.

## Exit codes

Operational commands take `-o json`. The same exit codes are used at a terminal
and in CI:

    0   measured, and any verdict was in its judge's pass set
    2   usage
    3   a precondition the person running it fixes
    4   integrity: a locked record, or a judge disagreeing with itself
    10  the substrate failed
    11  a probe failed
    12  interrupted
    20  a verdict outside its judge's pass set
    21  nothing could be judged

## External judges

If the built-in judges do not express the question, run a separate command:

```text
testease judge <run-directory> \
  --judge-command ./judge.sh --judge-name team-policy --judge-version 1
```

The command receives the record directory as its final argument and in
`TESTEASE_RECORD_DIR`. It writes a JSON object with at least a `verdict`, for
example:

```json
{"verdict":"pass","steps":{"check":{"verdict":"met"}}}
```

The result is stored beside built-in verdicts and is tied to the record's
event hash. Use `--pass-verdict` to define which verdicts count as passing.
