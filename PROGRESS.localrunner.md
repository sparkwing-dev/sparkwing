# Local runner migration progress

Step 3 remains incomplete. Do not land or push. Keep this note uncommitted.

Latest commits: 09bb338c1 moves the production local queue's execution hosts to
runners.NativeHosting over the authenticated API socket. b9d8401d0 updates docs
and ownership guards. cmd/sparkwing/local_host.go has no store bindings. The
embedded claimant/loop still run in the controller. Legacy StoreHosting remains
only for test fixtures and must go with the embedded runner.

Local cache profiles now bind from sealed inputs for trigger and independent
node awards. Publication reads terminal history with verified peer authority,
after native host cleanup/log flush, and keeps profile data credentials narrow.
Tests cover cache credential cleanup, publication order/success/refusal, checkout
redaction, opt-in, no node publication, and unchanged local verdict on failure.
No SDK, wire or schema changes. A preterminal host failure can now warn that
publication was unavailable instead of silently skipping publication.

Validation: full CLI (252.047s), localcontroller (6.647s), pipeline jobs (88.919s),
focused profile tests, real queued native schedule integration (3.554s), doccheck
suite, vet, lint/depguard, fmt, commentcheck, sleepcheck and source-doc checks pass.
Full acceptance passes (142.085s) after installing the missing locked web tsx
and SDK TypeScript compiler dependencies without changing source or locks.
Original setup failures, focused recovery and final full run logs are retained
in .sparkwing/cache/localrunner-profile-hosts/. Scratch removed. Xwing context
was unavailable for this fixed checkout; used repository Go checks.

Next: durable PID/start/version/build records and atomic activation/drain using
the same store claim-queue lock as claimUnit; a registered successor must not
claim until activation. Start after API listening. A runner-side lifecycle watch
calls Loop.Drain even at full host capacity; awards remain valid through uploads
and terminal acknowledgement. Use Loop, not ManagedRunner's separate updater.
Extract labels/profile policy from Claimant, then remove its token issuance and
revocation. Keep stable machine placement separate from process-instance IDs.

Add detached systemd user services (setsid only on confirmed no-systemd), separate
launchd agents and Windows SCM services. Existing controller unit/spawn helpers
do not provide those lifetimes. Then move foreground/here hosting and terminal
observation into the runner, delete remaining embedded adapters, and prove real
restart/roll/service survival, including an old owner awaiting a child. Native
Windows service/process/pipe parity remains untested. Prior restart polling,
observed exits, orphan cleanup and measured cancel tests already passed the lead
gate. Previous broad tagged-integration baseline failures and logs remain in
.sparkwing/cache/localrunner-http-hosts/; do not redo them without a scope reason.
