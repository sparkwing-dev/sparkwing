# W9 continuation: authorized debug resume

Base: `d67819a04`. The drain fix, shared logs, peer intake and service handover
remain implemented. The SDK pin policy is unchanged. This continuation connects
another unfinished W9 client adapter to an existing controller transaction.

## Implemented and deleted

`debug resume` now resolves the selected controller, reads the selected pause and
releases its observed stable run/job identity through the same authenticated
transport. It uses api.sock locally and the saved connection for an explicit
profile. The obsolete unconditional `policy_missing` refusal is removed.

The invariant is that pause release requires the selected job's current
controller authority. The request contains only `{}`. The controller derives
attribution and rechecks `runs.control` in the committing transaction. The client
requires an empty HTTP 204 acknowledgement and uses the existing safe replay
policy. CLI success output follows that acknowledgement.

The preflight reads the pause because releasing it needs no executor diagnostics.
Unknown profiles fail without opening local state. Read, authorization and write
failures produce no successful receipt. JSON, plain and pretty output remain.

The W9-owned C3 contract adds `DebugControl` and its shared client constructor.
No foreign contract, route, grant, authority rule or persistence API changed.
The debugging guide and generated embedded mirror describe the working adapter.

## Evidence

The configured-controller regression failed before implementation with zero
HTTP requests and the obsolete policy refusal. It now passes through the real
resolver. The real client-to-controller-to-SQLite property covers successful
release, repeated release, credential revocation, membership removal, missing
control authority and generated existing foreign-team targets. It checks
persisted release attribution, unchanged foreign pauses and exactly one event
for successful repeats. The property passes under the race detector and its
final fuzz run completed 10 executions. The client route-selector fuzz completed
24,802 executions. No test uses a sleep.

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR=/tmp/sw-astra-W9/go, TMPDIR=/tmp/sw-astra-W9 and at most four workers:

- Whole-repository `go build -p 4 ./...` and focused vet.
- Resume CLI, client, real controller/store and surface acceptance regressions.
- Resume, peer-intake and doctor sweep selections under the race detector.
- Drain acknowledgement, service installation, native construction and separate
  owner/eval binder regressions.
- Full short controllerwire, controller/client and doccheck suites.
- Surface acceptance selections, embedded docs reads and mirror-drift checks.
- Commentcheck against main and `git diff --check`.

The full docs suite could not finish because its WebClient tests require TCP
sockets. The public doctor integration probe also stops at sandbox socket
refusal, before the sweep. Its native drain-cleanup result is still unverified.
The socket-free pruned-run sweep and drain regressions pass.

The installed-runner integration probe still passes start, warm, fetch-source
and run-node through their first production HTTP operation. Launch still fails
with `launcher runtime is unavailable`. Its test and error are unchanged.
No native acceptance cell was marked complete. The existing debug surface test
now covers release as `TestSurfaceDebugControlTransport`. No archived test was
newly ported, and no old production file was deleted in this continuation.

## Contract changes needed and open integrations

1. W2/W3 owner/eval process authority remains unavailable. CredentialNodeProcess
   requires a positive node attempt, and ProcessAuthorities ties the SDK job to
   that node binding. Supply a separate owner/eval fence and source-job relation.
   The existing W9 binder and fake contract remain ready for that adapter.
2. W8 must supply actual Cloud network-enforcement probes and reservation-scoped
   cleanup. KubernetesBoundary still has no production Probe/Clear callbacks.
   The shipped launch factory remains unbound until that boundary exists.
3. W2's run decoder still resolves stable IDs only. The new native pause-read
   probe with `run=1` returned `resource not found` for a persisted run numbered
   1. Debug clients retain their declared number-selector contract, with fake
   transport coverage, but real number resolution needs authenticated tenant
   scope. Supply that shared resolver rather than an unscoped number lookup.
   Stable-ID pause reads and release through the real store pass. The separate
   public job route still returns JobDetail rather than the declared DebugJob
   projection, so debug env/attach retain their existing W2 projection handoff.
4. Korey's unpublished SDK replacement decision remains reserved. No pin reader,
   source-acquisition policy or expected SDK-policy failure changed.

Other open integrations remain in W9-CONTRACT-BOUNDARIES-CONTINUATION.md and its
linked reports: placement, Cloud plan/eval hosting, source checkout consumption,
cross-run record proofs, scheduled sealed intake and here/cache/publication.
Lead-owned handoffs and amendments were read and left unchanged.

## Scope and friction

Production Go additions/deletions: **+42/-10, net +32**. W9 versus main:
**+16,908/-23,797, net -6,889**, within the -2,900 budget. The deleted lines include
the obsolete refusal and separate function-field runtime bindings.

Shared edits outside W9-owned paths are only
`internal/controller/debug_client_native_test.go` and
`tests/acceptance/debug_contract_test.go`, plus the generated
`pkg/docs/mirror/debugging.md`. No production API was removed, so no foreign
caller migration was needed. The local C3 addition is recorded above.

The added interface replaces the CLI's separate reader/function bindings and has
a test double. No new flag, environment variable, retry mechanism or policy was
added. No Git write, candidate installation, operational Sparkwing command,
pipeline, deployment or release ran. Git has no local identity or core.worktree
override. No worker process remains. Scratch is removed after checks join and
the shared cache is retained. Friction remains native socket/service-manager
verification, the foreign contracts above and Overwing's reported source lag.

STATUS: CONTINUE
