# CLI usability grid verification

Verified on 2026-10-29 in the supplied worktree, branch `bw-3095-grid-ux`.
The baseline CLI was built from `1f4998a3e84b7b94bbd8524a66a4c44a95fcbf3b`
before source edits. The final implementation is committed through `cf7fef68d`.
Both binaries are development builds from this worktree, rather than the
installed QA CLI.

All binary invocations used private `HOME` and `SPARKWING_HOME` under
`/tmp/sw-codex-grid-ux/`. Go used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOPATH=/home/bombuntu/.go`, and
`GOMODCACHE=/home/bombuntu/.go/pkg/mod`. Temporary test files and runtime
sockets also lived under the lane directory. No real controller, cluster,
cloud service or operational database was used.

## Results

| Cell | Result | Baseline evidence | Final evidence |
| --- | --- | --- | --- |
| R02C07 | Fixed | `runs stauts` and `runz list` exited 2 with `unexpected positional argument; use a kind-named flag`. | Both exit 2 with nearest-command suggestions: `sparkwing runs status` and `sparkwing runs`. |
| R02C08 | Already fixed | `runs list --bogus` exited 2 and listed valid flags followed by `(see --help)`. | Same help hint remains. No diagnostic code change was needed for this cell. |
| R02C11 | Fixed | A loopback fixture replaying the grid's recorded run detail produced plain/pretty status without times. | Both formats show run start `2026-10-29T19:57:48.559Z`, finish `2026-10-29T19:57:50.803Z`, duration `2.244s`, and the job's start/finish with `839ms`. Missing timestamps print `-`. |
| R02C12 | Fixed | `profiles set --profile fixture --default` exited 2 with `unknown flag --default`. | Two loopback controllers received requests after switching defaults. The first received two requests and the second one: first default, second default, then explicit first override. List/show report `default`, clearing it succeeds, and tests preserve local routing. |
| R02C13 | Fixed | A missing-scheme controller URL produced only `must be an http or https URL`. A loopback server closing its connection produced raw `Get ...: EOF` with no repair. | The missing-scheme error names `staging.sparkwing.dev` and suggests `--controller https://HOST`. Transport errors retain their cause and name profile inspection, URL repair, network and controller availability checks. Credential-bearing URLs and private non-HTTP paths remain hidden. |
| R04C02 | Fixed | Running the grid's required-argument fixture with `--env=qa` failed with `argument --env is required`. | `--env=qa` and `--env qa` list `dev, prod`; `--env=dev` completes successfully. Required invocation values are parsed after declaration-only inspection. |
| R04C19 | Fixed | The grid child fixture ran and failed with `child source is outside the parent claim's declared children`. | The same refusal now names `StartsChildren`, TypeScript `startsChildren`, and `StartsChildrenIn` for another repository. Both controller and store guards are tested. |
| R05C01 | Fixed | A fresh checkout's `pipeline new --pipeline hello --template typescript` failed with `unknown shape "typescript"`. | The command succeeds and emits `jobs/hello.ts`, an entry module, an exact SDK dependency and pipeline configuration. A second pipeline can be added. Generated TypeScript passed `tsc --noEmit` against the worktree SDK declarations. |
| R01C20 | Fixed | Inventory contained a Go `pin`, no TypeScript pin, and no SDK kinds. | JSON includes `sdk: go`, `pin: v0.68.0-qa.16` for the Go fixture and `sdk: typescript`, `pin: 0.68.0-qa.16` for the TypeScript fixture. Plain and pretty also show SDK kind. |

The local pipeline fixture was reconstructed from the grid's R04C02 evidence,
with its SDK replaced by this worktree and an initial fixture commit. Runs used
`--sw-allow-unadmitted` and separate private homes. Status used the original
R02C11 recorded JSON served over loopback. Connection failure used a loopback
EOF fixture rather than an external DNS lookup.

## Regression evidence

Each real fix was exercised against the baseline before changing its owner.
The following tests failed on the original behavior and pass with the fix:

- `TestMisspelledCommandSuggestsNearestSibling`
- `TestRunStatusTextIncludesRunAndJobTiming`
- `TestProfileDefaultSwitchControlsCLIAndKeepsLocalHelpersLocal`
- `TestControllerURLDiagnosticNamesInputAndRepair`
- `TestControllerTransportFailureIncludesRepairAndPreservesCause`
- `TestNativeLocalPreparationInspectsBeforeParsingRequiredEnum`
- `TestUndeclaredChildSourceNamesDeclarationMethod`
- `TestChildPipelinesMustBeDeclared`
- `TestPipelineNewTypeScriptBootstrapsAndAddsPipelines`
- `TestDeriveFleetIncludesSDKAndTypeScriptLockedPin`

The fleet baseline assertion used serialized rows before the new `SDK` field
existed; its final assertion reads the field directly. Existing plain-status
assertions now expect the requested timing lines. The private-enum parser test
marks its declaration secret; complete/incomplete discovery tests still verify
that private metadata is masked or withheld at the CLI boundary.

## Checks

Passed:

- Focused CLI, parser, profile/config, argument and status checks, including the
  local-run/profile guards. Final CLI package run: 9.326 seconds.
- The controller and store undeclared-child regression tests.
- Complete `internal/clisurface`, `internal/repos`, `internal/nodeproto` and
  `internal/runspec` package tests.
- `go vet` over the CLI, parser, fleet, argument, controller-client and
  machine-config packages.
- CLI build, generated TypeScript type-check, repository/CI documentation
  grammar tests, comment checks and `git diff --check`.
- All 29 generated CLI reference pages matched the rebuilt CLI. Embedded docs
  and changelog matched their source files.

Reproduce focused checks with the isolated environment above:

```bash
W=/home/bombuntu/.xwing/envs/1087/sparkwing
go -C "$W" test ./cmd/sparkwing ./internal/clisurface ./internal/userconfig \
  -run 'TestProfile|TestRunStatus|TestNativeLocalPreparationInspects|TestPipelineNewTypeScriptBootstraps|TestControllerURLDiagnostic|TestMisspelledCommand|TestALocalRunReachesNoProfileOrControllerSecrets' -count=1
go -C "$W" test ./internal/controller ./internal/store \
  -run 'TestUndeclaredChildSource|TestChildPipelinesMustBeDeclared' -count=1
go -C "$W" test ./internal/repos ./internal/nodeproto ./internal/runspec -count=1
```

The full gate, full lint, race suite and PostgreSQL suite remain with the lead,
as requested. No release, deployment, real-service smoke test or TypeScript
package publication/install was performed. TypeScript validation covered
scaffolding, native pin resolution and type-checking against the worktree SDK.

## Implementation choices

- The requested default switch adds `profiles set --default` and
  `profiles.<name>.default`. An explicit CLI profile wins; `--profile=` selects
  local once. The default is applied at CLI dispatch only for optional-profile
  commands, so internal local clients, local runs, required-profile commands
  and `--all-repos` keep their routing contracts. Clearing or removing the
  selected default restores local selection. No environment-based selector or
  legacy default-profile shim was added.
- `--template typescript` is the requested native scaffold option. It reuses
  existing configuration, trigger and output helpers. It does not change the
  pipeline generator or TypeScript SDK pin implementation.
- Inventory uses the existing engine TypeScript pin reader. The Go updater's
  `SDKPin` helper retains its Go-specific contract.
- No new quotas, timeouts, caps or customer restrictions were added. Connection
  retry behavior is unchanged.

The private controller was stopped and the process sweep found no remaining
executables under the lane directory. Temporary artifacts are removed after
this report is committed.

Binary SHA256 receipts before temporary cleanup:

- `sparkwing-before`: `5c52634139ba2df897d719bd652325ed766124217f4bc2995e527af48827b09b`
- `sparkwing-after`: `1eaf8559a2ba73554a7e8e9db82e8e927309b4d33fe599c85f20675989f7d2d5`

## Gate follow-up

Continued from `53f3ba436` after reading the lead's full gate output. The
per-cell reproductions above remain the evidence for the completed grid work.
This follow-up addresses the gate failures without repeating those scenarios.
All new CLI and Go invocations used the same private-home and Go environment
described above. Xwing reported no installed candidate for this environment.
The supplied lane contract selects direct worktree builds under its temporary
directory, so no candidate installer or global launcher was changed.

Before changes, this focused command reproduced three failing tests:

```bash
go -C "$W" test ./cmd/sparkwing ./internal/nodehost \
  ./sparkwing/sparkwingtest ./internal/wingd \
  -run 'TestRequiredProfileFlagsDoNotAdvertiseDefault|TestHostArgumentRulesAgreeForBothSDKs|TestHostPreparesArguments|TestJournalRecordsChildLineageAndCancelScope' \
  -count=1
```

- `TestHostArgumentRulesAgreeForBothSDKs` still expected enum errors without
  choices in three cases. `TestHostPreparesArguments` had the same stale
  expectation. Commit `7787c9508` updates those assertions to check the public
  choices through both SDK host paths, including generated arguments. Both
  tests pass after the update (`internal/nodehost`: 1.161 seconds,
  `sparkwing/sparkwingtest`: 0.054 seconds).
- `TestRequiredProfileFlagsDoNotAdvertiseDefault` found optional-profile
  default guidance on 11 required-profile routes. A freshly built baseline CLI
  reproduced it through `profiles show --help`. Commit `926bc5ae0` gives
  required-profile flags explicit-selection guidance. A rebuilt CLI passed
  assertions on all 11 routes and retained saved-default guidance for optional
  `runs list --profile`. Generated references and the embedded mirror were
  regenerated, and the changelog describes the correction.
- `TestJournalRecordsChildLineageAndCancelScope` passed unchanged in isolation
  (0.202 seconds). The lead's gate failure is not claimed fixed. Admission and
  journal behavior were left untouched because they are outside this lane's
  CLI usability scope.

Additional checks passed:

- The focused CLI/profile/timing/argument/scaffold regressions (5.555 seconds).
- Complete `internal/nodeproto` and `sparkwing/sparkwingtest` package tests.
- CLI rebuild and `go vet ./cmd/sparkwing`.
- CLI reference drift check, embedded-docs/changelog drift check, and
  `git diff --check`.

The full gate remains with the lead. No new mechanisms, limits, runtime
settings or routing changes were added in this follow-up. The help correction
uses the existing regression test, which failed before the change.

Follow-up binary SHA256 receipts before cleanup:

- Before: `c354d2a4e300c0152b392da9d58482b4397928e12828cbb187c159afc0bcb171`
- After: `89b313fb01f5d15bd2db8fb047b1f667a12be4280f83dfa7e4752e9ca167eea7`
