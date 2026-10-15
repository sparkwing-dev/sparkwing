# W8 legacy client cut and HTTP composition

Continuation from `211541180`. The cluster worker, warm-pool dispatcher,
Kubernetes run-host dispatcher and old pod executor were already deleted.
This cut removes their orphan client residue rather than rebuilding those paths.

Removed the legacy launch claim, synchronization, source credential and unused
pod heartbeat/start/report/child client APIs. Removed unused caller-selected
runner identity, capacity-report callbacks, hourly parking, jitter/advice and
shed-log helpers. The claim-input reader and log/output path helpers still have
consumers. They moved with their implementations unchanged into `claim_input.go` and `claim_paths.go`.

Removed the embedded launcher's `StoreController.SyncLauncher` and launcher flag.
Only the local queue uses that adapter. `TestStoreLauncherW8` now constructs
`NewLauncher` with its default engine client and passes claim/sync through real
controller handlers and store authorization. The launcher authority property,
remote native-owner and Cloud bootstrap components use those handlers too.
Their HTTP transport is in memory. The transactional sync reader, source,
process and deployment services remain fixtures.

The real exchange path exposed a request mismatch. `engineClient.Exchange` now
sends the empty JSON object required by the controller and published migration
guide. Its client test checks JSON content type and absence of binding fields.
The one-use authority tests still refuse replay. Native host binding reads the
real capability instead of asserting the transport is a fake implementation.

## Code volume

Production Go adds **67 lines** and deletes **469 lines**.
**Net production delta: -402 lines.** Tests, testdata, acceptance code and
contract declaration files are excluded. The two new Go files retain **60**
existing reader/helper lines. Whole removed production files are `claim.go`
(135 lines), `claim_backoff.go` (80) and `launcher.go` (81).

W8-owned directories shrink **17 lines**. Shared client cleanup shrinks
**385 lines**, which directory accounting credits to W9. W8 is **+4,204 against
main**, **6,504 over** its -2,300 budget. This cut does not meet the budget or
request a waiver. Further W8 implementation and reduction remain required.

## Verification and ports

- Whole-repository `go build -p 4 ./...` passes.
- Full runners, launcher, service, cluster, engine and local-controller
  composition suites pass.
- All `W8|LauncherCloud` acceptance components pass. The changed native-store
  modes include remote award, managed runner, renewal, cancellation and local
  controller. Cloud bootstrap and store-backed launcher components pass.
- Final engine-client, launcher wire, retry/one-use authority and retained
  error-classification tests pass. Controller/client and orchestrator tests
  compile after the API removals.
- The migrated authority property passed **142 generated executions**. Its
  invariant is that invalid, narrowed or revoked claimant authority cannot
  reach the sync reader or acquire work. The same bootstrap, native-owner,
  launcher and property seeds pass under the race detector.
- Focused vet, commentcheck against main, API snapshots, documentation mirror,
  changelog and whitespace checks pass.
- A broader legacy log/output/retry selection cannot run here. Its first
  signed-output test panics because binding `[::1]:0` is refused with
  `socket: operation not permitted`. No pass is claimed for that selection.

The port inventory records the removed identity, pacing and source-client tests
and their final dispositions. No native acceptance cell newly closes. No native
Windows, macOS, Kubernetes/DinD/network or real-repository QA was run.

Independent review found stale source docs and API snapshots. Those are fixed.
The generated localws snapshot and migration mirror also caught drift left by
the preceding consumer/dashboard cut. Source documentation and generated copies
are synchronized. No new production mechanism or compatibility alias is added.

## Decisions and remaining integration

07:40 items 5 and 6 remain implemented: `HTTP() LocalHTTP` and the final launcher
grammar. Item 10 now has evidence through the real client and HTTP handlers.
Observations carry operational unit kind/ID, holder, claim generation, Job
namespace/name/UID and node UID. Eval source generation remains separate.
**A1-38 remains open.** W1/W2 still need the transactional ancestry, live-award
and Job-instance checks behind `LauncherSyncReader.ReadLauncherSync`.
09:40 item 4 remains done. The four named dead declarations have no Go matches.

All W8 amendment rows and handoffs retain these remaining obligations:

- A1-08/09/18/19/26/27/36/38/39/51: the default launcher client and controller
  claim/sync/exchange/renewal handlers are exercised. Remote native-store,
  per-process source/log/storage services and actual native hello remain final
  bindings. Sync's transactional store reader remains unbound.
- A1-10/11/12/43/45/46/48/50/52/55: engine/version, sealed-source/environment,
  snapshot, settings, upgrade and here components remain implemented. CLI/source
  composition, native Windows packaging and placement QA remain open.
- A1-20/49: production enrollment/provisioning, failed-enrollment revocation and
  the runner factory's repository-list binding remain open.
- A1-40/41: the elected store, shared queue, maintenance, readiness, four holds,
  managed units and joined drain remain implemented. The production activity
  projection, peer-authorized HTTP adapter and CLI service binder remain open.
- A1-42/53: deployment Probe/Clear, enforced network isolation, shared scratch,
  teardown and full native evidence remain open.

**Contract change still needed, W3/W6:** sealed source must distinguish an
uploaded working-tree snapshot from a committed Git-bundle seed before remote
`accept_working_tree` consent can be implemented. No foreign contract was changed.

W1/W2 legacy tests still consume `Client.ClaimNode`, `ClaimNodeByID`,
`ClaimTrigger`, `ClaimSpecificTrigger`, `WithTriggerNodeRunner` and `ClaimInput`.
The shared-queue and named-claim store/controller ports must replace those tests
before their old implementation can be deleted. No replacement alias preserves
them. Their callers are outside W8's runtime.

## Shared files

Production edits outside W8 are under `pkg/controller/client`:
`client.go`, `engine_methods.go`, `livelogs.go`, `logs.go`, `pace.go`,
`poll_advice.go`, the deleted `claim.go`, `claim_backoff.go`, `launcher.go`, and
the retained code in `claim_input.go` and `claim_paths.go`.

Tests outside W8 are the three `tests/acceptance` files
`bootstrap_store_w8_test.go`, `native_store_w8_test.go`, `store_launcher_w8_test.go`,
and client `engine_amendments_test.go`, `pace_test.go`, plus the removed
`claim_backoff_test.go`, `runner_identity_test.go`, `source_credential_test.go`.
The port inventory, changelog, unreleased migration and their generated copies,
and the client/localws API snapshots are updated. Lead-owned handoffs and
amendment files are unchanged.

Friction: the previous transport fake hid the exchange mismatch, native socket
tests are sandbox-blocked, and Overwing reports a stale binary although standards
discovery succeeds. Git metadata and local identity configuration are unchanged.
No Sparkwing binary, pipeline, deployment, release or Git write ran. Worker
scratch is removed after checks. `/tmp/sw-gocache` is retained.

STATUS: CONTINUE
