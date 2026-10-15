# W7 storage continuation

<!-- design:begin -->

## Implemented and deleted

The merge is complete at starting HEAD `8def7fae6`. The index has no conflicts.
W3's deleted hosted_run, trigger_host and pod_node_host files remain absent.
No Git write was made.

The signed byte, record and log paths retain W1/W1b's real tenant persistence.
W2's merged data adapter resolves live authority, checks data access, reads
committed metadata and charges downloads through the store. WithDataObjects
installs physical manifest verification for record and result transactions.
The authenticated log submission test still drives real filesystem bytes and
real tenant segment commits. No component acceptance cell was added.

Removed the unused bincache direct-fetch clients, credential exchange,
credential pipes/files, SSH-key sweeper, submodule fetcher and duplicate
checkout implementations. None had a production caller. Source acquisition
belongs to internal/source. Removed remote baseline adoption and its unused
environment transport. The CLI still uses WorkspaceBaseline.Resolve when
capturing snapshot metadata.

The signed source-bundle importer and binary-record storage remain. The importer's
local Git helpers live in source_import.go and its platform files. One environment
filter now replaces the former two filters. The invariant is that inherited Git
routing, tracing and configuration controls cannot influence a verified bundle
import. FuzzSourceBundleImportIgnoresAmbientGitControls exercises real bundle
imports with adversarial control variables and checks that the redirected
filesystem remains untouched. Existing exact-byte, cancellation, symlink,
receipt and record tests remain active. Baseline validation cases now exercise
Resolve directly.

The earlier cache command/service, gitcache proxy, package proxy, cache chart,
raw S3 output/log backends and run-directory log fallback remain deleted.
Historical signed release assets remain intact.

## Production line delta

W7 has **-6,866 net production Go lines against main**. The working tree has
8,773 lines versus 15,639 on main, using the lead's W7 prefixes and exclusions.
That is **236 lines below the -6,630 budget**. This session removes another
**1,744 net production lines** relative to starting HEAD.

Cumulatively, 51 baseline production files totaling 9,817 lines are absent.
This session removes 12 former production paths totaling 1,652 lines, including
the two platform helper files that survive under source_import names. It also
shrinks workspace_baseline.go by 182 lines. The new and moved helper files total
90 lines. The full deletion manifest is w7-storage-pruned-files.txt.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR under the W7 scratch directory.
Final commands also set TMPDIR there. Tests use -p 4 or less.

| Check | Result |
| --- | --- |
| Whole-repository go build ./... after final production edits | Pass |
| bincache, source, blob/..., artifacts, depcache, depcachekey, pkg/storage/... | Pass |
| Bundle-import fuzzing after helper consolidation, two workers | Pass, 32 executions |
| Full bincache tests after helper consolidation, including fuzz seeds | Pass |
| TestMaterializedResultCopiesBytesThroughControllerAndHost | Pass |
| Controller TestDataStorage*, TestLogSubmission* and receipt privacy fuzz seeds | Pass |
| Vet on changed storage packages and controller | Pass |
| Existing TestStorage acceptance suite | Pass, 70.348 s, 70 passing events, 424 skips, zero failures |
| commentcheck -base main . | Clean across 2,186 files |
| internal/doccheck and git diff --check | Pass |
| Repository Git configuration | Expected global identity. No core.worktree override |

Passing storage checks retain real SQLite reservations, receipt settlement,
manifest/member records, materialization, reachability collection, tagged logs,
signed filesystem/S3 byte transfers and archive restores. S3 uses an injected
object service. Native platform, placement, PostgreSQL, deployment and release
proofs remain with their owners. No new historical acceptance port was added.
The retired source implementation's tests leave with it. Existing source tests
cover sealed commits, current credentials, failed-checkout cleanup, authorized
submodules, history/tags and entrypoint containment.

## Contracts and remaining integration

A1-29, A1-30, A1-31 and A1-33 remain implemented. Consumers of A1-24, A1-32,
A1-34, A1-35, A1-36 and A1-37 use the real store or the declared foreign
interfaces. A1-43's historical assets remain intact. A1-50 production host
composition and A1-53 native evidence remain with their assigned owners.
Lead-owned handoffs and amendments were not edited. No foreign contract changed.

**W1/W2 receipt projection still needs a contract addition.** NodeRow.OutputRecord
persists the selected record, including a materialized result without an attempt.
RunSnapshot still has no tenant-scoped digest projection for that selected
record. It must resolve the digest within the reporting snapshot under runs.read,
refusing foreign tenant/result selectors. Then remove OutputRef, its report/finish
API, validation and node_outputs/data_objects queries together. LookupRecord
requires data authority and would change this boundary. Those remaining legacy
metadata APIs belong to W1. The physical output backends are already removed.

**W2 log transport adapters remain absent.** Hosts consume blob.LogTransport and
logseg.SegmentRecorder. The authenticated signing and commit routes and physical
verification are implemented. W2 must provide the authenticated client bindings
and uncertain-upload verification route using W7's UploadVerifier. No credential
or authorization substitute was added here.

**W3/W8/W9 production host construction remains external.** Supply fresh storage
and log bindings per attempt and owner generation, including remote-cache opt-in
and source-bundle intake. Existing interfaces remain the consumption seam.
Native placement evidence is still required.

**W1 maintenance inputs remain external.** Seal log retention at intake and provide
a fenced selector for expired reservation staging cleanup. Existing physical
backends and leased retention contracts remain available.

## Shared files, assumptions and friction

The only shared edits this session are pkg/controller/trigger_test.go and
pkg/controller/retry_test.go. Their legacy fixtures now contain literal input
keys instead of depending on the removed bincache environment constants.
No production caller migration was needed for the removed source-fetch APIs.
Storage source documentation was updated in internal/blob/README.md.

No new mechanism, setting, environment variable or retry policy was added.
The filter consolidation preserves the existing local-import policy.
Independent review, staging and commits remain with the lead.

The prompt's conflict list was stale because the lead had already completed
that merge. No unresolved conflict or deleted hosting file was restored.
The remaining foreign seams above prevent an end-to-end completion claim.
W7 is now within its production budget.

Scratch /tmp/sw-astra-W7 is removed at exit. Shared /tmp/sw-gocache is retained.

<!-- design:end -->

STATUS: CONTINUE
