# Restore workstream handoff

Sessions 1 and 2 are committed as `bc8e884eb` and `ad07ea0e2`. The harness started the next integration merge and owns its completion and subsequent commits.

Implemented external credential epochs, immutable bearer/session fencing, explicit offline prepare/reconcile/acknowledge commands, startup detection of epoch changes, required provider pull plus hourly sweeps through SettlePayment, durable schedule suppression, and directional schema errors with generated documentation and drift checks. Preparation always rotates, including retries, because a backup may already contain a pending restore.

Net production Go: +724 lines; SQL: +1. The growth pays for persistent epoch state, an offline operation with controller exclusion, recovery state, and complete provider-object sweeps. Directory syncing now has one shared owner instead of separate key and epoch implementations. No masking, claim/delivery, or admission redesign files were edited.

Verification is recorded in WS15-test-output.txt. The real SQLite backup drill proves revoked-token refusal, recovery of post-backup payment exactly once, and suppression of previously fired cron instants. Normal and tagged focused tests, leaf suites, and touched-package vet passed. Full suites, PostgreSQL (including the new restore/controller exclusion test), and socket tests belong to the lead.

Session 2 removes the unused `EpochPath` export and its comment (net -1 production line). The existing unexport guard caught the defect; its allow-list stays unchanged. Full guardcheck, the credentials package, and normal/tagged focused restore tests pass. No new mechanism or behavioral test was needed for this visibility change. The lead's prior full run passed the store suites but reported chaos convergence, nodehost admission, and missing shared Go cache artifacts; those remain with their owners and the lead.

Session 3 resolves both integration conflicts in the working files: retain schema 20 and the restore, audit-protection and tombstone SQL guard exemptions. The harness must mark the files resolved and finish the merge because this session cannot stage or commit. The existing schema-error drift test failed against the old examples. Regeneration updates them for schema 20, and `bin/regen-all.sh` now invokes the existing store generator before syncing embedded docs. The doc sync also refreshes mirrors changed by integration. Net new production Go and SQL: 0. Shell: +1 line, which makes the documented regeneration path own the schema examples. No redesign-lane files or guard allow-lists changed beyond conflict resolution.

Focused normal and `rewrite_integration` restore, epoch, payment-pull, cron, directional-schema and SQL guard tests pass, as does full guardcheck. Shell syntax, embedded-doc drift and diff whitespace checks pass. No new tests were needed because the existing drift test catches the stale examples. The lead's latest test output reports passing store suites and guardcheck, with `TestExpansionFansOutAndHoldsItsWaiter` failing in the admission lane. Full suites, PostgreSQL, socket checks and independent review remain with the lead. Candidate inspection still cannot create Xwing's session-index lock inside the sandbox.

Session 4 fixes stale reconciliation success. A successful sweep followed by
a failed resweep previously left `Reconciled=true`, allowing acknowledgment.
`ReconcileRestore` now commits the reset before calling the provider. A failed
or interrupted call keeps acknowledgment blocked until another sweep succeeds.
The new regression failed on the previous code and covers acknowledgment
during the call, after failure, and after a successful retry. This adds 8
production Go lines to the existing restore owner, for cumulative WS15 growth
of 732 Go lines and 1 SQL line. No parallel redesign files changed.

Focused normal and tagged checks pass. Independent correctness review found
no actionable issue. The lead's two reported authority failures both passed
in isolation. A 20-run repeat reproduced the named-job failure twice, at
`named_job_scope_test.go:67`, with `authority expired while ancestry was locked`
and `authority has ended`. The peer-placement test passed all 20 runs. This
does not establish the cause of either failure. They are outside the restore
diff. Test commands and output are in `WS15-test-output.txt`.

## Lead review corrections

The ws15a startup, trusted-file, purge, card ordering, detached-card, reporting
and drill findings are fixed. The usage-recovery finding remains deferred to
Korey. No usage journal was added.

Startup, admin and restore share the epoch directory. Helm retains its dedicated
PVC across pod and database replacement. Epoch reads use OpenCredential.
Provider-parent classification distinguishes recorded, purged and missing
identities using redacted closure evidence committed with parent erasure.
Setup ordering uses provider completion events or retained completion evidence;
missing evidence holds acknowledgment after monetary recovery. Detached methods
remain historical evidence, clear the latest saved selection, and preserve its
completion watermark. Webhooks and pull recovery share reporting. Offline
reconciliation accepts the controller's mail settings. The corrected drill uses
a normal backup, supported revocation and actual cron admission.

This session adds 162 production Go lines, 8 SQL lines and 29 Helm/values lines.
Cumulative production growth is 894 Go lines and 9 SQL lines. The growth supplies
durable deployment storage, one parent-classification owner and distinct setup
completion/usability facts. The obsolete free reconciliation entrypoint and
forged drill envelope writes are removed. The store unexport allowance shrinks
from 261 to 260. No parallel redesign files changed.

Focused normal and rewrite_integration tests, leaf suites, all chart tests,
provider replay tests, acceptance payment tests, doccheck, vet and guardcheck
pass. New regressions demonstrated the reported failures before their fixes.
Independent correctness review caught and resolved an identical-setup replay
regression. Commands and evidence are in WS15-test-output.txt. Full repository
suites, PostgreSQL and socket-dependent checks remain with the harness lead.
No commits were made. Xwing candidate inspection remains blocked by its
read-only session lock. The task used the selected worktree directly.

## Continuation after lead verification

The worktree started clean at `36f09a7a6`, which already contains all seven
unblocked ws15a corrections. Integration `a23a6a777` is an ancestor of HEAD.
No merge or further production change was needed. This continuation adds zero
production lines and no mechanism. The earlier cumulative growth remains
894 Go lines, 9 SQL lines and 29 Helm/values lines.

Normal and tagged focused restore, payment-pull, epoch, directional-schema and
Helm epoch render checks pass. Complete credentials, fssecure and capability
suites, doccheck, documentation mirror drift and guardcheck also pass.
The lead's full SQLite and PostgreSQL store suites passed. Its full run still
reports controller authority failures and a nodehost lease-authority failure.

A ten-run repeat reproduced two secrets-envelope authority failures. The retry
and metadata-selector tests passed all ten runs. A narrower 40-run secrets
probe with temporary lifetime diagnostics passed, so the cause is still
undiagnosed. Diagnostics were removed. No claims, admission, masking or
fixture changes were made. Exact commands and results are in
`WS15-test-output.txt`. Full suites, PostgreSQL and socket checks remain with
the lead. No commits were made. Scratch was removed after verification.

## Decisions needing the lead

B63 is covered. B61 remains partial: authoritative recovery of post-backup usage
and lost reservations waits for Korey. Missing correlations continue to block
restore acknowledgment.
