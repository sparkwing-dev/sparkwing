# W9 local integrator handoff

The 17:15 integration checkpoint is reached. The production probes for local
foreground run, controller startup and schedules now reach concrete owners and
fail on sandbox socket syscalls. The persisted-peer claimant probe passes.
The lead must run the command probes natively. Native execution still has the
contract and owner gaps listed below.

Base: `444cab1ac`. No Git write, Sparkwing candidate execution, installer,
pipeline, deployment or release ran. The only Sparkwing executable invocation
was the required read-only `info --for-agent`.

## Implemented

- Verified local peers authenticate through a persisted default-team root. The
  shared issuer mints it once and ordinary delegation issues queue authority.
  Reopen retains its reference. Revocation ends descendant authentication,
  relation checks and further delegation without replacing the root. Peer
  intake and named/queued claim authorization use the shared relation checks.
- The CLI foreground binder constructs W8's `NativeServices` and W3's native
  host over authenticated private transport. It opens no state store. A shared
  award validator compares the award with persisted authority. Named local
  claims report available CPU. Captured environment loading, process delegation,
  lease renewal and cleanup use the existing owners.
- Seven local-only host endpoints supply private snapshots, node reads,
  transitions, authentication, relation checks and atomic run/unit completion.
  Mutations derive actors, fences and runner metadata from server facts. The
  ordinary transition endpoint accepts only run start. Typed policy refusal
  survives atomic completion. Holder observations and step reports work while
  draining cancellation. Adopted node bearers stay private and authorize node
  reports independently from the run owner.
- Local schedule registration transports explicit replace/upsert mode, selected
  declarations, project configuration, native environment bytes and proof/follow
  choice. Existing private snapshots retain environment values. A private
  per-schedule registration file associates their seal with the project and
  checkout. Pin/unpin, tick, local pause/override and deletion reach the elected
  service. Registration, evaluation and pin/unpin/delete share the existing
  schedule lock. Proofs use verified source acquisition under the captured
  controller home. Acquisition cleanup follows pin copying and propagates errors,
  including when a later proof fails.
- The controller probe now starts the real runtime and joins its workers. The
  local-run probe exercises foreground mode. API inventory, OpenAPI, schedule
  source docs and embedded mirrors are refreshed. Three acceptance fixtures now
  check the complete trigger owner reference, whose Name is empty, rather than
  treating the trigger unit ID as a controller singleton lease name.

## Invariants and evidence

Verified peer roots remain tenant scoped and revocation closes their ancestry.
Generated forged-selector tests check that invalid peer lookup never persists
an authority, and actual intake/claim authorization refuses foreign teams.

Private host operations remain within the authenticated run and its separately
awarded nodes. Real SQLite awards pass through the actual HTTP handlers and
client using a socket-free RoundTripper. Tests cover node start/step/result,
foreign digest/class/audience probes, hosted-route absence, cancellation drain,
atomic normal/policy finish and terminal-transition bypass refusal. The
foreign-authority fuzz property passed 3,485 executions.

Schedule selection must match the supplied project before host capture.
Registration tests verify replacement/upsert boundaries and preservation after
proof failure. Real SQLite HTTP tests use a fake compiler and snapshot owner to
exercise registration, pin/unpin, dry tick, pause, override and delete. A
channel-driven overlap test verifies that pin/unpin/delete cannot read stale
captured inputs during registration. Environment transport preserves arbitrary
native bytes. Its fuzz property passed 2,400 executions.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and at most -p 4. Go scratch stayed under
/tmp/sw-astra-W9.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for cmd/sparkwing, controller, controller/client, store, crons,
  runners, localhostwire and controllerwire.
- Full short client, controllerwire, clisurface and localcontroller suites pass.
- Focused CLI host/local-run/owner/schedule tests pass. Peer/authentication/
  authority store tests pass. Focused private-host and local-schedule service,
  HTTP and wire tests pass. The controller's Engine/LocalHost/LocalSchedule
  selection also passes.
- `tests/acceptance -run '^TestSurface|^TestEngine'` passes. No native cell was
  unskipped and no archived test was newly ported.
- API reference/OpenAPI/doc drift checks pass. The controller and client API
  snapshots are regenerated. Commentcheck is clean across
  2,274 files. `git diff --check` passes.
- Tagged `TestLocalClaimantW8UsesPersistedPeerAuthority` passes.
- Tagged local run and schedules install fail at `dial unix ... d.sock:
  connect: operation not permitted`. Controller startup reaches elected store
  reconciliation and workers, then fails at Unix listener setup and the version
  policy worker's DNS socket. Both are sandbox syscall refusals. Public doctor's
  real probe also remains blocked at its daemon socket.

Production probes for the lead:

```sh
GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache GOMAXPROCS=4 \
  go test -p 4 -tags rewrite_integration ./cmd/sparkwing ./internal/wingd/localcontroller \
  -run '^TestReview(ProductionExecutionBindings|PublicDoctorSweepsAndWarnsForPrunedRun)$|^TestLocalClaimantW8UsesPersistedPeerAuthority$' -count=1
```

Native test builds need the release-version stamp used by the signed runner
policy. The local-run probe reads `installedVersion()` instead of a fixture
version. Source builds with an unknown version cannot establish runner policy.

## Real integration points left open

1. **DESIGN: foreground reservation.** After intake, the background local queue
   can claim the trigger before the CLI's named claim. The sealed request has
   no foreground/detached discriminator. Excluding all local origins breaks
   detached runs. A new boolean alone can strand an intake when the CLI exits
   before claiming, and retries retain the immutable original request. The lead
   needs one persisted reservation/first-claim contract, including expiry, CLI
   loss and retry behavior. Reuse the existing here/claim-by machinery if its
   semantics fit. No new reservation policy or schema was invented here.
2. **W1/W2 terminal wire replay.** The first node report commits. Repeating it
   returns unauthenticated because store authentication does not populate
   `BindingState.Result` for `LiveResult`. The client retains terminal actor
   identity so it does not block the eventual shared fix. Successful terminal
   wire replay remains unproved.
3. **W1 scheduled sealed intake.** `Server.ScheduleLauncher()` exposes the
   existing shared cron launcher. That launcher still uses legacy intake and
   assumes a clone URL. W1 must consume `crons.ReadLocalRegistration(ctx, home,
   scheduleID)`, load its environment seal through `FileEnvironmentSnapshots`,
   and join the selected checkout/pin to the single sealed intake. No alternate
   scheduled intake was added.
4. **W3/W8 pin representation.** `crons.Proof` and the stored pin represent one
   executable. TypeScript acquisition returns a command and its dependency
   closure. The current representation cannot pin that closure or deliver it
   through scheduled intake. Following without proof remains expressible. This
   does not establish native TypeScript pinning.
5. **W3/W7/W8 native services.** Output/data bindings, process service relations,
   controller log projection and remaining owner policies retain W8's recorded
   gaps. The shared native constructors remain their owners. Native source,
   engine, admission, runtime and teardown evidence stays with the lead/T1.

The preceding W9 work and deletions remain in W9-LEGACY-CUT-FILES.md and
W9-CLIENT-CUT-FILES.md. A1-03/08/09/13-18/22/38/55 transports and replay/fence
components, A1-41 managed-home wiring, A1-43 engine notices, A1-51 generated
schemas and A1-52 release packaging remain implemented at their recorded
boundaries. This continuation adds the local A1-40/49/50 composition. Initial
here reason/environment retention, authorized bundle/publication, debug resume,
remote cache/native hosting and native platform acceptance retain their named
owner integration dependencies. No prior native claim is upgraded to passed.

## Shared files and mechanisms

Foreign production edits are limited to:

- `pkg/store/authentication_contract.go`, `authority_binding.go` and
  `authority_issue.go` for persisted peer authority and shared relation checks.
- `internal/capability/routes.go` for the local route policies.
- `pkg/controller/local_http.go`, `engine_intake.go`, `server.go`, `crons.go`,
  `schedule_routes.go`, plus new `private_host.go` and `schedules_local.go` for
  local handlers, service attachment and trigger lease identity.
- `internal/runners/store_host.go` exports the existing award validator so the
  CLI and store-host composition use one rule.
- `internal/crons/arm.go` and new `local.go` reuse the schedule service and lock.
- New `internal/localhostwire/private_host.go` carries the store-shaped private
  DTOs because store already imports controllerwire and adding them there would
  introduce an import cycle.

Shared tests changed in controller route expectations and the three acceptance
fixtures named above. Generated controller/client `.apidiff` snapshots also
changed. All cmd/sparkwing, controller/client and controllerwire
edits are in W9's area. Only W9's c3 contract was amended. Lead-owned handoffs
and amendments were read and left untouched.

Mechanisms added are the persisted peer bootstrap through the shared mint
helper, authenticated private transport/finish adapter, and schedule registration
association through existing private snapshot storage. Each replaces a named
missing boundary. No dependency, environment variable, background worker,
compatibility path or separate intake was added. Independent review accepted
these mechanisms and the reuse of the existing schedule lock. It found the
foreground reservation and terminal replay gaps above.

No whole production file was deleted in this continuation. The old foreground
binding refusal, unbound schedule callbacks and duplicate local registration
struct are removed. With the lead's production exclusions, this session adds
1,375 and removes 58 Go lines across all areas, net +1,317. W9-owned paths add
599 and remove 23, net +576. W9 versus main is +16,485 / -23,764, net -7,279,
within its -2,900 budget. These counts include untracked source files and exclude
tests, testdata, acceptance and contract declaration paths.

Friction: native sockets are forbidden, Overwing reports an outdated installed
binary, and shared Go cache entries vanished during one check before a rebuild
succeeded. The cache was retained. Worker scratch is removed after all checks
and workers join. Shared Git config has no local identity or core.worktree
override.

STATUS: COMPLETE
