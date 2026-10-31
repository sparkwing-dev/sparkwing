# Migrating to the next release

## Recreate the store for schema baseline 28

This release changes the SQLite and PostgreSQL baseline directly. There is no
forward migration. Stores at another baseline refuse startup and remain
unchanged. Staging and production require a schema wipe.

Before upgrading, stop controllers and runners and back up the database. For a
disposable SQLite store, move the database and any WAL/SHM files aside. For
PostgreSQL, point the controller at a new empty database. Recreate teams,
secrets, credentials and schedules, then enroll runners again. Keep the backup
with its matching binary if its history is needed. See
[backup and restore](../backup-restore.md#schema-compatibility).

The baseline removes unused session, capability, node, trigger and storage
columns and the insert-only team counter table. It requires complete financial
decisions, nonempty credit grant references, positive award generations and
one open billing interval per award.

## Sealed execution trust

Intake now seals repository proof and verified default-branch eligibility.
Claim issuance seals host kind and cache scope. These facts govern every trust
consumer and survive delegation unchanged. The schema baseline is 28; see the store
recreation section above.

Personal machines and here hosts cannot claim repository-proven runs, including
run-now and burst `machines` work. Use a shared runner or Sparkwing Cloud.
Repository-scoped runners reject asserted uploads even when the remote name
matches `--repos`. Omit `--repos` when enrolling a personal runner so it can
claim its owner's asserted work. Shared enrollment still requires a repository
scope.

Job source credentials require both repository proof and a successful intake
GitHub compare check. Host-wide credentials no longer bypass proof. Uploaded,
local and unproven remote code receive no team credential. A historical commit
or locked schedule can qualify after App source selection and a compare result
of `ahead` or `identical` against a repository branch or tag for both source and
execution commits when they differ. Merged PR runs compare against their base
branch. Verified cache fallbacks require sealed repository proof. Such a historical
commit carries no resolved-head OIDC `ref` or `sha`.

Pipeline references now select verified default-branch runs and exclude
publication. To accept other producers, declare `sw.FromAnyRun()` in Go or
`{fromAnyRun: true}` in TypeScript. The direct output route also requires that
declaration. Existing unverified outputs no longer satisfy the default lookup.

## Capacity profiles report measured data

Sizing and capacity responses drop stored resource pins, demand floors,
queue-wait percentiles and contention counters. Update clients that decode
those fields or the pin/floor estimate sources. The console and CLI continue
to show measured resource distributions and predecessor estimates. Local daemon
admission resource pins retain their existing behavior.

The OpenAPI schemas `components.schemas.internal.controllerwire.JobSizing`
and `components.schemas.internal.controllerwire.SizingMeasurements` remove
`pinned_cores`, `pinned_memory_bytes`, `floor_cores`, `floor_memory_bytes`,
`wait_p50_ns`, `wait_p99_ns`, `wait_sample_count` and `contended_count`.
`components.schemas.internal.controllerwire.SizingDetail` removes `drift`.
Remove dependencies on these fields before upgrading.

## Teams have no member cap

Team membership is unlimited for free, funded and payment-held teams. Remove
`free_team_members` from operator signup requests and response handling. The
invitation creation response drops `warning`, and acceptance no longer returns
`team_full`. Keep handling `invitation_not_found` and `invitation_closed`.
The inviter must still hold team administration, and the accepting account
must own the invited address.

The unreleased schema baseline remains 28 and removes
`signup_gate.free_team_members`. Recreate pre-release stores with the updated
baseline using the instructions above.
