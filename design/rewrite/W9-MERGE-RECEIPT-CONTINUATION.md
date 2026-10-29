# W9 merge and receipt continuation

Base HEAD: `ae13798f2`, with the integration branch mid-merge. The lead's 01:40
native passes for local_controller, local_schedules and public doctor remain
the starting evidence. The SDK replacement pin policy is unchanged.

## Implemented and resolved

Both conflict files have resolved contents and no conflict markers:

- `internal/nodehost/process_authority_store_test.go` retains the integration
  branch's repository-scope property, including explicit empty scope and refusal
  of a widened or foreign scope. The full short nodehost suite passes.
- `internal/controller/browser_reads.go` uses the amended tenant-only backend
  constructor and retains W9's canonical run/job path values. Numeric debug,
  public reads and private storage/log tests pass.

No integration-deleted file was restored. No Git write was made because the
swarm brief forbids it. The index therefore still lists those two paths as
unmerged. The lead must stage their resolved contents and commit the merge.

The numeric receipt regression initially failed through the shipped shared
client because the server returned `node_id` instead of required `job_id`.
The builder now returns `controllerwire.Receipt` directly. Four duplicate DTOs
and their invocation/store response fields are deleted. The HTTP response and
its receipt hash now use the same shared projection that the client decodes.
The API guide, changelog and generated embedded copies describe this cut.
No production caller needed an adapter or compatibility alias.

Privacy invariant: receipt output excludes invocation metadata and local store
locations, and private or unclassified arguments never acquire an input hash.
The existing generated privacy test now also checks the serialized response
against the shared field boundary. Its seed cases pass under race, and fuzzing
completed 10,953 executions. The end-to-end numeric regression uses the actual
controller, shared client, credential issuance and SQLite without a listener.

## Assigned integration status

The merged implementations for W8 findings 6-9 and W2 finding 13 remain present:
owner/eval delegation has its own live fence and source checker, process and
storage bindings are required at construction, logs use shared segments and
record/object authority, service installation joins the old owner before its
successor, and peer intake uses the shared relation and committing authority.
Their focused component and persisted-HTTP regressions pass. The private JSONL
writer remains deleted. No old SDK pin rule was changed to complete execution.

W9-owned amendment actions A1-09, A1-16, A1-17 and A1-38 retain the implemented
launcher selection, private execution projections, expansion replay state and
immutable award transport. A1-41's lifecycle/service bindings pass. A1-43's
notice/upgrade adapters and A1-49's credential-file flow remain implemented.
A1-50 still has the concrete here/cache/publication and host integration seams
listed below. A1-51's shared wire/schema/docs checks pass. A1-52's release
packaging remains implemented with native Windows execution still unclaimed.
A1-53's complete native QA remains with the lead. A1-55's named here claim and
nonreplaying transport remain implemented, with native here hosting still open.
The SDK guide already contains decision 110's cross-host mask residual.
Lead-owned amendments and handoffs were read and left unchanged.

## Contract changes and real integrations still needed

1. **W8 production launch boundary.** The required full
   `TestInstalledRunnerRuntimeComposition` was run. Start, warm, fetch-source
   and run-node pass by reaching their production HTTP operations. Launch fails
   with `launcher runtime is unavailable`. `KubernetesBoundary.Probe` and
   `Clear` still have no production implementations. Supply enforced network
   evidence and reservation-scoped cleanup through the existing CloudBoundary
   contract, then compose ProductionRuntime.Launcher. No error-only factory,
   no-op boundary or manifest-derived enforcement claim was added.
2. **W2/W3 owner reference relation.** Owner/eval process credentials now exist,
   superseding the broad missing-credential item in older reports. Their latest
   pipeline reference still reaches `bindingPipelineReferenceTx`, which requires
   a current UnitNode claim. It cannot authorize an owner-source relation.
   The process source checker alone is not a replacement for persisted parent
   authority. UnitEval also lacks NodeReport in the frozen table, so its mask
   and cleanup routes remain denied. These need C2 amendments rather than an
   invented node attempt or a wider grant.
3. **W1/W2 deletion result.** The merge now supplies the real
   Tenant.DeleteAuthorizedRun implementation and TeamWriter conformance. The
   earlier report's missing-store-implementation statement is superseded.
   Its error-only result cannot report whether deletion removed a row or was a
   no-op. The HTTP handler still returns 204 while RunPruner requires the
   declared RunDeletion receipt. Exact replay needs an atomic deletion result
   and selector handling for an already absent run. No success result was guessed
   from a separate read. This is especially relevant now that the real writer
   can commit before the shared client rejects the response shape.
4. **W1/W2 remaining observations.** Run retry history still calls
   ListRunRetryTree/GetRun and reads removed args_json. Receipts now decode
   correctly but still estimate cost through buildCost, and their identity
   builder still uses legacy reporting inputs rather than all accepted seal
   hashes. C1 needs a tenant-scoped per-run billing/settlement projection for
   the receipt. CreditUsageByRun is a bounded team history without settlement
   state, so it cannot substitute for that read. These are not claimed complete.
5. **Other existing seams.** Canonical remote owner placement, Cloud plan/eval
   hosting, bootstrap checkout consumption, remote SDK discovery, cross-run
   record/read-set proofs, sealed scheduled intake, initial here reason and
   native here/cache/publication bindings remain in W9-HANDOFF.md and its
   linked continuation reports. Kubernetes debug attachment still needs an
   explicit persisted namespace/pod/container target. No attachment target is
   inferred from a holder string. Local execution still waits on Korey's SDK
   replacement decision and a native rerun after it.

These pieces stop at foreign contract boundaries under the swarm brief.
No foreign contract declaration, authority policy, timeout or SDK pin policy
was changed. This report does not claim full W9 completion.

## Evidence and size

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR=/tmp/sw-astra-W9/go, TMPDIR=/tmp/sw-astra-W9 and at most four package
workers:

- Whole-repository build before and after the receipt change.
- Focused vet for nodehost, receipt, controller and controller/client.
- Full short cluster, localcontroller, nodehost and controller/client suites.
- Persisted peer-intake, private owner/node transport, storage/log publication,
  numeric/debug selector and receipt selections.
- Service handover, drain acknowledgement, peer-intake, numeric receipt and
  privacy property seed cases under race.
- Surface/engine-amendment acceptance components. Native skeletons remain
  skipped, so this is no new native acceptance-cell claim.
- Full doccheck, embedded-doc/privacy/symbol checks, final mirror/changelog
  drift checks, commentcheck across 2,485 files and diff whitespace checks.

The socket-based TestSecretArgs_ControllerReceiptRedacts was attempted and
failed at httptest's TCP listener with operation not permitted. Socket-free
receipt privacy tests pass. The complete shipped-runner done test remains red
only for launch. No archived test was newly ported. Independent landing review,
platform service-manager checks and native CLI reruns remain with the lead.

Receipt production change relative to the pre-session merge index:
**+13/-58, net -45**. No whole production file was deleted this session.
The browser conflict adapts one constructor line while retaining both canonical
path values. W9-owned production versus main remains **+16,911/-23,797,
net -6,886**, within the -2,900 budget. Shared receipt code is counted separately.

Shared files touched: internal/nodehost/process_authority_store_test.go,
internal/orchestrator/receipt/receipt.go, internal/controller/browser_reads.go,
internal/controller/public_selectors_native_test.go,
internal/controller/receipts_snapshot_test.go and the generated pkg/docs copies.
Other edits are docs/api.md, CHANGELOG.md and this report. No API caller outside
these files required migration.

No Git write, pipeline, candidate install, operational Sparkwing run, deployment
or release ran. Git still uses the global identity with no local identity or
core.worktree override. Worker scratch is removed after joined checks, and
/tmp/sw-gocache is retained. Friction remains the foreign contracts above,
sandbox sockets, native platform evidence and Overwing's reported source lag.

STATUS: CONTINUE
