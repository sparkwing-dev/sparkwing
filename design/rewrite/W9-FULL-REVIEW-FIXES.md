# W9 full-area review fixes

Session base: `b3ad49db1`. No Git writes. Lead-owned amendments and handoffs
are unchanged. Rulings 1-4, 6 and 8-12 and all five simplifications retain their
earlier implementations. Rulings 5 and 7 remain open.

Implemented this session:

- Local `--sw-no-bincache` reaches inspection, public pipeline help, detached
  hosting and retries. The selected engine builds an owned Go executable with
  debug information and no binary-cache publication.
- Local `--sw-hash-all-files` reaches the same paths. Each materialization gets
  its own policy. Changed ignored embed inputs change the executable. One
  installation cannot change another installation's policy.
- Local relative `--sw-priority front|back` reads the real controller queue
  immediately before intake and seals the integer with flag provenance. Empty
  queues produce +1 and -1. Integer overflow fails before submission.
- Describe-cache writes use the materialized binary's key. Uncached help skips
  describe-cache reads and writes. An explicit build policy cannot replace the
  default binary's cached description.

Deleted the process-global `SetHashAllFiles` API and atomic state. Its sole
caller was a test, migrated to an explicit per-call policy. Deleted duplicate
hashing before materialization and uncached help's describe-cache population.
No whole production file was deleted. Production accounting versus this session
base is +83/-60, net +23 across all packages. W9 alone is +34/-28, net +6. W9
versus main is +19,455/-25,168, net -5,713, within its -2,900 budget. Counts
exclude tests, testdata and `_contract.go` declarations.

The invariant is that a local run's binary policy survives inspection, sealing
and retry and controls the executable actually built. New regressions ran red
before implementation. They use the public parser, real preparation, Go
compiler, strict installer JSON, source acquisition, SDK protocol and binary
metadata. Extending coverage to public help separately ran red on the stale
description. The priority regression runs the command handler with real native
preparation and the real controller over `net.Pipe`. It replaces neither
preparation nor the controller with a fake. Native socket listeners remain the
lead's integration lane.

The existing hash-all regression now uses a per-call policy rather than mutating
a global. Other changed tests migrate private signatures. No archived inventory
test was newly ported or acceptance cell newly unskipped.

Contract changes made:

- C4 records optional `runspec.Source.NoBincache` and `HashAllFiles` booleans.
  Source projection carries them into acquisition and installation after retry.
- C3 engine records the corresponding optional `SDKInstallRequest` fields and
  per-materialization `bincache.CompileOptions.HashAllFiles`.
- C4 records local relative-priority resolution before sealing.

Assumptions and mechanisms added: the two retained source fields are necessary
because installation runs in another process, and detached hosts and retries
cannot recover CLI closures. Keeping them only in private CLI options would
lose the selection. Removing the fields and restoring the refusals reverses the
change. There is no new flag, config key, environment variable, grant, route,
store table, compatibility adapter or execution limit. Relative priority selects
a rank from the observed queue under wingwire's existing contract. It does not
reserve that position across concurrent submissions.

| Option | Kept or cut | Source and state |
| --- | --- | --- |
| Relative --sw-priority | Kept | C4 Registry/run, settings priority and wingwire contract. Local execution implemented. Cloud overflow binding remains. |
| --sw-no-bincache | Kept | C4 Registry/run and existing uncached compiler. Local execution and help implemented. Cloud overflow inspection binding remains. |
| --sw-hash-all-files | Kept | C4 Registry/run and existing hash policy. Local execution and help implemented without global state. Cloud overflow inspection binding remains. |
| --sw-allow-unadmitted | Kept, pending | C4 Registry/run and decision 91. Its explicit refusal remains. |
| --sw-index | Kept, pending | C4 Registry/run and procenv.Runtime.GateIndex. It needs a retained, verified private file snapshot. |
| --target | Kept, pending | C4 Registry/run. It needs a declared-target binding through preparation. |
| --sw-request-id | Kept, implemented earlier | C4 Registry/run and detached tracing contract. |
| Hooks --fleet and --all | Cut earlier | Lead ruling 4 and C4 --all-repos grammar. No aliases. |
| profiles set --token | Cut earlier | Lead ruling 12. Use --token-stdin. |
| profiles add --scope | Cut earlier | Lead simplification 5. Use member credentials. |

No run option was cut. Cloud overflow retains the previous refusal for relative
priority and the two binary selections. Its inspection acquires an SDK entry
and then invokes a second engine describe command. That second preparation still
needs the selected policy. Merely copying the source fields there would leave
inspection using defaults. This path remains refused until its binding is done.

Remaining work:

1. Ruling 5: capture present, allow-listed `run.submit_env_allow` values, retain
   and transport them privately, consume them in hosts and mask them. Trigger's
   unconditional refusal remains. Values must stay out of public names/digest
   snapshots. The private local environment owner exists. Remote retention and
   delivery remain to be connected.
2. Ruling 7: bind the three remaining local options and the three Cloud overflow
   selections above. Keep one inspection owner rather than adding another
   parser or ambient control variable.
3. Diagnose the native burst cache failure. The latest report contains
   `TestBurstPublicRunReadsProfileCacheBeforeNativeExecution/attached` failing
   with `lock_held` / `holder_rights` during here handoff. No cause was established
   and no speculative fix was made. The harness also targets the deleted
   `internal/projectconfig`; its replacement is `pkg/projectconfig`.
4. Lead review and native verification. Native Windows/Darwin, deployed Cloud,
   DinD, PostgreSQL, fresh-machine distribution and A1-53 remain lead integration
   lanes carried from the prior report.

The prior amendment/handoff audit still applies: A1-09/16/17/38/41/43/49/50/51/52,
source attempts, step reports/windows, pipeline identity, launcher grammar,
doctor, local composition, owner/eval separation, shared logs, service handover
and decisions 110/111/113/114 retain their implementations. No foreign stub
blocked this session. No further contract change is needed to review this slice.

Shared production files touched: `internal/runspec/contracts.go`,
`internal/source/{source,projection,acquire}.go`, `internal/engine/install.go`,
`internal/engine/sdkinstall/install.go`, and
`internal/bincache/{bincache,hashignore,materialize}.go`. Other-area test caller
migrations are `internal/bincache/hashignore_test.go` and
`internal/engine/sdkinstall/go_inputs_test.go`. W9 caller migrations also touch
`burst_intake_unix_test.go`, `local_describe_unix_test.go`,
`run_request_id_test.go`, `compile_local_unix_test.go` and
`pipeline_identity_unix_test.go`. Source docs, changelog, generated mirrors and
the two named contracts were updated together.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
TMPDIR=/tmp/sw-astra-W9 and GOTMPDIR=/tmp/sw-astra-W9/go, with at most four package
workers. Reproducible focused commands, from the supplied worktree:

```sh
go build -p 4 ./...
go vet -p 4 ./cmd/sparkwing ./internal/bincache ./internal/engine/... ./internal/runspec ./internal/source
go test -p 4 -short ./internal/bincache ./internal/engine/... ./internal/runspec ./internal/source
go test -p 4 ./cmd/sparkwing ./internal/engine/sdkinstall -run 'Test(RunBuildPolicy|RunRelativePriority|SDKInstallUncached|SDKInstallHashAll|NativeLocalPreparation|LocalRun|LocalForeground|LocalIntake|DetachedRequestID)' -count=1
go test -p 4 ./tests/acceptance -run 'Test(Surface|EngineAmendments)' -count=1
go run ./internal/commentcheck -base main .
go run ./internal/sleepcheck -base HEAD .
go run ./internal/doccheck ./docs .
bash bin/sync-docs.sh --check
git diff --check
```

All pass. All 12 tests in changed CLI test files also pass without `-short`,
excluding the migrated burst intake file in the native lane. No full native
CLI/bin suite was run. The lead's native report still has FAIL, so this area is
not complete. Git's local user.name and core.worktree remain absent. Scratch is
removed after checks join. The shared cache is kept.

Friction: one test-selection script inherited the default read-only Go cache
and could not compile. Re-running with the required shared-cache environment
passed. The installer fixture initially tried a network module lookup. A real
local SDK replacement made it offline. Overwing reports its binary behind
source. None blocked this slice.

STATUS: CONTINUE
