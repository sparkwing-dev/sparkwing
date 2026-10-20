# W9 cross-run log search continuation

Base: `f0393d97e`. Priorities (1)-(3) remain implemented. Their readiness,
public doctor, peer-intake and cleanup regressions pass. Native command reruns
remain with the lead. This continuation connects cross-run search to persisted
subjects and closes the search handoff in W9-LOG-PAGES-CONTINUATION.md.

## Implemented

The JSON representation of `/api/v1/runs/grep` now returns C3 `LogSearchPage`
through the shipped `client.NewLogReader`. Run selection uses sealed public
repository/ref/SHA facts, current status, lineage, inclusive time bounds and
persisted masked failure text. Positive repeated selections are ORed,
exclusions subtract, and filter categories are ANDed before the run limit.

Tenant run views are read in descending allocated-number order. The cursor
retains the initial number boundary and the current subject's line and match
positions. An empty candidate page can have a continuation. A full response can
resume inside one subject without repeating or losing matches. MaxMatches
applies per subject, including across pages. Zero is uncapped.

Reads use the shared committed-segment reader and existing log egress metering.
Owner units retain their canonical identity. The authorization invariant is
that a cursor never grants access: every returned line still requires current
authority for its exact persisted subject. Consumed offsets cannot suppress
authorization or turn missing committed bytes into successful empty results.
The dashboard still uses its existing representation, which has live callers.

## Evidence and review

The new public-path regression initially failed against the legacy 400 response.
It now traverses the real client, policy router, credential issuance, SQLite and
filesystem objects. HTTP delivery uses an in-memory round trip and opens no socket.

Tests cover empty candidate pages, a later run arriving between pages, canonical
subjects, continuation within one subject, per-subject caps, uncapped matching,
all RunQuery filter categories, masked run/job errors, changed cursor scope,
foreign tenant rows, forged consumed offsets, revocation and missing committed
bytes. Store tests cover number bounds, ordering, tenant scope, complete views
and discarding partial pages on read failure. No archived test was newly ported.

Independent read-only review found that the first projection omitted
`nodes.error`. Its public-path regression ran red with an empty run error and
status detail, then passed after the same snapshot included masked job errors.
Re-review closed the finding and found no additional substantive issue or
mechanism to remove.

The following checks pass with GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and at most four Go package workers:

- Whole-repository `go build ./...` and focused vet for controller, client,
  store, CLI, localcontroller and cluster.
- Search, single-run log pages, authority fuzz seeds and store-view tests,
  including the race detector after the review fix.
- Bounded public authority fuzzing: eight executions without a failure.
- Full short suites for controller/client, localcontroller, cluster,
  clisurface and controllerwire.
- Readiness, runtime cleanup, managed-service handover, public doctor,
  peer-intake, local queue binding and production-controller regressions.
- Surface and engine-amendment acceptance selections. Native-store owner and
  cancelled-host final-log acceptance components also pass.
- CLI and runner rewrite_integration compilation, without native execution.
- Doccheck, apispec, embedded symbol/privacy/section checks and docs mirror drift.
- Commentcheck and `git diff --check`.

No native acceptance cell is newly claimed. No daemon, candidate installation,
operational Sparkwing command, pipeline, Git write or release ran.

## Open integration and contracts

No frozen declaration, schema, grant or route changed. The earlier requests in
W9-HANDOFF.md and W9-READINESS-CONTINUATION.md remain, except that both single-run
pages and cross-run search now have persisted server bindings.

- C2/W3 must supply owner/eval process credentials and the source-job relation
  independently of a positive node attempt. NativeConfig.Evaluate has a fake
  boundary test, but the production adapter cannot use CredentialNodeProcess.
- W8 must supply the production KubernetesBoundary network probe and
  reservation-scoped cleanup. ProductionRuntime.Launcher remains unbound.
  Start, warm, fetch-source and run-node retain their production factories.
- Canonical remote placement eligibility, cross-run record/read-set proofs,
  bootstrap checkout consumption, scheduled sealed intake and the earlier
  here/cache/publication/native execution boundaries remain as reported before.
- The new selector serves SearchLogs. Inspection still shows the ordinary
  `/runs` listing on its legacy ListRuns projection. Its C3 response/pagination
  composition remains a separate integration point.

The lead should rerun TestReviewProductionExecutionBindings,
TestReviewPublicDoctorSweepsAndWarnsForPrunedRun and
TestLocalClaimantW8UsesPersistedPeerAuthority natively with isolated state.
TestInstalledRunnerRuntimeComposition/launch remains open at the Cloud boundary.

## Scope, size and assumptions

Shared production files are internal/controller/log_search_pages.go,
internal/controller/run_selection.go, internal/controller/log_pages.go,
internal/controller/reporting_routes.go and internal/store/run_view.go. Shared tests are
internal/controller/log_search_pages_test.go and internal/store/run_view_test.go.
Tenant.ListRunViews and RunView.Failures are concrete read projections for C3.
The local-execution guide and its generated mirror describe search pagination.
Lead-owned handoffs and amendments are unchanged.

This continuation adds 378 production lines and deletes nine, net +369.
No whole legacy file was deleted. W9-owned production paths versus main remain
+16,876 / -23,797, net **-6,921**, within the -2,900 budget. The shared controller
and store additions above are reported separately from that path count.

The candidate and subject cursor implements the declared C3 search contract.
Rendered dashboard results cannot supply canonical subjects or resumable
per-subject progress. Removing the JSON search registration and its new files
reverts this continuation. No flag, environment variable, interface, retry,
timeout or retention limit was added. The existing 500-record page size is
pagination, with continuation for remaining matches.

Friction: native sockets and service managers remain unavailable. PostgreSQL
and native platform execution remain unverified here. Worker scratch is removed
after checks join. The shared build cache is retained. Independent landing
review and native validation remain with the lead.

STATUS: CONTINUE
