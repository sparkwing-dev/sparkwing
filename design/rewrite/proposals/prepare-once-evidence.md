# Same-run preparation evidence

## Behavior

The planning host captures the acquired checkout, separately pinned pipeline
checkout when present, and installed SDK command before SDK planning. It commits
an immutable run index through authenticated data storage. The artifact manifest
hash binds the request seal, source/checkout inputs, engine and SDK pin, target,
install environment, tool identities and snapshot. Job hosts verify the complete
download before extraction and compare their sealed inputs before use.

Absent records/objects and different inputs build fresh. Integrity and authority
errors stop before installation or execution. Planning owner restarts can restore
with the engine still unselected; the engine resolver verifies the stored identity
against the restored pin. Other runs and teams have no prepared read edge.

The fetch init container, source-init adapter and prepared-checkout handoff are
removed. Run-node now owns acquisition after bootstrap exchange. There is no
additional service, variable, configuration switch or cross-run cache.

## Timing

`TestAcquirePreparedRealGoBuildTiming` drives `source.Acquire` through a real
native Go build, publication, and restored execution. Its fake engine resolver
and signed-transfer fixture isolate the preparation path; Git, files, compiler,
SHA-256 verification and extraction are real. The compiler builds a tiny stdlib
fixture with a shared warm build cache. This is not a staging latency estimate.

Measured in the focused prepared suite:

| Path | Elapsed |
| --- | ---: |
| Fresh acquisition + Go build + artifact publication | 422.572 ms |
| Download + verification + restored acquisition | 129.342 ms |

Installation count was one. The original repository was removed before restore,
and the restored executable produced its expected output. Another sample was
386.999 ms / 122.925 ms. Source-transfer tests also revoke Git credentials after
publication and prove that restoration requests no second credential or install.

Reproduce with the installed Go/Node tools on PATH and an isolated HOME:

```sh
go test ./internal/source -run TestAcquirePreparedRealGoBuildTiming -v -count=1
```

The staging baseline in COLD-START.md measured roughly 23 s per preparation and
5 s per fetch. This change removes the second compile/install/fetch on a matching
artifact. No cluster run was performed; transfer/publication costs on staging
remain for the lead's later measurement.

## Verification and limits

Tests cover Go and TypeScript restoration, unknown TypeScript SDK selection,
separate pipeline/source revisions, planning restarts, missing and mismatched
inputs, corrupted bytes, unauthorized reads, immutable publication, canonical
sealed provenance across host partitions, and foreign run/team refusal.

The real controller HTTP path covers owner upload/commit, prepared-record commit,
pending job lookup and signed verified download. It exposed an owner-upload
fence check that required a job attempt; owner generations now use their live
unit fence, with a regression for a changed generation.

The fast gate covers build, vet, changed-package lint, focused controller/store
and acceptance tests, source/launcher/blob/chart short suites, documentation and
archive confinement checks. The lead owns the full gate and Opus security review.
No deployment, cluster or AWS write was performed. The rollout proposal contains
local admission overlays for both GitOps environments; no infra change is needed
for this lane. Published replacement image digests belong to the release owner.

## Gate follow-up

A follow-up timing sample was 562.690 ms fresh / 183.789 ms restored, with
one installation.

The lead's gate found six untagged preparation comments. They now carry the
required invariant tags. The lane-wide comment check, root build, source/store
vet and lint, and focused source/store/controller preparation tests pass.

The lead's full CLI package run exceeded its 30-minute budget while
`TestLocalRunSelectionsReachPreparationSealingAndReplan` was installing an SDK.
An isolated run of that test exceeded a five-minute budget in its second revision
case, also during SDK installation. Neither run reported an assertion failure
before timing out. The CLI package gate remains unresolved. This local source
selection test does not enable prepared artifact reuse.

## Continuation verification

The focused source, store, controller HTTP, bootstrap and launcher admission CEL
suites pass. The real Go fixture measured 499.840 ms fresh / 167.038 ms restored,
with one installation. The source suite includes corrupt-byte refusal and
restoration after Git credential revocation.

Both rollout overlays now use readable YAML patches. Their parsed operations
and CEL expressions are unchanged. All four policy patches apply in memory to
the checked-in staging and production `kikd-gitops` manifests, including every
old-value assertion. No file in that repository was written.

The isolated CLI selection test reproduced the timeout at 90 seconds in its
first revision case, after two SDK replacement warnings. Its stack was waiting
in `engine.Resolved.InstallSDK` through fresh `source.Acquire`. The separate
CLI-budget lane owns these repeated compilations and the package budget. No CLI
test or product workaround was added here. The full gate and Opus review remain
with the lead.
