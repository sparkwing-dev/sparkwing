# W2 native credentials continuation

The next continuation is [W2-console-continuation.md](W2-console-continuation.md).
It adds native console commits and queue-priority HTTP. The four missing
registrations and the unimplemented queue ordering transaction remain open.

Items (a), (c), and the available part of (d) are implemented. Item (b) has
native bootstrap, peer-read, launcher and data adapters. Item (e) closes 18 of
23 missing registrations, counting resume. Five serving operations still need
cross-area contracts. The area is not complete.

- (a) Both committing origin gates use DirectAccountSession after resolving
  current authority. Generated capability-derived sessions cannot provision
  detached member roots or change machine-owner consent in either direction.
- (b) The configured bootstrap seed reaches Store.Issue in a locked, once-only
  transaction. Its durable settings marker commits with the root and prevents
  revival after revocation. HasDeploymentAdministrator uses live ancestry and
  the store clock. Native rotation grace now ends at its deadline.
- (b) Shared local HTTP uses the real default-tenant peer snapshot and native
  BindNone relations. Launcher sync checks current unit ownership and launcher
  ancestry, returns immutable inputs without bearers, and retains holder rights.
  Data adapters derive sealed scope/uploader, authorize logical selection before
  choosing a digest, and recheck read edges and size when charging downloads.
  Cancellation closes sensitive blob access while preserving holder rights.
  Verified payment proofs already reach SettlePayment. W1b still must implement
  settlement beyond checkout.
- (c) The native checker reads the live bound job's sealed pipeline references.
  The latest-run query now uses the final schema, one snapshot, the store clock
  and sanitized display inputs. Actual authorized HTTP succeeds for a declared
  reference and refuses undeclared or removed references.
- (d) The dashboard sends source acquisition location. The controller resolves
  canonical repository identity through the current GitHub App API and refuses
  a mismatched supplied key. Native resume rechecks authority, closes pauses,
  preserves execution fences and handles repeated requests.
- (e) Native attempts, steps, activity, debug-pause, named claims, runner removal,
  pools, child intake/reads and six log submissions are registered. Logs derive
  exact subjects, read sealed retention, verify physical segment bytes and
  repeat the writer check at commit. Incremental and final replay tests pass.

Controller work capabilities with attempt zero cover a holder generation.
C2 now projects the current attempt and copies its claim data scope from the
existing committed snapshot. This fixes authentication after start for the
whole class. Process credentials and delegates with a fixed attempt stay
strict. The generated property checks both boundaries. Cancellation snapshots
now read separate run/job intent, with job intent taking precedence.

## Deleted and ported

Deleted pkg/store/launch_sync.go (154 production lines) and its replaced
53-line billing test. Native ReleaseUnit and immutable-billing tests retain
unstarted refund and started-work refusal coverage. The old latest-run private
query is deleted (33 lines). Six latest-run fixtures now use native admission.
Legacy pause fixtures are replaced. Ten retained controller tests pass after
migration. The two deleted tests covered retired trigger.source and unsealed
run creation/finish requests. Three blob fixtures now obtain the real tagged
principal required by the transaction checker. The port inventory records the
mappings. Legacy credential routes removed in earlier commits remain absent.

## Remaining contracts and integration

The serving inventory remains red for exactly these five routes in both
metrics configurations:

- GET /api/v1/runs/{run}/jobs/{job}/output
- GET /api/v1/runs/{run}/children/{child}/jobs/{job}/output
- POST /api/v1/runs/{run}/jobs/{job}/claim/input
- PUT /api/v1/queue/groups/{group}/priority
- POST /api/v1/operator/teams/{team}/refunds

W1/W1b/W7/W9 must declare the committed result/record/input projections and
member/child blob read edges. C1 has no persisted integer queue priority or
ordering transaction. Refunds need a console-authorized request/queue joining
the provider and ledger owners. These are contract gaps, rather than existing
interfaces that can be implemented with another fake. The policies remain.
Detailed shapes and integration points are in W2-serving-handlers.md.

Other real integrations remain: W1b non-checkout settlement; authority-bearing
console and local mutations; roleless console sessions; C1/C4 intake policy,
no-App GitHub identity transport, pause-create/approval diagnostic masking,
runner inventory/queue-depth ingestion, data-bound session and expansion
reader projections, and receipt output references. W9 must
connect native log clients. The dashboard log reader still uses legacy NodeLog
instead of native log_streams/log_segments. C1 child replay identity must include
the parent generation. Pool intake still compares exact branches while the
write wire accepts patterns. W1 owns local-first matching and trusted schedule/
App intake under the lead's split. No foreign contract was silently changed.

## Shared store files

Production edits and additions, including the deleted implementation:

- authentication_binding.go, authentication_contract.go, authority_binding.go
- authority_bootstrap.go, authority_issue.go, authority_provision.go
- binding_execution.go, blob_authority_projection.go, blob_data_access.go
- blob_persistence.go, capabilities_contract.go
- controller_pools.go, controller_reporting.go, controller_runner_admin.go
- debug_pause_resume.go, download_accounting.go
- latest_run.go, launch_sync.go (deleted), launcher_snapshot.go
- source_release.go, store.go

Shared test migrations include binding_runtime_test.go, claim_run_test.go,
latest_run_test.go and three blob fixtures. Other shared edits are frontend
API/tests, tests/acceptance/engine_intake_http_test.go, C2 documentation and the
port inventory. Lead-owned handoffs and amendments are untouched.

## Evidence and limits

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache and
GOTMPDIR/TMPDIR under /tmp/sw-astra-W2. Package concurrency stayed at -p 4.

- Whole-repository build and scoped vet for capability, apierr, controller,
  store and executable acceptance.
- Full capability, apierr, apiroutes and controller/client suites.
- Focused native store origin, grace, binding, cancellation, data, latest-run,
  pool, runner, activity, log and release tests.
- Focused controller credential/engine/reporting/console/peer/bootstrap/source/
  resume tests, repaired metric/trigger/receipt tests and log-through-commit HTTP.
- W2 executable acceptance selection and native TypeScript host tests.
- Frontend API tests, commentcheck and diff whitespace checks.

No new native topology matrix cell is claimed. W7 physical log I/O, claim
enrollment facts and C1 intake policy use explicit fakes in the HTTP tests. The actual dedicated
metrics listener tests fail with listen tcp 127.0.0.1:0: socket: operation not
permitted. A broader controller selection also reached a socket-based object
budget test and stopped. No full controller or whole-repository test pass is
claimed. Shared-cache artifacts disappeared during some concurrent builds.
Later builds and focused tests passed. The shared cache was never removed.

Mechanisms added: a bootstrap-used marker in the existing settings table,
because deleting or revoking an old root must not reopen seeded issuance.
Native route/adaptor owners complete the declared surfaces. No schema column,
configuration option, dependency, compatibility alias or policy cut was added.
The local request-shape completions are listed in W2-serving-handlers.md.

W2 production delta versus main is +14,110/-16,998, net -2,888, within the -250
budget. This session adds a net 825 W2 production lines under the lead's ownership
exclusions. Shared store edits add 873 and delete 201 production lines,
net +672, including the 154-line legacy launcher deletion.
Only discovery commands ran. No Git write, candidate/runtime Sparkwing process,
provider network or pipeline ran. Scratch is removed at handoff and the shared
cache is retained.

STATUS: CONTINUE
