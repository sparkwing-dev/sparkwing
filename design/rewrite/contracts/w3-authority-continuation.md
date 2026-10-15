# W3 process authority continuation

<!-- design:begin -->

W3 remains incomplete. This continues the
[execution diagnostics work](w3-diagnostics-continuation.md). The
[execution audit](w3-execution-continuation.md) still governs unchanged assignments.

## Merge

The working contents of `tests/acceptance/protocol_test.go` and
`tests/acceptance/skeleton_test.go` retain both W3's host checks and the incoming
TypeScript checks. Neither file contains conflict markers. The index remains
unmerged because the swarm brief forbids Git writes. The lead must stage both
resolutions and finish the merge. No contract conflict was present.

## Implemented

The invariant is that a process bearer retains its delegator's ancestry and the
authorized attempt fence, and revocation prevents further service use.

`ProcessAuthorities` owns node-host delegations in memory. It uses C2's
`AuthenticationReader`, `ValidateAuthority` and `ValidateDelegation` interfaces.
The shared validators remain the default. Tests replace their unfinished C2
implementations with explicit fakes. The adapter issues 256 random bits with the
`swn_` prefix, installs the bearer in the shared masker before delivery, and keeps
only its digest as the lookup key. It copies the narrowed authority and accepted
service declarations so callers cannot change them after issuance.

Authentication snapshots prepend the process authority to a fresh parent chain.
They retain the parent's store time, binding, cancellation, result and expansion
facts. Process lifetime is the extra local fact. Revocation during a parent read
prevents that read from returning a live process. Registry shutdown cancels every
process, refuses later admissions and cannot be undone by an in-flight admission.
Known identity keeps its recorded audience even when a lookup names another
audience. Controller lookups go to the parent reader. C2 owns the audience verdict,
so the memory reader cannot turn a live audience refusal into unknown identity.

`CheckBinding` rejects foreign run/team/job selectors and undeclared secrets before
resource lookup. Cleanup IDs belong to the selected process's ledger. Read-set,
child and persisted secret checks use the supplied `BindingChecker` with the
narrowed process principal. They do not substitute the broader parent principal.
That checker must understand the sealed process binding. Same-team or same-run
membership alone still grants no output or child read.

The existing eleven service registrations can use `ProcessAuthorities.Resolve`.
It requires the exact registered principal and full attempt fence. Their request
contexts now follow process lifetime. A revoked request cannot acknowledge
success, including when its writer returns nil after cancellation.

`ProtocolSession` tests connect its existing revocation callback to the real memory
adapter. Real child processes cover normal exit and rejected hello. Failed spawn
also revokes the bearer. Cleanup observes revoked authority. The pipe peer and C2
validators are fakes. This does not establish native HTTP or SDK route parity.

## Acceptance and retained tests

`TestCredentials/credentials-delegation-subset/hermetic-process-authority` now
exercises the adapter, exact attempt fence, mask-before-delivery, registered summary
handler, fresh parent membership and revocation. C2 validation, parent persistence
and resource relations are fakes. Native placement cells remain blocked.

The retained admission, invalid-bearer and revoke/close assertions from
`routes_test.go` have memory-adapter counterparts. The old HTTP tests stay because
their final-router replacement still needs C2. No legacy code or test is deleted
in this continuation. The deletion count is **0 lines**.

`FuzzProcessAuthorityFenceAndRevocation` generates run, job, holder, generation,
attempt and revocation changes. Only the registered, unchanged fence resolves.
Separate tests cover unknown/truncated/extended tokens, preserved audience facts,
credential class mismatches, immutable snapshots, closed admission and in-flight revocation.

## Assignment audit and real integration points

| Assignment | Disposition |
| --- | --- |
| A1-01/02/05/21-25 and router handoff 2 | The service bodies remain implemented. The memory delegation, fresh parent snapshots, process binding checks and revocation adapter are added. C2 authentication, policy-router assembly and native route evidence remain open. |
| A1-03/04/06/07/10-18 | Existing canonical errors, limits, cancellation, concurrency, source, reconciliation, recovery, expansion and attempt implementations remain. The remote step/failure/reuse and execution-snapshot requests in the prior audit remain open. |
| A1-19/38/39 and identity handoff 3 | Common constructor/hello and keeper evidence remains. Native runner, worker, pod and CLI assembly still need verified engine and granted identity inputs. |
| A1-36/44-48 and handoffs 7/9 | Existing storage, captured environments, aliases, secret classifications, guards and startup/cleanup projections remain. Owner policy composition and native callers remain open. |
| A1-50/51 and retained-test handoff 1 | Existing event and diagnostic ports remain. The old Session, Server, scheduler, layers and runtime fixtures still need deletion with their callers. |
| A1-53/54 and handoffs 4/5/6 | Eval failure, strict expansion and settlement components remain implemented. Native placements and approval-policy integration remain open. |

No other area's contract changes here. W3 adds its memory-adapter API to the C3
export inventory. The required parent reader is C2's existing contract.

The remaining cross-area requests are:

- W2 must implement `NewAuthenticator`, `ValidateAuthority`, `ValidateDelegation`,
  `Authorize` and `PolicyRouter`. Assembly must authenticate before resolving
  services. `Resolve` consumes an already-authorized value.
- W2/W9 must supply the fresh parent-authentication snapshot adapter for remote
  hosts. No controller route for that projection is declared. This work adds no
  route or cached-parent fallback. Local hosts can use C1's declared reader.
- The bound resource checker must resolve the node-host principal's sealed
  read-set, declared refs and child edges. Passing its wider controller parent
  would discard narrowing.
- W9/W2 still need typed fenced step reports, complete restricted execution
  snapshots, persisted `JobFailure` on `JobResult`, and terminal result/state
  sequence on `ReuseSource`.
- W6/W9 still need sealed StartAt/StopAt/Only selections. C1 still needs the
  owner-policy failure and approval-bypass edges and persisted approval timing
  and resolution identity recorded in the prior audit.

The expansion-concurrency mismatch question remains with W4 and the lead.
Full native HTTP/api.sock, placement, storage, Darwin and Windows evidence remains
open. The implementation does not replace the old Server with a second router.

## Scope and verification

The memory owner is required because the old `Server.Admit` carries neither
delegation ancestry nor attempt authority. Removing this addition means removing
the registry, its binding adapter and the service lifetime connection together.
It adds no setting, environment variable, service or customer limit.

Shared implementation changes are limited to
`internal/acceptance/host_process_authority.go`,
`tests/acceptance/protocol_test.go` and `tests/acceptance/skeleton_test.go`.
The latter two also contain the merge resolutions. No production caller outside
W3 changes. Shared contract notes are `c3-protocol-sdk.md`, `handoffs.md` and
`t1-port-inventory.md`, plus this continuation.

Go checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, `-p 4`
and cache/temp directories under `/tmp/sw-astra-W3`. The final report records
completed checks and scratch cleanup.

- Whole-repository build and scoped vet pass.
- Source, lease and internal acceptance suites pass.
- Current-host tests and the W3 acceptance-component selection pass.
- Process-authority, service and native lifecycle race checks pass. The final
  five-second fuzz run passes 1,935 executions with two workers.
- Commentcheck against main passes. Whitespace and conflict-marker scans are clean.
- The full acceptance run cannot complete the incoming TypeScript native fixture
  because `sdk/typescript/node_modules/typescript/bin/tsc` is absent. No TypeScript
  code changes here. Its restored checks remain enabled.
- Broad legacy nodehost/orchestrator suites, native HTTP and other platforms are
  not established by this component coverage. The lead retains pipelines and review.

No Sparkwing executable, installer, Git write, deployment or release runs here.
The Git configuration has no local user.name or core.worktree override. Scratch
under `/tmp/sw-astra-W3` is removed before the final report.

<!-- design:end -->
