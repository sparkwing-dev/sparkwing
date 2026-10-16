# W1 continuation: credential reader retirement and local-first placement

W1 remains incomplete. This session starts from `7a18b8c82`. The initial
whole-repository build passes. There are no merge conflicts or Git writes.
W1b files, materialization and lead-owned contracts are unchanged.

## Changes and deletion

- Removed token-table authentication, prefix lookup, listing and revocation:
  `LookupToken`, `LookupTokenByPrefix`, `ListTokens`, `RevokeToken` and their
  private readers, verifier, sliding-use writer and unused rejection errors.
  Current capability snapshots, inventory and committing mutations own those
  operations. The console test fixture now accepts only its session input.
- Removed the unused raw here issuer, its request type and principal helpers.
  Here admission and issuance already use `AdmitRun` and `Issue`. The remaining
  legacy claim path reads `limits.ClaimWindow` instead of duplicating the limit.
- Removed unused claim-origin readers, `ClaimedRunFor` and its result type,
  `Tenant.ListAgentClaims` and `AgentClaim`. Current secret reads retain live
  binding, tenant, pipeline and declared-secret checks.
- Connected configured default preferences, hold and liveness to shared job
  claims and reconciliation. Named and queued claims use the same preference
  check and hold deadline. Eligibility and preference scores come from the
  shared matcher. Successful awards persist their placement reason.
- Replaced `RecordAgentLabels` with `RecordRunnerPoll(ctx, UnitDemand)`.
  The current HTTP claim path records machine capacity and labels with store
  time. Reserved labels grant no identity. A hold rechecks current same-team
  authority, job-claim grants, ancestry, repository scope, selectors, pools and
  free capacity. Revoked, expired, stale or ineligible observations earn no hold.
  Unknown capacity earns no hold. Held polls change no job, award or event.

Production delta from HEAD is **-313 lines**, with 462 deleted and 149 added.
The store itself is **-318**. The two deleted production files remove 120 lines.
Combined store delta against main is **+481**, against the -4,250 budget, or
**4,731 over**. This is incomplete deletion work, not a budget exception.

## Invariants and ports

Authentication selects only the live credential matching the complete digest
and never extends its lifetime. The generated digest property mutates issued
presentations, checks exact selection and unchanged persistence, and refuses
revoked and expired credentials. Existing ancestry and membership properties
remain green.

Only a live authorized runner with enough advertised capacity may hold matching
work for a preference. The generated placement property covers named and queued
claims, declared and configured preferences, stale/revoked/expired authority,
foreign teams, repository exclusion, missing job grants, insufficient and unknown
capacity, hard selectors, the exact hold deadline and granted-name preferences
against forged labels. An overlay disabling placement reproduced the missing
hold. The unknown-capacity case also ran red before its correction.

The HTTP placement test uses the real router and store. It records an idle
preferred runner, holds fallback and awards the preferred runner. Enrollment
and software-policy boundaries are faked. The port inventory names the retired
lookup, inventory, expiry, here, secret-scope and agent-report tests and their
current replacements. No native acceptance cell is newly claimed.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and GOTMPDIR under /tmp/sw-astra-W1. Final checks also
set TMPDIR there. Tests use -p 4. Scratch is removed after checks. The shared
cache is retained.

- All 21 W1 plain acceptance functions pass, in 4.108s.
- Scoped store/controller/capability tests pass, in 11.296s / 2.313s / 0.008s.
  This includes authentication, inventory, rotation, secret reads, current claims,
  queue matching, placement, registration, SQL scope and HTTP claims.
- The race selection for generated authentication and placement, registered
  observations and HTTP placement passes, in 75.514s / 3.047s. A final race
  selection covering the unknown-capacity correction and HTTP claims passes,
  in 46.752s / 13.763s.
- Whole-repository build and scoped store/controller vet pass. Acceptance and
  documentation test packages compile. Embedded changelog and migration checks
  pass. API generation changes only pkg_store.txt. Documentation mirrors match.
- Commentcheck and whitespace checks pass.

The broader legacy selections fail before exercising their old behavior:
`TestStoreFinishRefusesAnotherTeamsNode` and the legacy `TestClaimPlacement_*`
fixtures write removed `runs.args_json`. Legacy token mint fixtures reference
the removed `tokens` table. They are not passing evidence. No whole-repository
test run, native PostgreSQL, socket service, product binary, pipeline or release
action was run.

## Contract changes, assumptions and remaining work

The C1-local change replaces the label-only observation method and adds
`ClaimPlacement.Liveness` and nullable `capabilities.claim_observation_json`.
Observed resources need persistence because label-only rows cannot establish
free capacity across controller replicas. Keeping them on their capability row
avoids a second runner registry or process-local authority cache. Reverting
that column and observer replacement removes the addition. Existing hold and
liveness settings remain the only deadlines. No new customer restriction,
flag, process, cache or environment variable is added.

No contract change in another area is required by this slice. These remain open:

- Trusted schedule and verified App admission through the sealed intake, then
  deletion of `triggerIntake`, `triggerRows` and raw insertion. This is still the
  lead's 14:40(g) W1 assignment. The 14:40(f) placement path is now implemented
  through shared claims and HTTP with the boundaries stated above.
- Further legacy node/trigger queue, token construction, lifecycle and
  StateStoreOld cuts. Their fixtures include W1b billing callers. W1b files were
  left untouched. Retained legacy token writers have no current schema table.
- Console committing authority, runner metering, complete runner inventory and
  renewal/headroom observations, pool/repository management and remaining local
  composition. Poll observations now persist, but this is not a completed
  `ReadRunners` or native host-observation integration.
- Exact HTTP result-replay provenance still needs C2's trusted request digest,
  method and pattern in the committing contract. Current canonical store replay
  remains covered. No context side channel was added for it.
- W1b still owns accounting, settlement, blobs, logs and materialization end to
  end. Native PostgreSQL locking, real transport/placement and independent review
  remain with the lead and area owners.

Prior completed seeded bootstrap, rotation grace, SyncLauncher store fencing,
credit-method ownership, harness movement and dead contract-table removal remain
intact. Their current acceptance and rotation tests pass.

## Shared files and friction

Outside pkg/store, code/test edits are limited to pkg/controller
`engine_claim.go`, `engine_claim_routes.go`, `engine_claim_http_test.go`,
`claim_attention_internal_test.go` and `console_fixture_test.go`. These carry
poll observations and placement configuration, test HTTP composition and remove
the unused fixture bearer branch. There are no other package caller migrations.

The other shared files are `.apidiff/pkg_store.txt`, C1 and the test port
inventory, authentication docs, the migration guide, CHANGELOG.md and their
embedded mirrors. Lead-owned handoffs and amendment files are unchanged.

Friction: broad test-name prefixes also select legacy fixtures whose schema is
gone. The current acceptance selection is derived from acceptance_*_test.go so
it names exactly the 21 current functions. No compatibility API was restored.

STATUS: CONTINUE
