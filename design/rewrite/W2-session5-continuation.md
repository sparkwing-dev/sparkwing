# W2 data-record ref correction and dependency handoff

Started from b6fd9e4c6. The committing origin gate, production console/peer
composition, declared references, dashboard repository resolution, resume and
cancellation snapshot were already committed. They were verified without
reimplementing them. Lead-owned handoffs and amendments are unchanged.

## Implemented

The W2 acceptance selection exposed two data-record failures:
TestDataWriteHTTPRecordEdgesAndLookup and TestDataRecordHTTPQueryValidationAndMiss.
Responses dropped the manual namespace. Queries treated manual:main as a raw ref,
producing the unmatched capability scope manual:manual:main and HTTP 403.

The HTTP adapter now returns RefScope.CapabilityScope and decodes the manual
prefix into the separate raw ref and verification flag. It removes the three-line
inference that copied the current write's trust onto a matching raw query ref.
The read grant still controls the selected scope. The existing assertion now
checks the actual RefScope{Ref: "main", Verified: false} representation.

Invariant: HTTP record selectors and responses preserve ref trust, and changing
that trust without a matching grant cannot reach record storage.
TestDataRecordHTTPRefScopeProperty generates verified/manual refs with escaped
query characters. It exercises write, lookup, response round-trip and refused
trust substitution through the actual HTTP router. It failed before the fix
and passes afterward. Authority, read edges and record transactions are fakes.
This adds no native topology cell or archived test-port identity.

The production diff replaces four lines with six, net +2. Legacy credential
production deletion is zero in this continuation because the W2 route and
verifier cut is already committed. W2 production Go versus main is
+14,355/-17,191, net -2,836, within the -250 budget. No adapter, setting,
schema, limit or compatibility mechanism is added.

## Items (a) through (e)

| Item | Result |
| --- | --- |
| (a) Committing origin | Complete. Root provisioning and owner consent share the direct-session predicate in their committing transactions. The generated native store property passes. |
| (b) Production composition | The named bootstrap/readiness, console, peer-read, launcher and data adapters are implemented. Verified non-checkout events reach SettlePayment. Final financial correlation/setup persistence and authority-bearing local mutations remain foreign integrations. |
| (c) Declared references | Complete. The native declaration and latest-run paths remain implemented. The generated store property passes. |
| (d) Dashboard and resume | Complete. Canonical repository resolution and the native resume handler remain implemented. Resume properties and HTTP checks pass. |
| (e) Serving operations | Nineteen of the 23 omissions are implemented. Queue-priority HTTP uses its existing C2 writer with fakes. The four missing registrations and native integer-priority persistence still wait on the foreign contracts below. |

TestFinalCredentialServingRegistrations still fails in both metrics configurations
for exactly these four routes. No placeholder or policy deletion hides them.

- GET /api/v1/runs/{run}/jobs/{job}/output: C1/W7 must supply member-session blob
  read authority. NodeRow.OutputRecord now supplies the selector, but the native
  blob authority resolver still accepts capability data authority only.
- GET /api/v1/runs/{run}/children/{child}/jobs/{job}/output: C1/W7 must supply the
  child output read edge through the same blob authority path.
- POST /api/v1/runs/{run}/jobs/{job}/claim/input: C1/W9 must declare the final
  memo/coalescing/reference input request and committed record projection.
- POST /api/v1/operator/teams/{team}/refunds: C1/W1b/W9 must declare the human
  refund request and its durable provider/ledger join. Verified event settlement
  does not define that operation.

W1 must supply integer queue-priority persistence and ordering. The remaining
credential/account/team mutation, rotation, audit, secret-read, roleless-session,
trusted intake, local-first and runner snapshot transactions remain assigned to
W1 in handoffs.md. Their W2 route/interface implementations use fakes and refuse
missing native adapters. W9's NewDataClient still needs the documented C2 flat
data transports and final native log transport. Native placement, provider,
Windows/Darwin and PostgreSQL evidence remains with the acceptance owners.

All remaining pieces of (a)-(e) wait on code/contracts another area has not
landed. COMPLETE below uses that stopping condition from the session request.
It does not claim serving-route completeness or launch acceptance.

## Files

This continuation changes pkg/controller/data_record_routes.go,
tests/acceptance/data_writes_http_test.go, docs/data-uploads.md, C2's local
contract explanation, CHANGELOG.md and their generated documentation mirrors.
This report is the only new file. No pkg/store file changes in this continuation.

The earlier end-to-end (a)-(e) store changes are listed in
[W2-vertical-handoff.md](W2-vertical-handoff.md) and
[W2-console-continuation.md](W2-console-continuation.md). Their production files
under pkg/store are authentication_binding.go, authentication_contract.go,
authority_binding.go, authority_bootstrap.go, authority_issue.go,
authority_provision.go, binding_execution.go, blob_authority_projection.go,
blob_data_access.go, blob_persistence.go, capabilities_contract.go,
controller_pools.go, controller_reporting.go, controller_runner_admin.go,
debug_pause_resume.go, download_accounting.go, latest_run.go,
launcher_snapshot.go, source_release.go, store.go, billing_trust.go,
business_events.go, console.go, credit_reversals.go, credits.go, deletion.go,
dialect.go, github_app.go, operator_credit_settings.go, signup_gate.go and
state_authority.go. launch_sync.go was deleted. The previous report also records
the blob_authority.go integration. Their test files remain with those reports.

## Verification and limits

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR under /tmp/sw-astra-W2.
Tests use at most -p 4.

- Whole-repository go build ./... passes on the final tree.
- Scoped vet, full capability/apierr/apiroutes/controller-client suites,
  focused controller data/credential/bootstrap/console/peer/launcher/resume tests,
  native origin/cancellation/reference/data properties and commentcheck pass.
- The final W2 executable acceptance selection passes 269 top-level tests in
  50.443 seconds. Its 372 pending/skipped subtests remain skipped. The existing
  SQLite console-split and session-digest proofs pass. No native matrix cell
  is newly claimed.
- The seven socket-free metrics tests, trigger validation and receipt not-found
  pass. The prior repairs and two obsolete-test deletions remain committed.
  The two dedicated metrics listener tests still need the lead's socket-enabled
  environment. No full controller suite is claimed.
- Native TypeScript passes with go -C sdk/typescript test -p 4 ./test/nativehost
  -run '^TestNative' -count=1 -timeout=90s. No setup error occurred.
- Documentation mirror drift and git diff --check pass. The focused red command
  for the regression was go test ./tests/acceptance
  -run '^TestDataRecordHTTPRefScopeProperty$' -count=1. It passes after the fix.

The initial broad acceptance failure is corrected, not waived. The separate
serving-registration failure remains visible. Overwing reports its binary is
ten commits behind source. No Git write, provider call, Sparkwing runtime,
pipeline or release ran. Git identity remains Korey Gambill, and core.worktree
is unset. Task scratch is removed at handoff. The shared build cache is retained.

STATUS: COMPLETE
