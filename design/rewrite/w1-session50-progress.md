# W1 session 50: metadata writers and legacy settlement retirement

W1 remains incomplete. HEAD is unchanged at `192442f95`. The integration merge
has working-tree resolutions for every conflict and no conflict markers remain.
Integration-deleted files remain deleted. The swarm prohibits Git writes, so the
lead must stage the resolutions and finish the merge. No Git write ran.

The first build after resolving contents passed. Controller test compilation
then exposed retired APIs restored by the conflicting test file. The final
resolution preserves W1's prior claim/secret test cuts and W3's concurrency cuts.
W3 conflict files use integration's native implementations and tests.

## Changes and volume

Removed raw Store/Tenant plan-snapshot, step, annotation, summary, activity and
approval writers. Removed their old annotation limits/errors, step validator,
default-team approval getter, cross-run input resolver and memo-key codec.
Removed `OwnsRun`, `ListPendingTriggersForParent`, `RunExceedsWallClock`,
`StateStoreOld`, its conformance test and the unused settlement export fixture.
The old approval resolver was the only production caller of the old settlement
engine. Both are deleted, including their separate decider and state writers.

Porting the unfinished-step test reproduced a current defect for success,
failure and cancellation: TransitionNode ended an attempt but left its steps
running. Its attempt-closing transaction now closes only that tenant/job/attempt/
generation's running steps. Retry steps stay separate. A failed completion event
rolls back job, attempt and step closure together. This adds ten production lines
inside the existing transition owner and no new mechanism or configuration.

Session delta against the merge's initial staged contents is **20 added,
1,440 removed, net -1,420 production Go lines**, excluding the acceptance
harness. Another **197 orphaned harness lines** are deleted from
`internal/acceptance/store_recovery.go`. Its final W3 caller has migrated and
its store checks already run in store tests. Including that harness deletion,
the session delta is -1,617. Imported integration changes are not counted as
session work.

Combined W1+W1b production delta against main is **-3,316** (17,705 added,
21,021 removed), **934 lines over** the -4,250 target. This uses the lead's
exclusions for tests, testdata, acceptance and contract-named files. There is no
budget exception claim. Remaining legacy code prevents COMPLETE.

## Test ports and evidence

The 24 old tests in summary, annotation-concurrent, step-status, approval and
generated-node files are replaced by current API checks. Metadata ports use real
sealed intake, provisioning, acceptance, claim and start transactions. They cover
job/step summary overwrite and repeat, reopen persistence, concurrent annotations,
count/byte limits, refusal rollback, missing-step refusal, terminal-step
immutability, unfinished-step completion and retry isolation. The generated
claim-fence property checks unchanged job and step metadata after forged writes.

Approval ports cover opening through transitions, decision attribution/comments,
exact replay, refusal to overwrite/reopen, foreign tenants, missing targets,
pending request order and retained resolved rows. Existing reaper, race and
rollback tests remain. Generated spec/reference assertions now check canonical
hashes and immutable accepted expansion declarations. The controller's native
pipeline-latest reference lookup remains open with W2. The property does not
claim that integration cell. Details are in the updated port inventory.

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR/TMPDIR under /tmp/sw-astra-W1 and package parallelism at most four.

- Whole-repository `go build -p 4 ./...` passes after the final production edits.
- Store/controller/storage/nodehost scoped vet passes. Those dependents compile.
- All 21 W1 acceptance functions in `acceptance_*_test.go` pass, alongside
  metadata, approval, generated-spec and approval-reaper selections (8.591s).
  Added ordering and completion-rollback cases pass separately.
- Metadata, approval, generated-reference, grace, bootstrap and launcher snapshot
  race selection passes (75.865s). The later completion-rollback test has ordinary
  test evidence only. No new native placement cell is enabled.
- Merged nodehost closure, debug, dispatch, expansion, retry and approval tests
  pass (2.854s). Controller run-control/business/metrics selections pass (0.749s).
- Inherited placement metering, runner console authority, bootstrap, rotation
  grace and launcher snapshot checks pass (3.969s).
- Commentcheck against main passes across 2,243 Go files. API snapshots,
  changelog, embedded docs/migration drift, focused docs tests and whitespace
  checks pass. Git emitted only its large-diff rename-detection warning.

PostgreSQL metadata coverage is ported but skipped without a configured database.
No whole-repository test suite, pipeline, socket service, release or independent
review is claimed. Those remain lead work.

## Remaining contracts and integration

No foreign contract signature is added. Existing bootstrap seed, rotation grace,
launcher store fence, local-first matching and console runner metering are kept
and verified. The six dead contract tables from 09:40 remain deleted.
Materialization and W1b's production files are untouched.

- Trusted schedule and verified App intake still need a declared committing
  origin reference. IntakeRequest requires capability/session authority, schedules
  retain an account-valued ArmedBy, and no production IntakePolicy supplies
  verified App facts. W2 must supply that authority/fact contract. Then W1 can
  remove triggerIntake, triggerRows and the raw insertion path.
- Full runner observations still lack immutable Cloud/here identity, enrolled
  concurrency and accepted-renewal persistence inputs. Queue-depth transport is
  W8/W9 work. Poll timestamps cannot substitute for accepted renewals.
- W2 owns the remaining committing direct-session gate, general ConsoleWriter,
  RunControlWriter and controller adapter composition under the 14:40 handoff.
  Native declared-reference lookup remains its controller integration boundary.
- Raw run/node lifecycle and token constructors remain. Their remaining legacy
  fixture callers include W1b's credit/billing tests, which this worker is not
  authorized to edit. The browser fixture still uses the raw token constructor.
  Those fixtures must move before the last constructors and old fence helpers
  can be removed. This session adds no test-only compatibility wrapper.
- Renewal-time spend exhaustion/grace, provider composition and payment-mode
  persistence checks remain with W1b/W2 as recorded in session 49. No billing,
  blob, log or materialization implementation was changed here.

## Shared files and handoff

Outside store, source changes remove dead `runLimitStatus` from
`internal/controller/metrics.go` and `StateStoreOld` from `internal/storage/state.go`.
`internal/controller/team_boundary_test.go` replaces one removed approval fixture call
with a tenant-bound fixture insert. `internal/controller/claim_run_test.go` preserves
both branches' compatible API retirement. The settlement-only test export is
removed from `claim_tokens_export_internal_test.go`; its billing helper remains.

Merge resolutions also touch the listed nodehost conflict tests, delete
integration-removed attempt/expansion files, preserve both migration notes and
both areas' port-inventory additions, and delete already-moved capacity harnesses.
C1, the port inventory, CHANGELOG, the unreleased migration, their embedded copies
and the store/storage API snapshots are updated. Lead-owned amendments and
handoffs are unchanged.

Friction: Git's index remains unmerged because Git writes are prohibited. The
lead must stage all resolved contents/deletions, including the three new port
test files, and finish the merge. HEAD and Git identity are unchanged. Scratch
is removed at handoff; the shared build cache is retained.

STATUS: CONTINUE
