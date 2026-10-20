# W9 public run pages continuation

Base: `989475f44`. Priorities (1)-(3) remain implemented. Focused readiness,
doctor, peer-intake and cleanup regressions pass. This continuation closes the
run-list wire gap recorded in W9-LOG-SEARCH-CONTINUATION.md. The full W9 area
remains open at the foreign contracts below.

## Implemented

The shipped RunReader now receives C3 RunPage responses from the real controller.
JSON listing uses the same safe RunDetail projection as single-run status,
including attempts, canonical log subjects and read-time signed engine notices.
The dashboard retains its existing representation because its web callers still
consume that view. Both representations use the same authenticated route.

All RunQuery filters apply before the page limit. Pages descend by allocated
team number, preserve the exclusive lower bound and retain their initial upper
boundary. New runs arriving between pages do not displace earlier candidates.
Lookahead retains the next matching run. Sparse matches can cross internal read
batches, and the internal batch size does not cap the requested page size.

The security invariant is that a cursor grants no authority: each request uses
current authentication, each candidate passes its persisted binding check, and
only the public display projection leaves the controller. Cursor scope retains
the tenant and exact query. Any storage, policy or projection failure discards
the complete response rather than returning a partial successful page.

Listing and log search share their selection fingerprint implementation. Their
cursor validation and traversal remain separate because log search can resume
within a subject. Single-run status and listing share their notice projection.

The local-execution guide and its generated mirror describe run-page behavior.

## Evidence

The new public listing regressions failed before implementation: the old handler
returned HTTP 500 for native sealed run rows. They now traverse the shipped
client, route policy, credential issuance and real SQLite without opening a
socket. No transport fake supplies the response body.

Tests cover descending order, arrivals between pages, exact continuation, empty
final pages, exclusive number bounds, all RunQuery filter categories, inclusive
time bounds, masked failure selection, private input redaction, tagged log and
attempt parity with ReadRun, current notices and policy failure after a result
has already accumulated. A sparse-match test scans beyond 100 candidates and
then reads a 105-run page without an implicit cap.

Authority fuzzing mutates cursor offsets and additive data, generates private
argument values, inserts malformed foreign-tenant rows and revokes the credential
between pages. Selection fuzzing changes the tenant, filters, page size, parent
and date bounds. The public authority fuzz completed seven executions without
failure; seed cases also pass under the race detector.

A fresh read-only reviewer found no blocking issue. Its suggested fingerprint
extraction was implemented and re-reviewed without findings. Its Simplify pass
found that the cursor, boundary, projection and representation dispatch each
have current callers and contract purposes.

Checks passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR=/tmp/sw-astra-W9/go and at most four package workers:

- Whole-repository `go build ./...` and focused vet for controller, client,
  CLI, localcontroller and cluster, including after the final shared helper.
- Listing, detail, log-search and cursor regressions under the race detector.
- Full short suites for controller/client, localcontroller, cluster,
  clisurface and controllerwire.
- Readiness, fixture cleanup, doctor, detached-entrypoint and peer-intake
  selections through the real socket-free boundaries.
- Surface, engine-amendment and public run-detail acceptance selections.
- Full doccheck and apispec suites, focused embedded-doc tests and docs mirror
  drift verification.
- CLI and runner rewrite_integration compilation.
- Commentcheck and `git diff --check`.

No archived test was newly ported. No native acceptance cell is newly claimed.
Native command, service-manager, Windows and PostgreSQL evidence remain with
the lead. No daemon, candidate installation, pipeline, Git write or release ran.

## Contract changes needed and real integration left open

No foreign contract, route grant or schema changed. The prior requests in
W9-HANDOFF.md and W9-READINESS-CONTINUATION.md remain, except that persisted
single-run log pages, cross-run log search and run listing are now connected.

1. C2/W3 must declare owner/eval process credentials and a separate source-job
   relation. CredentialNodeProcess requires a positive node attempt;
   ProcessAuthorities.Delegate and NativeProcesses.Delegate identify the SDK
   job through that node binding. A trigger owner has no node binding, and an
   evaluation source's fence is distinct from its owner's lease. The production
   NativeConfig.Evaluate adapter needs a supported delegation path retaining
   both identities. The existing separate-binder fake proves the W9 seam before
   any work award. No attempt or authority was fabricated to bypass this gap.
2. W8 must supply KubernetesBoundary.Probe and Clear: actual network-enforcement
   evidence and reservation-scoped teardown. ProductionRuntime.Launcher remains
   unbound. This session ran TestInstalledRunnerRuntimeComposition/launch and
   reproduced its failure: `launcher runtime is unavailable: wire the controller
   Cloud awards and verified node/network boundary before launching Jobs`.
   The assertion and production error remain unchanged. Start, warm,
   fetch-source and run-node retain their production factories.
3. Canonical remote placement eligibility, Cloud plan/eval host entrypoints,
   cross-run record/read-set proofs, bootstrap checkout consumption, scheduled
   sealed intake and earlier here/cache/publication boundaries remain as
   recorded in the previous reports. Steps and receipts still need their C2/W1
   read projections; old step rows cannot supply an attempt fence, and the CLI
   must not invent settlement facts.

The lead should rerun TestReviewProductionExecutionBindings,
TestReviewPublicDoctorSweepsAndWarnsForPrunedRun and
TestLocalClaimantW8UsesPersistedPeerAuthority natively with isolated state and a
release-version stamp. Launch remains red until its deployment boundary exists.

## Scope, size and assumptions

Shared production files: internal/controller/run_pages.go, run_selection.go,
log_search_pages.go, engine_run_detail.go and handlers.go. Shared test files:
internal/controller/run_pages_test.go and log_search_pages_test.go. Documentation:
docs/local-execution.md and pkg/docs/mirror/local-execution.md. Lead-owned
handoffs and amendments are unchanged.

This continuation adds 137 production lines and deletes nine, net +128. No whole
legacy file was deleted. W9-owned production paths versus main remain
+16,876 / -23,797, net **-6,921**, within the -2,900 budget. The shared controller
addition above is reported separately. Counts exclude tests and declarations.

The cursor and JSON representation implement the existing C3 contract. The
dashboard's live representation cannot provide RunDetail or a continuation
boundary. Removing the JSON dispatch and run_pages.go reverts this composition;
the notice and fingerprint extractions can remain shared. No option, environment
variable, interface, retry, timeout or customer-visible limit was added.

Friction: native sockets and service managers remain unavailable. Overwing's
installed binary reports ten commits of source lag and treats untracked files
as names during matching; the reviewer read their complete contents. Early Go
tests used their default auto-cleaned temporary directories; final tests also
set TMPDIR inside the worker scratch directory. Scratch is removed after checks
join, and the shared build cache is retained. The process sweep found no test
daemon or child left running.

STATUS: CONTINUE
