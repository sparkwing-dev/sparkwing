# W3 store admission continuation

<!-- design:begin -->

W3 remains incomplete. This continues `w3-local-admission-progress.md` and the
assignment audit in `contracts/w3-amendment-handoff.md`. Lead-owned handoffs and
amendments are unchanged. No acceptance cell is added or unskipped.

## Implemented

Native assembly now composes `StoreAdmission` with local admission. It uses
sealed plan and job settings, sorted tuple keys and C1's tenant methods. The
owner supplies its authenticated actor. Each acquisition first asks C1 whether
capacity was continuously retained. A grant or promotion must pass another
adoption check with the exact run/job/generation, holder and cost before work
starts. A malformed or superseded holder cannot release work.

Unit renewal owns capacity liveness. The host adds no holder heartbeat loop and
releases no store capacity when its process exits. Terminal transitions release
it. Owner loss retains a live waiter for its successor. Queue deadlines use the
persisted arrival, including on adoption. Queue expiry cancels the waiter.
Store outages retain the existing five-minute budget. No new timeout is added.

`NewNativeHost` connects this adapter before plan scheduling and job readiness,
including global groups on remotely dispatched jobs. With a local daemon,
run/box groups use the daemon and other groups use C1. A remote job requiring
that daemon's local semaphore lifetime still needs its adapter and fails loudly.
The constructor copies admission configuration before selecting these owners.
Store Skip proposes a skipped transition through C1's existing owner edge.
Admission failure and plan refusal still need the policy transitions below.

Invariant: execution can use only capacity C1 has confirmed for its exact
accepted work. The generated property changes run, job, key, holder, cost,
superseded flag, generation and owner run. None reaches work or a new acquire.
It passes 1,000 generated cases. Separate checks cover adoption without another
acquire, promotion, skip/fail/cancel_others contract responses, retained waiters,
expired adopted deadlines, store outage tolerance and combined local/store groups.
These use explicit C1 fakes. They do not establish production store policy.

## Retained failure fixed and deletion

`TestAdmissionLeaseEndings` now runs the common executor, its process cleanup and
its fenced reports. It first failed because eviction and rejected reattachment
both persisted cancelled. Local admission now carries a typed superseded cause,
and the executor preserves that outcome on its final report. Daemon cancellation
still reports cancelled. The box-group eviction case retains its group.

All three cases pass and both local leases release. The old 39-line function and
one trailing blank line are deleted. The existing native helper body was replaced
with the stronger port. Legacy hosting implementation deleted: **0 lines**.
The broader scheduler, Config, Execute, Session, layers and production callers
remain. Their deletion is required work.

## Real integration points and contract requests

- **C1 cancellation integration is incomplete.** A temporary SQLite probe created
  two live jobs, acquired a capacity-one group for the first, then acquired it
  with cancel_others for the second. Output was `first: granted`,
  `second: cancelling_others`, `superseded=1 bound_job_cancel_requested=0`.
  `txSupersedeHolderAndInherited` only marks holders superseded. C1 must join
  bound-work cancellation to this transaction under A60. `SupersedeCapacity`
  exists separately, but a host-side second transaction cannot provide that
  atomicity. No W1 file or foreign contract shape was changed. The probe is deleted.
- C1 still lacks owner pending-to-failed policy transitions, approval SkipIf
  success and typed debug operations. Native debug, approval and memo composition
  therefore remains open. Plan admission refusal also needs the owner to finish
  its pending nodes through those transitions.
- W3 still needs retained daemon semaphores around remotely executed work,
  successor reconciliation of already-ready work and complete memo source and
  terminal-record integration. The new admission adapter is not the production
  caller cutover. Its C1 cancellation and real SQLite/HTTP adapter joins remain
  open despite the fake-backed component passes.
- C3/W9 `JobTransitionRequest` still omits SourceAttempt. Its snapshot remains
  scheduling-only, without the full accepted execution read or typed step writer.
  `client.NewEngineClient` still panics. These prevent a correct remote native
  placement migration. No raw legacy writer substitutes for them.
- W8/W9 still need selected-engine host invocation and award/bootstrap transport.
  W8's runner still marshals the whole SealedRequest into the inner request field
  and retains prepared owner/runner identity. Those foreign callsites remain open.
- Cross-process dynamic-mask propagation remains unspecified. Native socket,
  api.sock, Windows/Darwin, Postgres and real-repository evidence remains with the
  lead. TypeScript dependencies are installed and are not a blocker.

The prior assignment audit remains in force for A1-01 through A1-07, A1-10
through A1-19, A1-21 through A1-25, A1-36, A1-38/39, A1-44 through A1-48,
A1-50/51, A1-53/54 and the W1 attempt/Executing handoffs. This continuation
advances native admission for A1-19/39/48/51 and the retained nodehost-test port.
It leaves the prior v2 router, eval decoder, original-byte validation, procenv
projection, tuple codec, alias and settlement implementations in place.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, at most four
packages, and GOCACHE/GOTMPDIR/TMPDIR under `/tmp/sw-astra-W3/`.

- Whole-repository build and scoped vet pass.
- Focused native host, owner, executor, admission, v2 router, SDK, environment,
  planning, reconciliation, settlement and service checks pass. The actual HTTP
  SDK/router test fails at listener creation because the sandbox forbids sockets.
- Native host, owner and admission race selection passes.
- Full source, lease and nodeproto suites pass.
- Capacity-binding fuzz passes 1,000 generated cases.
- Selected existing recovery, lifecycle, environment and credential hermetic/SQLite
  acceptance tests pass. No native placement cell is claimed complete.
- The initial full nodehost run failed on removed-schema legacy fixtures, then
  stopped at a forbidden HTTP listener. It finished without the old ten-minute
  hang. It is not a passing package suite.
- Commentcheck against main, sleepcheck against HEAD and whitespace checks pass.

The new mechanism is a host adapter over C1's existing capacity contract. Reusing
the legacy admission implementation would require mutable Run/NodeRun state and
its separate holder heartbeat loop. The adapter uses sealed inputs and unit
renewal instead. Remove it with its NativeHostConfig field to undo the assembly.
No credential source, user environment variable, service or compatibility alias
is added. Native placement remains incomplete.

Outside nodehost, changes are the C3 export inventory, the retained-test inventory
and this report. No foreign production caller changed. No Git writes, Sparkwing
binaries, pipelines, candidate installs or releases ran. Scratch is deleted after
all checks join.

<!-- design:end -->

STATUS: CONTINUE
