# Burst placement and overflow

The invariant is that a personal machine can claim only runs submitted by its account owner. Machine labels never supply ownership, runner identity, pool authority or claimant class. Personal-machine property tests generate account identities, forged fixed labels, queued and named claims, and account/machine ID collisions.

The overflow invariant is that a request can only narrow its account's stored budget. Machine authority cannot read or change account budgets, including when it carries account-like grants. Property tests generate caps and machine identities against the real store.

## Contract changes made

- `Request.Burst` seals `BurstPlacement{Preferences []PlacementStep{Target, Hold}, OverflowMinutes *int64}`. Targets are `here`, `machines`, `cloud`, `name=<runner>` and `pool=<pool>`. Invalid, duplicate and overflowing holds are refused before intake. Account authority is required for burst intake.
- Sealed burst preferences feed W1's existing `ClaimPlacement`/`PlacementPreference` matcher. They override the controller's default soft placement order. Node requirements still govern eligibility. Owner snapshot projection, reconciliation, named claims and queued claims share the matcher. `machines` resolves registered names from the authenticated requesting account's registrations.
- Burst relaxes the sealed `RunnerHere` node exclusivity because Cloud owns this run and its nodes can overflow. The trigger remains bound to the original here claimant. The run keeps one owner.
- `ClaimRequest` adds optional `Capacity`, `OS` and `Arch` fields. Only the authenticated here bootstrap uses caller capacity as its own machine observation. Initial owner acquisition copies the observation to the awarded owner. `RenewalRequest.Available` updates that owner's current availability through `RecordHereAvailability`, with a live trigger fence. Saturated earlier hosts do not hold later claimants.
- `ProvisionSpec.Personal`, `CapabilityOrigin.Personal`, enrollment JSON `personal`, and `client.EnrollRunnerRequest.Personal` select account-owned machine capacity. Only direct account sessions can register personal machines. Ownership comes from the existing `created_by` record. Rotation preserves the personal marker and creator. Team-machine registration stays available.
- The schema adds `runs.created_subject_kind` because account and machine IDs can otherwise collide in the existing `created_principal` string. Intake persists the authenticated subject kind, including for retries. Personal claims require account kind and matching owner ID.
- The schema adds tenant-owned `overflow_controls(team, account_id, monthly_minutes)`, with account deletion cleanup through its foreign key. The rewrite baseline advances from 4 to 5. Existing version-4 stores are refused and native QA needs a fresh store.
- `Tenant.SetOverflowLimit` and `ReadOverflowControl` recheck live account authority. `GET/PUT /api/v1/overflow` reads or changes only that account's allowance for the current team. `monthly_minutes: null` removes the cap. The default has no cap. A request cap narrows the stored allowance.
- Budget usage includes that account's metered Cloud work in the team. The period starts on the first UTC day of the month. Active Cloud reservations and observed compute time count before another award. Named and queued Cloud awards enforce the allowance in addition to the standing tier, card ladder and team budgets. Existing work finishes. Local and customer-owned machine awards remain eligible.

## Integration

The CLI owner supplies actual machine capacity and free capacity at the initial here claim, then current free capacity through `RemoteHostStore` renewals. CLI route configuration supplies the sealed burst preferences. The CLI owner exposes personal enrollment and overflow controls. Native execution remains the lead's verification responsibility.

## Verification

Tests were written red before the corresponding pieces. Real store tests cover personal registration, owner authorization, multiple registered machines, sealed preference order, holds, saturation, here owner observation and default/zero/narrowed budgets. Controller recorder tests cover the actual authenticated routes and Cloud award paths, including queued refusal and continuation on unmetered capacity. Request-seal tests cover validation and immutable placement inputs.

A broader controller selection reaches an existing socket-based flood test that cannot run in this sandbox. The tenant SQL guard also found an existing unscoped source-upload expiry query, handed to the parent. The new burst tests use in-process HTTP recorders and do not require sockets.

## Scope and size

This is the newly authorized launch vertical. It adds placement, ownership and budget behavior that the original W9 deletion budget did not include. No legacy scheduler or parallel run owner is introduced. No replaced legacy code exists within this added store/controller slice.

Personal enrollment permits editors with `runs.write` through an independent account session. Team-machine enrollment still requires team administration, checked again in the store. The real account-session route test covers enrollment, refusal of another user's run and successful claiming of the owner's run.

The CLI member-bearer path cannot establish independent machine consent. The actual `NewRunnerTokenManager.EnrollRunner` transport test verifies that it is refused. CLI personal enrollment therefore needs an independent session transport or a separately reviewed bounded-ancestry issuance contract. No bearer exception was added to the existing direct-account-session boundary.

Measured owned/shared production Go changes against the starting HEAD are +539/-15, or +524 lines. Shared request and intake files also contain the concurrent secret slice. The five new production files comprise 407 lines. The launch vertical requires a separate budget allowance because the frozen W9 target sized removal of the old CLI, not this feature.

Verification passed: whole-repository `go build -p 4 ./...`, `go vet -p 4` on the six touched packages, focused burst/overflow/here/placement tests, and `commentcheck -base main` (2655 files). Tests also cover cross-month active Cloud usage and the inability to substitute a machine identity with an account's ID.
