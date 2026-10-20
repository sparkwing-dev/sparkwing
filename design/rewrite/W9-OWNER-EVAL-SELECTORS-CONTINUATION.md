# W9 continuation: owner evaluation and numeric selectors

Snapshot from base `805fb5dc9`. The SDK replacement pin policy is unchanged.
The native local-controller, schedule and public-doctor passes supplied by the
lead remain the starting evidence. Their completed implementations were retained.

## Implemented

The default native evaluation binder delegates from the current owner or eval
credential. SDK job identity is separate from the owner lease, and no work
attempt or synthetic node authority is created. The obsolete missing-binder
refusal is deleted. Node-attempt delegation still requires its independent award.

Invariant: an SDK evaluator uses only its live owner/eval authority and accepted
source, and cannot borrow another job's output edges. `CredentialOwnerProcess`
selects that authority under the existing grants. Process authentication rechecks
lease expiry as well as parent ancestry, membership, cancellation and revocation.
Terminal eval credentials use their separate eval and controller leases, including
source generation zero.

`ProcessServices.SourceBindings` supplies a required evaluation-source checker
through the existing BindingChecker interface. The checker captures the persisted
source run/job/generation/hash and rechecks it for service requests. It permits
only that job's dependencies, optional dependencies, recovery edge and generated
groups. The registry independently checks the parent relation after this check.
`capability.ReadsJobOutput` is the common declaration checker used by both the
store and evaluator. Source fence changes return lock_held; undeclared reads
return forbidden with reason=binding. The process bearer is masked before delivery
and revoked when joined. These are internal C2/C3 implementation additions within
the user-assigned owner/eval integration; no grant, persisted binding, issuance
kind or schema changed.

Public numeric selectors resolve after authentication within the credential's
active team. Stable identities then reach the existing authorization and
committing controls. This covers run/job observations, debug, logs/search/streams,
events, receipt, attempts, steps, approvals, cancel, retry and deletion decoding.
Private host and exact-body execution APIs retain stable IDs. ResolveTarget now
receives the request context; its existing callers were migrated.

The job route returns the declared safe DebugJob projection. It checks the live
persisted attempt and lease, maps granted coordinator/local and agent execution
to local and runner, and excludes private claim, specification and environment
fields. Unknown or Cloud locations remain absent. Dashboard reads receive the
canonical path identities. The local execution guide and embedded mirror changed.

## Evidence

- Whole-repository build and vet of capability, nodehost, localcontroller, store,
  controller and controller/client passed.
- Full short nodehost and localcontroller suites passed. Capability passed with
  the pre-existing removed-surface test excluded: it flags six declared private
  local-host paths containing `/nodes/`. The initial unexcluded run is recorded
  as a failure, not a pass.
- Real private HTTP-to-controller-to-SQLite process delegation, revocation and
  zero-work-attempt assertions passed. Default native binder tests passed.
- Liveness and source-edge properties passed under race. Fuzzing completed
  13,117 liveness executions and 19,062 final source-edge executions.
  The expanded numeric-selector fuzz completed 59 executions.
- Real client-to-SQLite numeric reads, resume and cancel, persisted log cursor
  parity, safe projections and tenant-isolation properties passed. The debug
  CLI, shared client and surface acceptance selections passed.
- Shared storage/log publication, peer intake, service handover and drain
  acknowledgement regressions passed. Full doccheck, embedded mirror checks,
  commentcheck against main and diff whitespace checks passed.

Tests used GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR=/tmp/sw-astra-W9/go and TMPDIR=/tmp/sw-astra-W9, with at most four
package workers. No tests added sleeps. The original persisted process-authority
fixture also failed with the unchanged implementation in a Go overlay; its
DelegateSpec now retains the parent's repository scope instead of widening it.
No archived test was newly ported or native acceptance cell newly unskipped.

## Remaining contracts and native work

1. The full shipped-runner composition probe still fails for launch. Start, warm,
   fetch-source and run-node reach their exact production HTTP operations. W8
   must supply KubernetesBoundary.Probe and Clear, or a real CloudBoundary
   constructor, with enforced network evidence and reservation-scoped cleanup.
   Only test implementations exist; no placeholder factory was added.
2. Kubernetes debug attach needs a persisted structured namespace/pod/container
   target. C1 NodeAttempt currently has only executor strings. No target is
   guessed from holder or location text.
3. Owner latest-reference requests still reach a parent relation requiring a
   live node claim. The source checker verifies the exact declared pipeline/job,
   but production parent transport needs an explicit owner-source relation.
   UnitEval's fixed table has no NodeReport, so masks/cleanup remain denied to
   that separately awarded credential; changing that table needs an amendment.
4. Native receipt still emits legacy node_id instead of required job_id, and
   computes estimated cost instead of projecting settled billing. Run attempts
   still use the legacy retry-tree read of absent args_json. The default tenant has no
   DeleteAuthorizedRun implementation. Its 204 response also differs from the
   client deletion receipt. Numeric decoding reaches these adapters,
   but their failures are not claimed as completed observations or deletion.
5. The SDK replacement decision remains with Korey. Native local execution and
   closure evaluation must be rerun after it is resolved. Cloud plan/eval hosting,
   canonical remote placement, source-checkout consumption, cross-run records,
   scheduled sealed intake and here/cache/publication boundaries remain in
   W9-CONTRACT-BOUNDARIES-CONTINUATION.md and its linked reports.

The source relation is tested with persisted private authority and scripted source
state. Completed SDK closure execution, Kubernetes, service managers and socket
QA remain native lead evidence. The docs WebClient test was blocked by the sandbox
TCP refusal; exact embedded-doc drift tests passed. No full-repository suite,
pipeline, candidate installation, deployment or release ran.

## Scope and cleanup

Production Go in this continuation: **+376/-90, net +286**.
W9-owned production Go versus main: **+16,911/-23,797, net -6,886**,
within the -2,900 budget. Tests, testdata and contract declarations are excluded.
No production file was deleted in this continuation. Removed lines include the
obsolete evaluation refusal, job/fence coupling and duplicate read-edge logic.
The private JSONL log writer remains deleted from earlier work. Lead-owned
handoffs and amendments were read and left unchanged.

Shared files touched, including the minimal caller and fixture migrations:

- `internal/capability/authentication.go`
- `internal/capability/policy.go`
- `internal/capability/read_set.go`
- `internal/capability/routes.go`
- `internal/nodehost/native_processes.go`
- `internal/nodehost/owner_process_authority_test.go`
- `internal/nodehost/owner_process_store_test.go`
- `internal/nodehost/process_authority.go`
- `internal/nodehost/process_authority_store_test.go`
- `internal/nodehost/process_services.go`
- `internal/nodehost/store_host_fixture_test.go`
- `internal/wingd/localcontroller/claims_test.go`
- `internal/wingd/localcontroller/evaluation.go`
- `internal/wingd/localcontroller/evaluation_test.go`
- `internal/wingd/localcontroller/native.go`
- `internal/wingd/localcontroller/native_config_test.go`
- `internal/controller/browser_reads.go`
- `internal/controller/concurrency_routes.go`
- `internal/controller/data_binding_native_test.go`
- `internal/controller/data_request_binding.go`
- `internal/controller/debug_selectors_native_test.go`
- `internal/controller/engine_claim_routes.go`
- `internal/controller/engine_jobs.go`
- `internal/controller/engine_routes.go`
- `internal/controller/log_pages_test.go`
- `internal/controller/missing_operation_routes.go`
- `internal/controller/policy.go`
- `internal/controller/policy_router.go`
- `internal/controller/private_host_test.go`
- `internal/controller/public_selectors_native_test.go`
- `internal/controller/reporting_routes.go`
- `internal/controller/reporting_routes_test.go`
- `internal/controller/run_control_routes.go`
- `internal/controller/run_selector.go`
- `internal/controller/team_write_routes.go`
- `pkg/docs/mirror/local-execution.md`
- `internal/store/binding_execution.go`

No Git write was made. Git has no local identity or core.worktree override.
Scratch is removed after checks join; the shared Go cache is retained. Friction
is the foreign contracts above, unavailable native sockets and service managers,
and Overwing's reported installed-binary source lag.

STATUS: CONTINUE
