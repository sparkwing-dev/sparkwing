# W9 native CLI QA follow-up

Lead QA items 1-10 are complete. This continuation finishes working-tree source
submission and closes the production GitHub intake gaps found while following
that path. Items 1 and 4-10 remain implemented from the preceding commit.
No operator onboarding route, runtime deployment, release or Git write ran.

## Implementation

- `pipeline trigger --working-tree` captures the existing immutable Git snapshot,
  resolves its captured pipeline configuration, inspects a private checkout with
  the selected engine, commits the bundle through shared storage, and submits
  the resulting digest through sealed intake. A pinned pipeline revision is
  included in the bundle too. The user's checkout and index stay untouched.
- Committed triggers also inspect a clean private checkout. Git inspection uses
  the shared environment projection and disables inherited config, hooks and
  credential helpers. Scratch closes on failure and success.
- The two `/api/v1/data/source` operations authenticate an unbound member/local
  submitter with `runs.write`. Transactional tickets bind its exact tagged
  authority, team, server-verified repository identity and both source revisions.
  Backend verification precedes unique-byte accounting. Signing failure releases
  its hold; post-commit staging cleanup survives request cancellation.
- Intake consumes one matching committed upload, enforces run quota and records
  the committed charge in run usage. Deduplicated bytes cost zero. It creates
  a local `source_bundle` record with no App or verified-ref trust. Retries keep
  original provenance. The `sealed_source` read edge exposes only the awarded
  run's sealed digest through ordinary live data authority.
- Leased expiry deletes staged bytes before releasing tickets and quota. Upload
  tickets expire; retained source records follow run retention. The expiry
  query is indexed and locks selected rows.
- Awarded source acquisition supports the same configured HTTP/HTTPS signed
  endpoints as other shared objects. It checks out a bundled commit without
  fetching its synthetic SHA from origin or releasing an unnecessary Git
  credential. Missing pinned revisions and requested Git features still use
  credentialed fetches.
- CLI and controller share the production numeric GitHub ID lookup. Controllers
  use their configured App, confirmed team HTTPS source credential, or public
  API for public repositories. CLI environment tokens are never forwarded to
  the controller. Caller display names grant no repository authority. A private
  intake context proof carries verified identity into the transaction without
  opening a second SQLite connection there.
- Updated setup/auth/deployment documentation, API reference/OpenAPI and embedded
  mirrors. Isolated profile writes still require both `SPARKWING_HOME` and
  `SPARKWING_CONFIG`. The Kubernetes provisioning step remains explicitly
  `PENDING operator onboarding`, as assigned.

## Verification

The invariant is: an execution reads only the committed source digest sealed
for its authorized submitter, team, repository and selected revisions; source
submission creates no verified-ref or App trust.

Passing evidence:

- Real client -> controller router -> SQLite reservation -> filesystem signed
  PUT -> verified commit -> intake -> actual owner award -> signed download ->
  native Git bundle acquisition (`TestSourceSnapshotRealClientStorageAndIntake`).
- Properties cover forged revisions/digests/repositories, changed or missing
  intake proofs, forged display names under repository name scopes, and ambient
  Git identity overrides. The existing GitHub secret/redirect property passes.
- Real public and configured-private GitHub intake tests, one-use submission,
  replay and deduplicated run accounting; signing failure quota rollback;
  membership removal between PUT and commit followed by physical staging expiry;
  deterministic expiry/quota tests; captured dirty/untracked source, pinned
  revision inclusion and committed inspection checkout tests.
- Whole-repository `go build -p 4 ./...`; focused vet; focused CLI/controller/store,
  schema, API/docs and `TestSurface*` acceptance selections.
- Full source, blob, sourceurl, capability, controller client, controllerwire and
  clisurface package suites.
- `commentcheck -base main`: clean across 2,622 files; generated API docs and
  embedded mirror drift; shell syntax; `git diff --check`.

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, private scratch and at most four package workers.
No archived test was newly ported. The live-describe fixture now emits the v2
invoked pipeline name; it no longer returns the registered symbol as the name.
No native deployment acceptance cell was newly marked passed.

The separate full credential-route inventory selection still fails on inherited
local-host/node-process alternative drift and four serving registrations (doctor,
refunds, job output and claim input). These were already failing before the
source binding; the new source routes have explicit matching expectations.
No claim of a green whole-repository test suite is made.

## Contract changes made

C1 adds tenant `source_uploads`, its transactional lifecycle, committed-charge
accounting, source record binding, retry provenance and leased expiry. C2 adds
the two member submission routes and the sealed-source read edge. C3 adds the
shared source request and production upload/receipt flow. C4 records immutable
CLI capture and inspection. The corresponding contract files contain these
amendments. No lead-owned amendments or handoffs file was edited.

The rewrite baseline advances from **3 to 4**. The existing hard-cut policy
refuses older stores and upgrades none. Native QA needs a fresh development
store; no operational state was opened or changed here.

Mechanisms added: a persisted pre-intake ticket and a source-only read edge.
A normal job/owner upload needs a persisted run, while manufacturing a
publication run would create false visible work. The private intake proof avoids
provider/DB lookup inside the admission transaction and is absent from the wire
and seal. These mechanisms are within the assigned integration slice; the lead
reviews the recorded contract changes. Independent review found no remaining
concrete blocker after the fixes, including its Simplify assessment.

## Deletions, budget and shared paths

Deleted the refusal guards and unused `SourceKeyDigest`; moved the GitHub adapter
into the shared implementation. No removed API had a remaining caller. Existing
`validUploadKey` callers remain intact. Production Go this continuation:
**+837/-126, net +711** across all touched areas; W9-owned **+208/-71, net +137**.
Cumulative W9 versus main: **+17,242/-23,818, net -6,576**, within its -2,900 budget.

Shared Go paths outside W9 ownership:

- `internal/apispec/wire.go`
- `internal/blob/authority.go`, `internal/blob/types.go`
- `internal/capability/routes.go`
- `internal/source/bundle.go`, `internal/source/checkout.go`
- `internal/sourceurl/github.go`
- `pkg/controller/credential_route_expectations_test.go`
- `pkg/controller/data_storage.go`, `pkg/controller/engine_routes.go`
- `pkg/controller/intake_policy.go`, `pkg/controller/intake_source.go` and its test
- `pkg/controller/server.go`, `pkg/controller/source_upload.go` and its test
- `pkg/store/blob_accounting.go`, `pkg/store/blob_gc.go`
- `pkg/store/blob_persistence.go`, `pkg/store/blob_read_authority.go`
- `pkg/store/direct_upload.go`, `pkg/store/intake_runtime.go`
- `pkg/store/schema.go`, `pkg/store/source_upload.go` and its test
- `pkg/store/store.go`

Also changed shared `pkg/store/schema.sql`, generated `api/openapi.yaml` and
`pkg/docs/mirror/`. Source docs and the four owning contract files are updated.

## Integration points and friction

The lead retains installed CLI/selected-engine execution, native network/S3,
PostgreSQL and full pipeline QA. Component evidence uses real SQLite, filesystem
objects, native Git and socket-free HTTP delivery; only the matching-profile
resource facts use the existing test contract fake. Earlier Cloud/launcher and
other broader rewrite handoffs remain in their owning reports; this report
closes the ten native CLI QA assignments, not those separate slices.

Operator onboarding remains pending Korey's decision. No substitute provisioning
route was built. The installed discovery CLI differs from this branch; the
repository registry and tested info examples are authoritative. Overwing reports
source lag. No local Git identity/worktree overrides appeared. Agent scratch is
removed after checks join, and the shared Go cache is retained.

STATUS: COMPLETE
