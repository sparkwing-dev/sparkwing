# W3 local admission continuation

<!-- design:begin -->

W3 remains incomplete. This continues `w3-owner-renewal-progress.md` and the
amendment audit in `contracts/w3-amendment-handoff.md`. No acceptance cell was
added, unskipped or marked complete.

## Merge

The only incoming conflict was `contracts/t1-port-inventory.md`. Its contents now
retain the integration branch's entries and W3's unique owner-assembly paragraph.
Each prior W3 port note appears once. No conflict marker remains. The lead must
stage this file and finish the merge because the swarm brief forbids worker Git
writes. Lead-owned amendments and handoffs are unchanged.

## Implementation

`LocalAdmission` consumes sealed run options and persisted job settings. It holds
the parent run lease until scheduling and all child work join. Each local job
acquires its lease before the ready transition and retains it through executor
close and cleanup. A resumed ready job also reacquires local admission. Plan
resources cover plain jobs. Grouped jobs under those resources take only a
semaphore lease. Multiple job groups share one resource lease, with sorted keys
from the existing tuple codec. Priority and class come from the sealed winners.

Queue timeout counts only the selected group wait. Moving to another wait or
receiving an award ends that timer. A stale timer callback cannot cancel work
after acquisition. Cancellation, eviction, rejected reattachment, release errors
and watcher errors remain errors. Every acquired lease is released and its
watcher joined, including an adapter returning a lease together with an error.

Invariant: a child admission request uses only its own run's live parent token.
The generated parent-binding property varies the accepted run, row run and job
identity. Invalid combinations never reach the daemon or body. The property also reuses a captured context after parent release. That check
first reproduced an admission request after release. Cancellation is now checked
before the daemon call and before the resource-covered plain-job shortcut. The
parent token travels in a private context value and never in a process environment.

`OwnerHost.Around` brackets scheduling. `OwnerHost.Admit` brackets local release
and execution. `NativeHostConfig.Admission` connects these to the local adapter.
The existing native owner fixture now enters this path and launches its actual
SDK child without a fake admission-layer callback. Claimed-node execution still
uses the same executor. Both paths return with no active SDK compute or lease.

The local adapter owns run/box concurrency. It refuses nonlocal groups because
the store admission adapter is still missing. It does not reinterpret declarations
through the unimplemented C4 `ResolveConcurrency` helper. Complete store-backed
policy assembly remains W3 work.

## Retained ports and priority-one status

Six existing tests now exercise sealed local admission:

- `TestAdmissionLeasesHostAroundEachNode`
- `TestAdmissionPriorityAndClassPrecedence`
- `TestAdmissionRunHoldingTheHostTakesOnlySemaphores`
- `TestAdmissionDaemonGroupBoundsParallelNodes`
- `TestAdmissionHostLeaseIsWatchedWhileTheNodeRuns`
- `TestV2MultipleLocalConcurrencyGroupsShareResourceLease`

Their old bodies and unused v2 admission fake total **181 removed lines**.
Replacement assertions live in local_admission_test.go. The last test
reproduced a panic in C4 ResolveConcurrency before its port. Parallel admission
uses synctest. Other waits have a test deadline. The three tests named in the
original W4 handoff already passed on arrival and now exercise the native seam.
The daemon parallel and watcher tests had failed on legacy CreateRun fixtures
and now run without those writers.

The new local component also checks waiting cancellation, queue refusal/timeout,
eviction and rejected reattachment. Their legacy source tests remain in
admission_test.go because the native checks do not yet establish every original
terminal outcome, queue-timeout reason and unstarted-row assertion. Those three
ports remain open. In particular, superseded versus cancelled and pending policy
failure still need their final store/executor integration. No source assertion
is deleted on the strength of a narrower error-only check.

Existing W3 protocol work remains in place: complete v2 service registration,
canonical eval failure decoding, strict original-byte expansion validation,
procenv startup/cleanup projection, tuple concurrency keys, alias resolution,
settled step snapshots, captured source ordinals and current owner compute.
The component checks below retain these implementations. Full nodehost testing
still fails on unported legacy store fixtures and forbidden socket listeners.

## Amendment and handoff audit

The earlier assignment audit remains the complete row map. This continuation
advances native local assembly for A1-19, A1-39, A1-48 and A1-51. The existing W3
implementations for A1-01 through A1-07, A1-10 through A1-18, A1-21 through A1-25,
A1-36 and A1-44 through A1-48 are retained. This does not complete their foreign
adapters or native placement evidence. A1-50 and A1-53 remain integration work.
A1-54's approval SkipIf success edge remains absent from C1. The W1 source-attempt
and Executing handoffs retain their prior W3 implementations and scoped tests.

C2 authentication/delegation validators are implemented in this merged tree.
The prior report's description of them as panic stubs is superseded. Native
credential tests still use contract fakes and do not prove production wiring.
C4 `settings.ResolveConcurrency` remains a panic stub. C3/W9 still lacks source
attempt on `JobTransitionRequest`, a complete restricted snapshot read and a
working engine-client constructor.

## Remaining work and contract requests

- W3 must assemble store-backed plan/job admission, debug and memo policies.
  C1's owner pending-to-failed policy edge, approval SkipIf success and typed
  debug state remain missing. Memo source identity, zero-attempt materialization,
  terminal result references and remote import remain as recorded in the memo
  continuation. No raw writer substitutes for these contracts.
- W3 must migrate production local, trigger and pod callers, then delete legacy
  `Execute`, `Config`, `Session`, scheduler, layers and their remaining fixtures.
  **Legacy hosting implementation deleted in this continuation: 0 lines.**
- W8/W9 still own selected-engine host invocation and runner/bootstrap transport.
  The supplied engine installer and W3's native host constructor are usable
  boundaries. The installed identity alone does not prove execution inside that
  selected engine.
- W8's `runners/runInputFor` marshals the entire SealedRequest into
  `RunInput.SealedRequest`, whereas `OwnerHost` reads the inner Request. Its
  Hosting path also retains prepared Owner/GrantedRunner rather than replacing
  them from the award. These are confirmed integration edges in
  `internal/runners/execution.go` and `hosting.go`. They are unchanged here.
- Cross-process dynamic-mask propagation and replay still need their shared
  contract. Same-host registries do not establish that transport.
- The lead owns socket, api.sock, native placement, Darwin/Windows, Postgres and
  real-repository QA. TypeScript dependencies are installed and are not recorded
  as a blocker.

No foreign contract was changed. No new user limit, environment variable,
credential source, compatibility alias or service was added. The added mechanism
is local admission over sealed inputs. Reusing the legacy helper would require
reconstructing its mutable Run/Config/Result runtime, so the native path takes the
sealed inputs directly. Legacy helper deletion remains part of the full cut.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and at most four
packages. GOCACHE, GOTMPDIR and TMPDIR stay under `/tmp/sw-astra-W3/`.

| Check | Result |
| --- | --- |
| Whole-repository build | Passed. |
| Vet on nodehost, lease, source and orchestrator | Passed. |
| Common host, owner/evaluator, native assembly, process services, protocol, retained v2 and six admission ports | Passed. |
| Strict scheduler expansion and native startup/drift/cleanup environment checks | Passed in 1.01 seconds. |
| Full source, lease and nodeproto suites | Passed. |
| Existing lifecycle, environment, recovery and credentials acceptance selections | Passed in 4.92 seconds. No new matrix cell. |
| Scoped owner/native/admission race check | Passed. |
| Parent-binding fuzz property | Passed 1,000 generated cases after the released-context fix. |
| Orchestrator concurrency-key and scope-label selection | Passed. |
| Commentcheck against main | Passed across 1,637 files. |
| Whitespace and conflict-marker checks | Passed. |
| Full nodehost suite | Failed in 6.23 seconds. Unported store fixtures fail on removed args_json or missing legacy liveness rows. The old event HTTP fixture then panics because sockets are forbidden. |
| Sleepcheck against HEAD | Failed on untouched W2 tests/acceptance/session_flow_http_test.go:102, which compares a deadline with the wall clock. No W3 finding. |

The common-host selector includes the existing production-router refusal, eval
failure, original-byte expansion fixture, settlement, alias and environment
components. The actual SDK HTTP-router test was attempted separately and failed
at listener creation. It is not represented as a native transport pass.

## Shared files and friction

Outside `internal/nodehost`, this continuation changes only W3's export inventory
in `contracts/c3-protocol-sdk.md`, the resolved `contracts/t1-port-inventory.md`
and this report. No production caller in another package was changed or removed.
No Sparkwing binary, pipeline, candidate install, external service or Git write
ran. The socket restriction prevents native HTTP evidence. The lead retains
staging and integration gates. Scratch is removed after the checks join.

<!-- design:end -->
