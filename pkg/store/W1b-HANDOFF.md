# W1b store-data continuation

Session base: bd25618e9. The worktree started mid-merge with 16 unmerged paths
and conflict markers. All conflict contents are resolved. No Git write ran, so
the index still reports those 16 paths as unmerged. The lead must stage the
resolved files and deletions and finish the merge. Whole-repository build and
scoped test compilation pass with these contents.

## Implemented

The 12:15 materialization path was already implemented by earlier sessions.
The merge moved DataController from data_contract.go to data.go and dropped
its default persisted-authority resolver. data.go now retains that resolver.
The real-store proof passes again. It selects a memoized result, copies its
records, persists output/manifest selectors through ExecutionJob and host
consumption, deletes the source run/memo, and reads the exact output and artifact
bytes through production record authorization and filesystem signing. Missing
or corrupt output, manifest or member bytes produce executable misses.

Browser log reads now carry the authenticated opaque AuthorityRef into the
existing tagged-segment reader. They no longer derive an actor from the removed
Principal context. Log object lookup uses the merged s3Logs configuration.
No legacy NodeLog persistence or output URL reader was restored.

07:40 item 2 now has verified-event dispatch in payment_provider_settlement.go.
SettlePayment routes all seven non-checkout kinds through the existing
transaction-local financial owners. Mode checks, payer lookup, event replay,
provider-object identity, mutation and receipt share one locked transaction.
Setup and collection use committed provider correlations. Refunds and disputes
resolve a mode-scoped checkout or collection payer before touching legacy rows.
A closed dispute cannot reopen, lost disputes reverse only their verified
amount, partial refunds cannot reverse twice, and old setup-object replay cannot
replace a newer card. Payment-intent failure observations do not freeze their
amount, method or latest charge before a later verified success. The existing
refund obligation remains when a success arrives after its attempt failed.

Warning protection is monotonic. An initially non-actionable warning can
escalate, and later non-actionable events cannot remove that protection. The
previous owner stored only the initial actionable flag, allowing collection to
miss an escalated warning. The generated warning sequence property covers this.

Invariants tested: verified events cannot change a committed payer or mode,
refunded credit cannot exceed the original grant, and failure to commit an
event receipt cannot leave any financial mutation committed.

## Production integration still required

The payment dispatch is implemented against the local C1 persistence contract
now recorded in design/rewrite/contracts/c1-data.md. Its test fixture supplies
a schema fake for the missing tables. The current production schema cannot
settle these new callbacks. It fails before any financial mutation. No payment
readiness or native payment acceptance cell is claimed.

W1 must extend its final schema and trusted writers:

- payment_events must admit the seven new kinds, allow a null checkout for
  those events and a null team only for a warning without an associated payer,
  and add object_id, event_json and settlement_json. Keep the existing
  mode/event key, checkout foreign key and checkout requirements.
- payment_correlations must bind mode/provider ID to setup or collection,
  team, customer, currency and exact internal attempt/charge. C1 documents the
  columns, defaults, key, constraints and immutable association rules.
- Trusted billing-page completion must populate setup correlations from its
  committed reservation and confirmed provider ID. The collection adapter must
  populate collection correlations and bind the exact attempt's payment intent.
  Provider metadata and callback parameters cannot create associations.
- Retained legacy card fingerprints, warnings and collection rows still need
  mode isolation in the final schema/writer cutover. The new dispatch refuses a
  retained payment when it has no matching mode-scoped association. It does not
  make raw legacy rows into new provider authority.

This adds a local C1 schema contract. W1's implementation files and schema.sql
were not edited. W2's PaymentEvent contract was consumed unchanged. The new
correlation and object-receipt records are required because signed provider
metadata cannot select a payer and several event IDs can name one financial
object. No new public API, retry, cache, configuration switch or customer cap
was added. USD cents and integer limits are the existing credit ledger policy.

## Deletions and budget

Deleted Store.CommittedObject, Store.NodeOutputObject and their unused legacy
Upload model: 60 production lines across direct_upload.go and node_output.go.
The merged controller no longer has their callers. Remaining local output and
legacy claim-credit writers still have W1/W3 callers and were not recreated or
cut out from beneath them.

Store production delta against session HEAD is **+259 lines**, 320 added and
61 deleted, including the new untracked production file. This excludes tests
and contract declaration files. Combined W1+W1b store production against main is
**+3,616**, which is **7,866 over** the -4,250 budget. No budget waiver is
claimed. The approved non-checkout event dispatch adds 313 production lines
that the earlier checkout-only implementation did not contain. Its missing
schema integration and remaining legacy code prevent COMPLETE.

Removed one 24-line test of the deleted Principal-to-actor projection. Existing
committed-reader authority properties, final-store reads and live-tail fencing
remain and pass. No archived test-port identity is newly claimed.

09:40 item 1 remains closed for W1b: its nine harness proofs already live in
pkg/store tests. The remaining internal/acceptance/store_* files concern W1's
claims, plans, credentials, schedules, capacity, transitions and recovery. None
was moved again. No scenario catalog, Harness or boundary registry was extended.

## Files and merge resolution

Owned implementation: pkg/store/payment_provider_settlement.go,
payment_settlement.go, payment_warning_settlement.go, direct_upload.go and
node_output.go. New tests: payment_provider_settlement_test.go. Source contract
and documentation: design/rewrite/contracts/c1-data.md and pkg/store/STORE-DATA.md.
This report replaces the earlier continuation note.

Minimal shared changes beyond the incoming integration merge:

- pkg/controller/data.go preserves the persisted authority default.
- pkg/controller/browser_reads.go forwards Authorized.Principal.Authority and
  the current capability prefix to the existing segment reader.
- pkg/controller/log_segments.go uses s3Logs without restoring legacy sealing.
- pkg/controller/log_reads_test.go removes the deleted helper's test.
- pkg/controller/log_segments_internal_test.go removes EnableAuthFromStore.
- pkg/controller/log_upload_removed_test.go and output_upload_removed_test.go
  consume the current router signature.
- pkg/controller/prom_internal_test.go uses the existing runner-inventory fake
  instead of the removed in-memory runnerPresence mechanism.
- tests/acceptance/console_writes_fake_test.go selects the tenant's existing
  ReleaseCreditFreezes method.
- .apidiff/pkg_store.txt and .apidiff/pkg_controller.txt are regenerated.

The 16 conflict paths resolved in the working tree are .apidiff/pkg_controller.txt,
design/rewrite/contracts/t1-port-inventory.md, docs/migrations/_unreleased.md,
pkg/docs/mirror/migrations/_unreleased.md, and these pkg/controller files:
browser_reads.go, card_billing.go, credits.go, data_contract.go (deleted),
log_segments.go, log_segments_internal_test.go, node_output.go (deleted),
operator.go, output_objects_s3.go (deleted), prom_internal_test.go, server.go and
tenant_routes.go (deleted). Integration policy/route shapes win. W1b's tagged
log-reader behavior and persisted data-authority default are preserved. No
lead-owned amendment or handoffs.md was edited.

Earlier materialization edits remain in internal/controllerwire/engine.go,
pkg/controller/engine_snapshot.go, internal/nodehost/output_records.go,
owner_evaluation.go, owner_schedule.go, remote_contract.go, sdk_code.go and
sdk_replay.go, internal/blob/authority.go, keys.go, object_contract.go and
objects.go, pkg/storage/manifest.go, and the existing store materialization
owners and tests. Those W3/W7/W9 files were not edited again this session.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR beneath /tmp/sw-astra-W1b. Test
parallelism was at most four.

- Final whole-repository go build -p 4 ./... passes.
- Final scoped go vet passes for pkg/store, pkg/controller, internal/blob,
  internal/nodehost and tests/acceptance.
- Focused store selection passes in 62.232 seconds: payment, blob,
  materialization, log-stream and the moved W1b billing/presence/CAS/cache/
  reference-edge/retention proofs.
- The final nine TestProvider entry points pass in 1.464 seconds. They include
  seven outcome subtests, financial rollback/reopening, generated payer/refund/
  warning properties, setup ordering, mode-switch refusal and later success.
  A signed refund passes C2 authentication and public SettlePayment while
  forged metadata cannot change its payer or credit effect. Receipt/correlation
  persistence is still a schema fake.
- All 13 data-binding entry points pass in tests/acceptance (0.024 seconds).
  Controller data-ref, retired-writer, committed-log and sampler selections
  pass (0.247 seconds).
- Final commentcheck -base main . is clean across 2,142 Go files.
- Regenerated API snapshots match every existing snapshot. Whitespace checks
  pass. No conflict markers remain. Local Git configuration has no user identity
  or core.worktree override.

Initial vet/test compilation failures came from the removed auth/presence
helpers, router signature and moved credit-freeze method. Their minimal caller
migrations are listed above. The broader inherited W1 metering/legacy-schema
failures recorded by prior sessions were not reclassified as fixed. W1 still
owns its cloud-award metering and renewal/exhaustion join. SourceCoalesced still
needs a declared persisted cross-run read edge. Memoization satisfies 12:15.

No pipeline, Sparkwing binary, socket-dependent suite, native PostgreSQL,
provider network, service, deployment, release or Git write ran. The lead owns
independent review and landing. Worker scratch is deleted after reporting.
The shared build cache remains.

STATUS: CONTINUE
