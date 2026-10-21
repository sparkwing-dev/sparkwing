# Restore workstream handoff

Sessions 1 and 2 are committed as `bc8e884eb` and `ad07ea0e2`. The harness started the next integration merge and owns its completion and subsequent commits.

Implemented external credential epochs, immutable bearer/session fencing, explicit offline prepare/reconcile/acknowledge commands, startup detection of epoch changes, required provider pull plus hourly sweeps through SettlePayment, durable schedule suppression, and directional schema errors with generated documentation and drift checks. Preparation always rotates, including retries, because a backup may already contain a pending restore.

Net production Go: +724 lines; SQL: +1. The growth pays for persistent epoch state, an offline operation with controller exclusion, recovery state, and complete provider-object sweeps. Directory syncing now has one shared owner instead of separate key and epoch implementations. No masking, claim/delivery, or admission redesign files were edited.

Verification is recorded in WS15-test-output.txt. The real SQLite backup drill proves revoked-token refusal, recovery of post-backup payment exactly once, and suppression of previously fired cron instants. Normal and tagged focused tests, leaf suites, and touched-package vet passed. Full suites, PostgreSQL (including the new restore/controller exclusion test), and socket tests belong to the lead.

Session 2 removes the unused `EpochPath` export and its comment (net -1 production line). The existing unexport guard caught the defect; its allow-list stays unchanged. Full guardcheck, the credentials package, and normal/tagged focused restore tests pass. No new mechanism or behavioral test was needed for this visibility change. The lead's prior full run passed the store suites but reported chaos convergence, nodehost admission, and missing shared Go cache artifacts; those remain with their owners and the lead.

Session 3 resolves both integration conflicts in the working files: retain schema 20 and the restore, audit-protection and tombstone SQL guard exemptions. The harness must mark the files resolved and finish the merge because this session cannot stage or commit. The existing schema-error drift test failed against the old examples. Regeneration updates them for schema 20, and `bin/regen-all.sh` now invokes the existing store generator before syncing embedded docs. The doc sync also refreshes mirrors changed by integration. Net new production Go and SQL: 0. Shell: +1 line, which makes the documented regeneration path own the schema examples. No redesign-lane files or guard allow-lists changed beyond conflict resolution.

Focused normal and `rewrite_integration` restore, epoch, payment-pull, cron, directional-schema and SQL guard tests pass, as does full guardcheck. Shell syntax, embedded-doc drift and diff whitespace checks pass. No new tests were needed because the existing drift test catches the stale examples. The lead's latest test output reports passing store suites and guardcheck, with `TestExpansionFansOutAndHoldsItsWaiter` failing in the admission lane. Full suites, PostgreSQL, socket checks and independent review remain with the lead. Candidate inspection still cannot create Xwing's session-index lock inside the sandbox.

## Decisions needing the lead

B63 is covered. B61 cannot be fully closed without authoritative recovery of reservations created after the backup and usage lost after it. Provider metadata only routes payments; it cannot recreate trusted team correlations. Uncorrelated pulls fail before settlement and keep the restore held. Recommend an external authoritative correlation/usage journal rather than weakening settlement authority. The limitation is explicit in the operator and migration docs.
