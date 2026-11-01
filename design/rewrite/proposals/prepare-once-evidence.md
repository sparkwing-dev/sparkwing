# Same-run preparation evidence

## Behavior

The Cloud-awarded planning host captures the acquired checkout, separately pinned
pipeline checkout when present, and installed SDK command before SDK planning.
The server allows only Cloud-awarded planning owners to commit the immutable run
index. Native and self-hosted owners cannot publish prepared records.

Integrity comes from the verified blob SHA-256 digest plus the server's Cloud-only
write authority. This assumes the Cloud planning publisher is trusted to capture
the correct source and executable. The manifest hash covers the request seal,
source/checkout inputs, engine and SDK pin, target, install environment, tool
identities and publisher-declared snapshot fingerprint. Restore copies
`Inputs.Snapshot` from the manifest without recomputing it. It does not independently
prove that the executable was built from the checkout. Job hosts verify the complete
download before extraction and compare their sealed inputs before use.

Checkouts that cannot be captured safely, including absolute or escaping symlinks,
skip publication with one info log. Planning continues and jobs build fresh.
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
| Fresh acquisition + Go build + artifact publication | 981.306 ms |
| Download + verification + restored acquisition | 136.988 ms |

Installation count was one. The original repository was removed before restore,
and the restored executable produced its expected output. Earlier samples ranged
from 386.999 to 562.690 ms fresh and 122.925 to 183.789 ms restored. These are
individual samples, not percentiles. Source-transfer tests also revoke Git
credentials after publication and prove that restoration requests no second
credential or install.

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

The real controller HTTP path covers Cloud owner upload/commit, prepared-record
commit, native trigger publication refusal, pending job lookup and signed verified
download. Owner generations use their live unit fence, with a regression for a
changed generation.

The fast gate covers build, vet, changed-package lint, focused controller/store
and acceptance tests, source/launcher/blob/chart short suites, documentation and
archive confinement checks. The lead owns the full gate and Opus security review.
No deployment, cluster or AWS write was performed. The rollout proposal contains
local admission overlays for both GitOps environments; no infra change is needed
for this lane. Published replacement image digests belong to the release owner.

## Gate status

The lead's latest fast gate reported a CLI timeout. Its source and documentation
checks passed. The full CLI package run exceeded the 30-minute budget in
`TestLocalRunSelectionsReachPreparationSealingAndReplan`. Earlier isolated runs
also timed out during SDK installation through fresh `source.Acquire`, without
an assertion failure. This local source selection test does not enable prepared
artifact reuse. The CLI-budget lane owns that failure; the full gate remains
unresolved.

The focused source, store, controller HTTP, bootstrap and launcher admission CEL
suites pass on the continuation check. The source suite includes corrupt-byte
refusal and restoration after Git credential revocation. The timing table above
records that check's real Go fixture. No product or test change was needed.

Both rollout overlays now use readable YAML patches. Their parsed operations
and CEL expressions are unchanged. All four policy patches apply in memory to
the checked-in staging and production `kikd-gitops` manifests, including every
old-value assertion. No file in that repository was written.

The full gate and Opus review remain with the lead. The continuation check did
not repeat the known failing CLI suite.
