# W9 remaining contract boundaries

Base: `ce2588779`. This continuation changes only this report. No new foreign
implementation or contract has landed since the previous continuation. The drain
fix remains committed in `112188414`. The lead's native local-controller and
local-schedule passes stand. The SDK replacement policy is unchanged.

## Current evidence

The shutdown invariant is that closing the controller API cannot cancel the
admission connection before its drain acknowledgement is written.
`TestLocalRuntimeDrainAcknowledgesBeforeClosingConnection` passes, including
under the race detector. It holds the acknowledgement reader until API shutdown
runs, then requires the acknowledgement and joined controller cleanup. Drain
client, service-installation, native construction and separate owner/eval binder
regressions also pass. No further shutdown patch was made without a new repro.

The combined public doctor probe was run against this base:

```text
go test -p 4 -tags rewrite_integration ./cmd/sparkwing -run '^TestReviewPublicDoctorSweepsAndWarnsForPrunedRun$' -count=1 -timeout=90s -v
```

It fails before the sweep because connecting to `d.sock` returns
`operation not permitted`. Cleanup encounters the same socket refusal.
This does not reproduce the reported drain acknowledgement failure or establish
a native cleanup pass. The lead must rerun this test natively against
`112188414` or a descendant to check the sweep and shutdown together.
The socket-free controller sweep still passes with a pruned run and reports
`unrecorded_step_outcome`. Public CLI warning tests pass.

Findings 7-9 retain their implementations: complete process/storage bindings,
shared log segments and record/object publication, and service installation that
joins the old owner before activating its successor. The private JSONL writer
remains deleted. Local peer intake commits through the shared relation and
committing authority check. Its HTTP-to-SQLite property passes for generated
peer identities, actor forgery, foreign teams, replay and revocation.

The complete installed-runner done test was also run:

```text
go test -p 4 -tags rewrite_integration ./internal/cluster -run '^TestInstalledRunnerRuntimeComposition$' -count=1 -timeout=90s -v
```

Start and warm pass by reaching `GET sparkwing.dev/versions.json`. Fetch-source
passes by reaching `POST controller.example/api/v1/runs/run/source-credential`.
Run-node passes by reaching `POST controller.example/api/v1/claims/exchange`.
Launch fails with `launcher runtime is unavailable`. These four passes prove
composition through the first production HTTP operation, not completed runs.
The full done test remains red. Its assertion and production error are unchanged.

## Contract changes needed

1. **W2/W3 owner/eval process authority.** Invariant: evaluating an owner's source
   job neither acquires nor impersonates a work attempt. The current
   `CredentialNodeProcess` rule in `internal/capability/policy.go` requires
   `BindingNode`, a holder, positive generation and positive attempt.
   `ProcessAuthorities.Delegate` requires the service job to equal that bound
   node. `NativeProcesses.processServices` enforces the same relation at startup.
   W2/W3 must declare a process credential and source-job relation that retain
   the owner/eval fence independently of a work attempt. The proposed shape is
   an owner/eval lease binding plus a separate source-job selector, both checked
   against fresh persisted authority. W9 can then implement
   `NativeConfig.Evaluate` through shared delegation. Its separate binder and
   fake-contract regression already work before any work award. Production
   construction currently leaves the binder unset and returns `policy_missing`.
   No node attempt or authority was fabricated.
2. **W8 production Cloud boundary.** Invariant: launch uses actual network
   enforcement and reservation-scoped teardown before node reuse.
   `KubernetesBoundary.Probe` and `Clear` remain required deployment callbacks
   with only test implementations. W8 must supply these implementations or a
   complete production `CloudBoundary` constructor. W9 can then connect
   `ProductionRuntime.Launcher` to `NewLauncher` and the shared authenticated
   transport. The existing launcher contract accepts a fake boundary for
   component tests. A no-op boundary, manifest-derived enforcement claim or
   error-only production factory would leave the required deployment behavior
   unimplemented, so none was added to satisfy the entrypoint test.
3. **Korey SDK pin policy.** The unpublished SDK replacement decision remains
   reserved. No pin reader, source acquisition rule or test expectation changed.
   Local-run acceptance remains pending that decision and native execution.

These pieces remain stopped at the foreign contract boundary required by the
swarm brief. No other area's declaration was changed. No permission request or
new policy decision is needed for the already authorized W9 adapters once their
owners provide those contracts.

Other real integrations remain recorded in [W9-HANDOFF.md](W9-HANDOFF.md),
[W9-RUN-PAGES-CONTINUATION.md](W9-RUN-PAGES-CONTINUATION.md) and
[W9-LOCAL-SDK-CONTINUATION.md](W9-LOCAL-SDK-CONTINUATION.md): canonical remote
placement eligibility, Cloud plan/eval hosting, bootstrap checkout consumption,
cross-run record/read-set proofs, sealed scheduled intake, public step/receipt
projections, and the remaining here/cache/publication bindings. Their current
missing-policy guards were inspected. No completion is claimed for them.
Lead-owned handoffs and amendments were read and left unchanged.

## Checks and scope

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W9/go` and
`TMPDIR=/tmp/sw-astra-W9`. Package parallelism was at most four.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Vet for wingd and subpackages, cluster, CLI and controller/client.
- Focused drain, service-installation, native construction and separate
  owner/eval binder regressions in wingd, wingd/client and localcontroller.
- The drain acknowledgement regression under the race detector.
- Doctor sweep/warning and peer-intake property selections in controller and CLI.
- Surface and engine-amendment acceptance selections.
- Commentcheck against main and `git diff --check`.

Failed: the combined public doctor probe at the sandbox socket boundary, and the
full installed-runner composition probe at launch's missing production factory.
No acceptance cell was newly unskipped or claimed as a native pass. No archived
test was newly ported.

Production additions/deletions in this continuation: **0/0**. Recounted W9-owned
production Go versus main: **+16,876/-23,797, net -6,921**, within the -2,900
budget. Tests, testdata and contract declarations are excluded. The only changed
file is this report. No shared source, API or caller changed.

No worker daemon or child survived the probes. Scratch is removed after checks
join, and `/tmp/sw-gocache` is retained. Git reads Korey's global identity and
has no core.worktree override. No Git write, candidate installation, operational
Sparkwing command, pipeline, deployment or release ran. Friction remains the
foreign contracts above, native socket/service-manager verification and the
installed Overwing binary's reported source lag.

STATUS: CONTINUE
