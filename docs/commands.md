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

Use `-o json` when another program is consuming the result. See the main
README for provider-specific flags and record layout.

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
