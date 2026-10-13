# Judging and reproducibility

A run measures; it never concludes. Every verdict here is reached by reading a
saved record, so it can be re-reached later without touching the target again.

## Restore censuses

For restore censuses, `restore-matrix@1` turns the low-level readings into the
answer a release team needs. Each cell says `restored`, `hang`,
`source-data-missing`, `target-not-replaced`, or `unverified`; the census says
`restored` only when every measured cell did. The judge first checks the
scenario's declared preconditions, then independently derives the outcome from
`restore.timed_out`, `source.version`, `marker.rung`, and `sentinel.count`. It
does not trust the probe's own `restore.outcome` label. That separation means a
saved record can be re-judged and the team's conclusion does not depend on the
script correctly grading itself.

    testease judge --judge restore-matrix scenario-runs/win-restore-...

    restore-matrix@1  restore-failed
      hang            5.0.2-to-5.0.3
                      the restore exceeded its recorded time limit

## Upgrade censuses

For upgrade censuses, `release-readiness@3` keeps the measurement result
separate from the application result. `matrix@3 pass` means every declared
precondition and evidence step measured correctly. The release judge then
requires the requested package, an active service, a usable HTTP response, a
target-compatible database schema, and an installed payload that matches the
RPM with no unowned code. Schema compatibility follows the release line: Flyway
numbers migrations `<major>.<minor>.<patch>.<n>`, so target 7.2.3 is satisfied
by 7.2.3.3 and the four-part hotfix target 7.3.2.1 by 7.3.2.11. When launcher
and process-transition readings are present, it uses them to distinguish a
manager handoff failure. Its census verdict is `release-ready` only when every
cell is ready; product failures make it `release-blocked`, while missing
evidence or a failed premise is `unverified`.

    testease judge --judge release-readiness scenario-runs/upgrade-sweep-...

    release-readiness@3  release-blocked
      manager-transition-failed  3.0.5
                      the target systemd launcher is absent
                      no target JVM is running
                      the legacy JVM remains running instead

This second verdict is appended beside the matrix verdict and reads only the
saved record. Re-judging does not rerun or modify the target.

## Reproducibility

A scenario may declare which readings should repeat and the projection a team
should see:

    reproducibility:
      stable_facts: [suite.commit, target.version, evidence.sha256]
    views:
      team:
        kind: facts
        facts: [suite.commit, target.version, evidence.sha256]

After two independent runs, Testease can reach and retain the cross-record
conclusion without changing either source record:

    testease reproduce RUN_A RUN_B
    testease render --preset team RUN_A RUN_B -o html > evidence.html

The reproduction result records both `events.jsonl` hashes under
`scenario-runs/comparisons/`. The named view selects recorded values; it does
not add or change results.

For matrix runs, pass the two top-level run IDs the same way. Testease matches
cells by matrix key, compares each cell's declared stable facts only with the
same cell in the other run, and pins both parent and child event streams in the
comparison verdict. A changed matrix scope is therefore unverified rather than
silently comparing unlike rows.
