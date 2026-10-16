# W9 service encoding and run status

The example's `weather-check` job uses the function-backed step `run` and calls
`RunAndAwait` for `weather-report`, output job `stamp`. Its empty arguments map
was encoded as `args: null`. The zero-value service conformance test reproduced
the exact host error and also found cleanup's `argv: null`.

The invariant is that SDK service requests never emit null required collections.
Every Go service request now passes through one encoder that copies and
normalizes nil maps and slices, including nested collections. Caller values stay
unchanged. The TypeScript child request already supplied `{}`. Its shared
encoder now refuses null fields and nonfinite numbers before transport.
Both SDKs' six write-request shapes pass the host's actual strict decoder with
zero or empty values. TypeScript's five bodyless requests are covered too.

Default run status shows every job's status and outcome, its status detail,
and recorded failing steps with their attempt and masked error. This applies to
plain and pretty output. JSON includes the failing steps. `--steps` includes all
recorded steps. A successful run containing a failed optional job still exits 0.

The database-backed HTTP regression exposed another broken connection. The
steps endpoint returned raw store fields instead of `StepDetail`, and the store
reader omitted persisted attempt and error columns. The reader now returns
those existing columns and the endpoint projects the declared public wire type.
The shared client receives job ID, step ID, attempt, outcome and masked reason
through the real authenticated controller and SQLite. No schema, grant or wire
declaration changed.

The persisted scheduler regression now requires the optional job's stored
outcome to be failed and the stored run outcome to be success. It passes.
No optional-failure scheduling change was needed.

Verification used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, and task scratch under `/tmp/sw-astra-W9`.

- Root and `.sparkwing`: `go build -p 4 ./...` and `go vet -p 4 ./...` pass.
- Full sdkserve, clisurface, controller/client and doccheck package tests pass.
- Focused nodehost, CLI, controller and store regressions pass, including the
  strict service decoder, persisted step reads, step report fences and optional
  failure settlement. The SDK child/RunAndAwait selection passes.
- Encoder fuzzing completed 100 executions. TypeScript's 20 test files and
  `npm run typecheck` pass.
- Commentcheck against main, whitespace and documentation mirror checks pass.
  CLI reference pages were regenerated through the existing generator inside a
  temporary test overlay. API snapshots were regenerated. That also refreshed
  pre-existing snapshot drift in controller, client and wingwire declarations.

The full doccheck executable fails on unchanged source documents. Its five
banned-token hits are in `data-uploads.md`, `environment-variables.md` and
`storage.md`. Its sidebar check also reports `debugging.md` as unlisted.
Those four documents match HEAD. The other full-doccheck sections pass.
The native example rerun and independent landing review remain with the lead.
The swarm brief prohibits workers from running Sparkwing binaries.

No acceptance cell was unskipped and no archived test was ported. No production
file was deleted. Production Go this session is +105/-20, net +85 lines.
W9-owned production Go versus main is +16,922/-23,812, net -6,890 lines,
within the -2,900 budget. TypeScript production changes are +9/-1.

Shared files touched are `internal/sdkserve/routes.go`, the new shared encoder
and their tests; `internal/nodehost/service_encoding_test.go` and
`scheduler_test.go`; `sdk/typescript/src/routes.ts`, its route tests and the
service-request fixture; `pkg/store/store.go`; `pkg/controller/handlers.go`,
`step_status.go` and its native test; generated `.apidiff` snapshots and embedded
documentation copies. Other changes are W9 CLI, help, docs and this report.
Lead-owned handoffs and amendments are unchanged.

Contract changes needed in other areas: none. Open implementation questions:
none. The requested shared encoding boundary replaces direct marshaling. No
setting, environment variable, retry, timeout or compatibility path was added.
No Git write ran. Git has no local identity or core.worktree override. All
task-owned processes finished and scratch was removed. The shared cache remains.

STATUS: COMPLETE
