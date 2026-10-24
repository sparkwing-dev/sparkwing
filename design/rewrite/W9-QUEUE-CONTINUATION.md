# W9 continuation from c5ae3a0a4

The earlier readiness, public doctor and daemon cleanup fixes remain implemented.
This continuation fixes the background queue's missing service binding and its
discarded log-finalization store wrapper. Native command verification remains
with the lead. The additional owner/eval and launcher assignments remain open.

## Implemented

- The embedded queue supplies its captured awarded bearer to the existing
  authenticated private client. A digest-only principal cannot authenticate
  process-service, storage or log HTTP calls. The elected controller owns the
  shared socket transport and closes its idle connections after workers join.
- Local queue construction now uses `localcontroller.WithClient` to bind process
  operations, byte/record storage and shared log publication. Owner adoption also
  retains each independently awarded node bearer in that client. Private clients
  close after process and log cleanup, including failed construction.
- `StoreHosting` preserves a constructor-supplied store decorator. Its previous
  unconditional tenant assignment discarded the wrapper that seals logs before
  finishing an award. The tenant remains the default for constructors that do
  not supply a decorator.
- Bearer availability follows the service lifetime through joined cleanup.
  Cancelled operation requests receive no bearer. Uncancelled cleanup can seal
  final logs after hosting cancellation, and no bearer is returned after `Host`
  joins. The controller still checks current award authority on each request.

The two invariants are: service credentials retain the captured award and end
with joined service cleanup; log finalization runs before the terminal award
transition. An independent review caught cancellation prematurely closing the
first implementation's bearer. The revised lifetime and real log regression
passed re-review.

## Evidence

- Whole-repository `go build ./...` passes. Focused vet passes for the CLI,
  runners, local controller, client, cluster and acceptance packages.
- Full short suites pass for runners, localcontroller, controller/client and
  cluster. Focused CLI construction, doctor and detached-entrypoint tests pass.
- `TestNativeStoreOwnerW8` passes all seven modes. Its embedded queue, award
  snapshot and local-controller modes failed before the store-wrapper fix.
- `TestLocalQueueBindsAuthenticatedStorageBeforeOpeningProcessTransport` reaches
  authenticated storage and preserves a storage-fence refusal. An overlay with
  the earlier constructor fails before any HTTP request with missing services.
- `TestEmbeddedOwnerSealsLogsDuringCancelledHostCleanup` uses real store awards,
  controller handlers, private clients, signed filesystem uploads and committed
  log reads. It verifies the final bytes and sealed manifest after cancellation,
  then verifies bearer retirement. No socket or daemon is used.
- The bearer lifetime property passes its owner/node seeds and a bounded fuzz
  run (8 executions). The owner, log-finalization and lifetime regressions pass
  under the race detector.
- Existing readiness-independent-of-consumer, public doctor sweep, peer intake
  and native storage/log HTTP regressions pass. Tagged CLI and installed-runner
  suites compile. Native socket/service assertions were not run.
- Commentcheck is clean across 2,337 files. `git diff --check` passes.

No archived test was newly ported. No native acceptance cell was marked passed.

## Contracts and real integration still open

The requests in [the previous handoff](W9-HANDOFF.md) remain. In particular,
C2/W3 must supply an owner/eval process binding that preserves its own owner fence
and source-job relation without requiring a positive node attempt. `NativeConfig`
has a separate evaluation binder, but no production adapter can meet that
contract. No node attempt or authority was fabricated.

W8 must supply the production Cloud network-enforcement probe and
reservation-scoped cleanup for `KubernetesBoundary`. `ProductionRuntime.Launcher`
remains unbound, so `TestInstalledRunnerRuntimeComposition/launch` remains an
expected native failure. Plan/eval hosting, canonical placement eligibility,
cache/read-set proofs, paged-log transport and the earlier here/source/publication
integration requests remain open. The previous report lists their interfaces.

The lead should rerun `TestReviewProductionExecutionBindings`,
`TestReviewPublicDoctorSweepsAndWarnsForPrunedRun`,
`TestLocalClaimantW8UsesPersistedPeerAuthority` and the installed-runner probe
with an isolated HOME/state and release-version stamp.

## Scope and size

The internal `localcontroller.StoreBinding` implementation gains a transient `Bearer`
callback. This is the only shared production edit. It passes existing award
bytes in memory, because reconstructing them from a digest is impossible. No
frozen wire, policy, schema or foreign contract declaration changed. Removing
the callback and queue client composition reverts this addition.

Other shared files are `tests/acceptance/local_node_authority_w8_test.go`,
`tests/acceptance/native_store_w8_test.go` and
`tests/acceptance/local_queue_logs_w9_test.go`. CLI edits are `local_host.go`,
`local_host_test.go` and `local_services.go`.

This continuation replaces 8 production lines and deletes no whole files:
+66 / -8, net +58 across production paths. W9-owned paths are +48 / -7,
net +41. W9 versus main is +16,876 / -23,797, net -6,921 production lines,
within the -2,900 budget. Counts exclude tests, acceptance and declarations.

Friction: sockets and platform service managers are unavailable in the sandbox.
No daemon, operational Sparkwing command, candidate install, pipeline, Git write
or release ran. Worker scratch is removed after checks join. `/tmp/sw-gocache`
is retained. Lead-owned handoff and amendment files are unchanged.

STATUS: CONTINUE
