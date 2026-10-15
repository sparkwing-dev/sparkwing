# W8 contract retirement

Removed the four dead policy catalogs assigned by the lead's 09:40 handoff:
`KeepAliveReasons`, `LocalPlatformPolicies`, `CloudUnitPolicies` and
`CloudPodInvariants`. Their unused element types and constants are removed too.
The runtime, platform transports and Job builder continue to enforce their
contracts. No caller depended on these catalogs.

Implemented declarations now live in `engine.go`, `install.go`, `upgrade.go`,
`runners/update.go`, `launcher/job_types.go` and `wingd/lifecycle.go`. Six
contract declaration files are deleted. `BuildJob` and `Resolved.InstallSDK`
now contain their implementations directly. Their private forwarding targets
and public forwarding bodies are removed. Retained public signatures and wire payloads
are unchanged. API comments retain one-line descriptions. The engine export
inventory and W8 entries in the surface contract link to the real files.

## Priority and amendment disposition

- Lead item 5 remains implemented: `LocalController.HTTP() LocalHTTP` supplies
  handlers, while the runtime owns cancellation and joined HTTP shutdown.
  W2's production handlers and local adapter are still required.
- Lead item 6 remains implemented: the shared launcher grammar and runtime
  accept digest-pinned `--docker-image` and reject `--deadline`. The cluster
  parser/runtime tests pass.
- Lead item 10 remains implemented on the launcher side. Observations carry
  operational kind, unit ID, holder, claim generation, actual Job namespace,
  name and UID, and node UID. Eval source generation stays separate from the
  live eval lease generation. Responses correlate awards across overlapping
  generations, and teardown uses the observed Job UID precondition.
  **A1-38 remains open end to end.**
- The cluster worker, warm-pool dispatcher, run-host Kubernetes dispatcher and
  old W8 pod builders remain deleted by earlier commits. No replacement legacy
  dispatcher or second Job builder was introduced.
- The other W8 amendment entries retain the implementations and integration
  obligations recorded in [the prior report](W8-BOOTSTRAP-DEADLINE.md). This
  continuation changes no issuance, enrollment, billing, source, process,
  storage, scheduling or platform contract.

## Production lines and budget

Against starting HEAD `b7fc2ad45`, production Go adds 521 physical lines and
removes 676, including the moved declarations. **Net production lines deleted:
155.** Tests, testdata and Markdown are excluded. Prior legacy deletions are
not counted again.

Across the W8 ownership paths against `main`, the resulting net is **+4,837
production Go lines**, or **-4,837 net lines deleted**. The lead's budget is
-2,300, so W8 remains **7,137 lines over budget**. Moving real declarations out
of excluded contract files makes the lead's counted delta increase by 519 this
session, despite the physical reduction of 155. No code was moved into an
excluded directory to change that metric.

The overage needs lead review. The summary estimate did not separately size
D102-103's signed version policy, exact engine release verification, machine
cache and native SDK upgrade implementation (+2,233 lines in engine), D91's
local process/HTTP/service lifecycle, or managed runner services. The latter
services add 1,055 lines, local wingd paths add 2,672, and the sole Cloud
launcher adds 1,113. Cluster and other runner paths remove 2,236. These are
shared implementations of the ratified features. Removing the resolver or
platform service implementations would remove those features. This is a
request to review and ratify the overage, not a claim that W8 meets its budget.

## Real integrations and contract needs

No new foreign contract change is requested. Existing open integrations are:

- W1/W2 must bind `LauncherSyncReader.ReadLauncherSync` to transactional current
  authority. Only the interface, controller delegation and fake readers exist.
  W2's claim projection still clears plan/eval `Execution`, so those awards lack
  the resolved resources required by the launcher.
- W2 must supply local state/HTTP, provisioning and claimant enrollment over
  `localcontroller.Binding`'s sole store handle. W8's election, store owner,
  queue, maintenance, readiness and drain composition are implemented against
  the declared interfaces and tested with SQLite and fakes. The CLI's
  `resolveLocalServices` still reports unavailable.
- W3 must supply Cloud plan/eval lifecycle and initialized-checkout adoption
  contracts, remote `NativeHostStore`, and final native service bindings.
  W9's source credential client and W2's response still need a shared shape.
- W8 still owes final executable composition and deployed `Probe`/`Clear`
  bindings. Enforced network isolation, node cleanup, native Kubernetes,
  Docker, Windows/Darwin and real-repository QA remain open with T1/lead.

No new product or security decision was made. No native acceptance cell or
archived test port is newly closed. No new tests or test-only cells were added.
The existing W8 acceptance selection remains passing.

## Verification and shared files

Commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, at most
four package workers, `GOCACHE=/tmp/sw-gocache`, and private temp directories
under `/tmp/sw-astra-W8`.

- Whole-repository `go build -p 4 ./...` passed.
- Focused vet passed for runners, engine, localcontroller, cluster and acceptance.
- Full runners, engine and localcontroller suites passed.
- Local runtime/lifecycle/managed-unit tests passed.
- `tests/acceptance -run 'W8|LauncherCloud|LauncherController|LauncherPolling'`
  passed, retaining store, runner, launcher, bootstrap, local-controller,
  managed-service and engine component evidence.
- Cluster passed with its socket-binding metrics test excluded. Clisurface and
  the corrected chart-package suite passed.
- Commentcheck against main passed across 2,123 Go files. Diff whitespace and
  current W8 contract-link checks passed.

The only shared file edited is `contracts/c4-surface-config.md`, for W8 export
links and removal of its dead export rows. `contracts/c3-engine-version.md` is
W8-owned. No production caller outside W8 required migration. Lead-owned
handoffs and amendments were read and left unchanged.

Friction: the first chart command named a nonexistent subdirectory. The corrected
check uses the chart package root. Overwing reports its installed binary behind
source. Native sockets and Git metadata writes remain outside this worker's
sandbox contract. No Sparkwing binary, deployment, pipeline, release or Git write
ran. The local Git configuration has no identity or core.worktree override.
All check processes joined. Scratch was removed and the shared build cache was kept.
The lead retains independent review and commits.

STATUS: CONTINUE
