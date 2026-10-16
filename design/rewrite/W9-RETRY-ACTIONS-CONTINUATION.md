# W9 sealed retry actions continuation

Base HEAD: `b92263eb8`. The lead's native local-controller, local-schedule and
public-doctor passes remain the starting evidence. The SDK replacement pin
policy and lead-owned handoffs/amendments are unchanged.

## Implemented and deleted

The HTTP retry handler accepted an empty selection and created a run. The new
persisted regression reproduced HTTP 200 for `{}`. `EngineController.Retry`
now calls the existing shared validator before intake. Missing/unknown mode or
missing idempotency key returns bad_request without writing a run. No grant,
schema, route, limit or public type changed. API docs, changelog and their
generated embedded copies describe the validation.

Invariant: retry admission requires a valid selection and current authority,
retains the original source seals, and cannot write into another team.
Generated-input properties cover selectors, private values, modes, keys,
revocation and membership loss. Refused requests leave run counts unchanged.
The shared-client regression follows three retry generations, with and without
an accepted plan, and checks the original checkout, SHA, actor, classified
arguments, plan and spec against each new execution. Lineage identifies the
current source and retrier independently. Exact replay returns the same run.
Revocation and membership loss after the first commit also refuse replay.

The obsolete `pkg/controller/retry_test.go` is deleted: **371 test lines**.
Its raw trigger responses, query flags and environment provenance are removed
contracts. The port inventory maps every old test, including the unresolved
mode test below. The old duplicate-ID fixture is impossible under the final
schema's globally unique run IDs. Foreign IDs and colliding team-local numbers
remain covered through the actual router and SQLite.

The obsolete environment allow-list test in `trigger_test.go` is replaced by
`FuzzRunIntakeRejectsLegacyEnvironmentProvenance`. Removed raw trigger/env
documents and unknown credential/provenance fields fail decoding without
echoing values or admitting a run. Typed captured environment snapshots retain
their existing contract. This deletes **68 test/import lines** and removes the
last import of `internal/retryprovenance`, whose **11 production lines** are
also deleted. No production caller needs migration.

Production Go this session is **+3/-11, net -8**, entirely in shared paths.
Tests are **+335/-439, net -104**. W9-owned production Go versus main remains
**+16,886/-23,807, net -6,921**, within the -2,900 budget. Counts exclude tests,
testdata, acceptance and contract declarations.

## Contract changes and real integrations still needed

- **DESIGN: committed retry selection.** Invariant: failed/all selection is
  part of the committed execution intent and exact replay identity, while the
  original request and accepted spec stay immutable. `EngineController.Retry`
  discards Mode when creating C1's IntakeRequest. That request and persisted
  execution lineage have no retry-selection field. The new tagged
  `TestReviewRetryModeIsPartOfCommittedReplay` reproduces a successful replay
  when the same key changes from failed to all. C1/C2/W3 must carry the selection
  independently of the original seals, include it in the committing digest,
  and consume it when choosing successful-job reuse. Do not encode it into a
  key or mutate the source's NoMemo setting. This remains stopped at the foreign
  contract boundary. The old FullQueryParam behavior is not claimed complete.
- **Owner/eval authority.** The earlier two-strikes design handoff remains:
  C1 result writes require a live node holder and positive attempt, and cannot
  receive the owner/eval source fence. The existing tagged owner-report test
  remains red. Persisted latest-reference binding also requires UnitNode, and
  UnitEval's frozen grants exclude NodeReport for masks and cleanup. No work
  attempt or wider grant was fabricated.
- **Production launch.** `ProductionRuntime.Launcher` remains unbound because
  KubernetesBoundary.Probe and Clear have no production implementations.
  W8 must provide enforced network evidence and reservation-scoped cleanup
  through CloudBoundary. The full shipped-runner done test remains open.
  Start, warm, fetch-source and run-node retain prior first-HTTP-operation
  evidence, which does not establish completed execution.
- **Other retained boundaries.** Annotation column/end_line persistence,
  canonical owner placement and here hosting, remote-cache source/read-set
  proof, publication, Cloud plan/eval and bootstrap checkout consumption,
  scheduled sealed intake, settled receipts and Kubernetes debug targets remain
  as recorded in W9-RETRY-HISTORY-CONTINUATION.md and its linked reports.
  Native local execution still waits on Korey's SDK replacement decision.

Existing local owner/node separation, required process/storage construction,
shared log publication, service election handover and peer intake were retained.
The private JSONL writer remains deleted. No new native acceptance cell is
enabled or claimed complete by this session.

## Evidence and scope

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package workers.

Passed: whole-repository build; focused controller/client/wire/CLI vet; persisted
retry action/history, numeric/debug selector and peer-intake selections; full
short capability, controller/client, controllerwire and doccheck suites; surface,
retry-contract and engine-amendment acceptance selections; embedded-doc/privacy/
symbol checks; mirror/changelog drift, commentcheck against main and whitespace
checks. The new properties and retry regressions also pass under race.
Fuzzing passed 263 selection-validation, 243 authority/source and 15 legacy
provenance executions. Independent read-only review found no new blocking issue
or added production mechanism.

The retry-mode tagged regression failed as described above. Initial new fixture
errors were corrected: the Go settings type, copied terminal timestamp, removed
plan version field and impossible duplicate run ID. The corrected invalid-mode
regression ran red before its production fix. No whole-repository test suite,
pipeline, candidate install, Sparkwing execution, deployment, release or Git
write ran. Source runs are seeded, and intake settings/retention use a contract
fake. Router, client, credentials and committing SQLite operations are real.
No host or placement execution is claimed by these tests.

Shared files touched: `pkg/controller/engine_lifecycle.go`, deleted
`pkg/controller/retry_test.go`, `pkg/controller/trigger_test.go`, the new
`retry_action_native_test.go`, `retry_mode_integration_test.go` and
`intake_provenance_native_test.go`, deleted `internal/retryprovenance/keys.go`,
the port inventory and generated `pkg/docs` copies. Remaining changes are
W9-owned docs, changelog and this report. The only caller migration removes
the obsolete test's constants import. No foreign contract was edited.

Mechanisms added: none. Friction is the foreign contracts above and unavailable
native sockets/service managers. Overwing reports its installed binary is ten
commits behind source but standards discovery succeeded. No worker process
survives the checks. Git has no local identity or core.worktree override. Scratch
is removed after checks join, and the shared build cache is retained.

STATUS: CONTINUE
