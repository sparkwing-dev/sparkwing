# Restore workstream handoff

The integration branch already matched HEAD. No Git mutations succeeded and no commits were made; the harness owns committing.

Implemented external credential epochs, immutable bearer/session fencing, explicit offline prepare/reconcile/acknowledge commands, startup detection of epoch changes, required provider pull plus hourly sweeps through SettlePayment, durable schedule suppression, and directional schema errors with generated documentation and drift checks. Preparation always rotates, including retries, because a backup may already contain a pending restore.

Net production Go: +725 lines; SQL: +1. The growth pays for persistent epoch state, an offline operation with controller exclusion, recovery state, and complete provider-object sweeps. Directory syncing now has one shared owner instead of separate key and epoch implementations. No masking, claim/delivery, or admission redesign files were edited.

Verification is recorded in WS15-test-output.txt. The real SQLite backup drill proves revoked-token refusal, recovery of post-backup payment exactly once, and suppression of previously fired cron instants. Normal and tagged focused tests, leaf suites, and touched-package vet passed. Full suites, PostgreSQL (including the new restore/controller exclusion test), and socket tests belong to the lead.

## Decisions needing the lead

B63 is covered. B61 cannot be fully closed without authoritative recovery of reservations created after the backup and usage lost after it. Provider metadata only routes payments; it cannot recreate trusted team correlations. Uncorrelated pulls fail before settlement and keep the restore held. Recommend an external authoritative correlation/usage journal rather than weakening settlement authority. The limitation is explicit in the operator and migration docs.
