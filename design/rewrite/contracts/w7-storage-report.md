# W7 adversarial review fixes

<!-- design:begin -->

Review baseline: `deea866fb`. Findings 1-7 are closed. Worktree Git metadata was not written, and no
Sparkwing binary ran. The selected environment and checkout were reused.

## Finding status

1. **FIXED - DESIGN: manifest admission memory.** Invariant: stored object size
   cannot drive controller memory allocation independently of submitted metadata.
   Admission now calls `VerifyManifest` with the commit request's members. A
   streaming decoder compares each entry directly. Its representation budget
   derives from the submitted paths/digests and worst-case JSON escaping, rather
   than stored byte length. Whitespace runs outside strings use constant memory.
   Unknown fields, extra entries and metadata disagreements fail. Length and
   SHA-256 still cover every original byte through EOF; close must succeed.
   The existing transaction retains member existence, authority and mutation
   checks. Admitted-record reads stream entries too. No manifest byte cap,
   setting, schema table or compatibility shim was added.
2. **FIXED - memo authority errors.** Invariant: authorization, authentication and
   claim-fence failures never become successful cache absence. Wrapped
   `ErrDigestNotReadable`, forbidden, unauthenticated and lock-held errors survive
   copy and byte-read failures. Missing/corrupt bytes still miss. Corrected both
   the unit `refused_edge` assertion and the filesystem/S3 acceptance assertions
   that required the wrong policy. Updated the source README.
3. **FIXED - source object format.** Snapshot import explicitly initializes SHA-1.
   The conflicting SHA-256 receiving-host default is covered directly and in the
   ambient-Git-controls property. The fixture also pins its producer format.
4. **FIXED - log authority comments.** Documented identical final-payload replay,
   conflicting persisted seals, and completeness from persisted records. Missing
   or corrupt segment bytes fail reads. Removed the physical-manifest authority
   and abandonment-manifest claims.
5. **FIXED - storage responsibility comments.** Removed host-state/state-handle
   claims and the assertion that `SafeSegment` governs SDK job names. Existing
   hierarchical-job prefix and single-segment validation tests pass.
6. **FIXED - artifact directory comments.** Documented that outside directories
   and wildcard matches warn and skip, while literal outside files fail. Expanded
   the containment property to generate both literal `link` and wildcard matches,
   checking that skipped directories commit only an empty manifest and never
   publish outside bytes.
7. **FIXED - binary integrity comments.** Removed claims that remote binaries
   lack digest verification. Documented compiler-key freshness separately from
   remote EOF/digest verification and local compiler-keyed binary reuse.

## Reproduction and verification

The manifest regression failed before implementation: a 16 MiB whitespace prefix
caused **36,240,064 allocated bytes** for an empty member list. Its final tests
cover leading, trailing and interior padding, oversized field names/values and
arrays, malformed token spacing, fully escaped schema fields, exact member
agreement, close and cancellation failures. A generated property checks bounded
allocation for valid padded and malformed manifests. A real SQLite `CommitRecord`
with physical filesystem bytes reproduces the small-commit/large-object scenario.
Admitted reads and verification of padded manifests allocate under 2 MiB in the
regression; generated admission cases allocate under 512 KiB.

Memo unit and generated authority cases ran red first. The source-format test ran
red under `GIT_DEFAULT_HASH=sha256` before initialization was pinned. The two memo
acceptance cells also failed on their incorrect nil-error assertions before those
were corrected. Findings 4-7 describe incorrect prose rather than incorrect
runtime behavior; their behavioral checks already pass. No source-text tests or
manufactured runtime failures were added for those prose claims.

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W7/gocache`, and Go/test scratch under `/tmp/sw-astra-W7`.
Tests used `-p 4` or fewer workers.

- Whole repository `go build ./...`: pass.
- Full tests for `internal/blob/...`, artifacts, bincache and `pkg/storage/...`: pass.
- Focused store record/result/materialization tests and the real
  `TestMaterializedResultCopiesBytesThroughControllerAndHost`: pass.
- Controller `TestDataStorageAttachesVerifiedResultRecords`: pass.
- Six existing acceptance cells pass: filesystem/S3 manifests, manifest-records,
  and memo-materialization components. Manifest cells now exercise admission
  verification as well as admitted-member reads. No new cell or skip was added.
- Manifest metadata fuzzing: 1,936 executions; pass. Memo authority fuzzing:
  76,530 executions; pass. Existing source-control fuzz seeds pass.
- Vet across changed packages, store, controller and acceptance: pass.
- `internal/doccheck`: pass. `commentcheck -base main .`: clean, 2,253 files.
- `git diff --check`: pass.

No archived test port was required. Native sockets, PostgreSQL, deployments,
releases and whole-repository test suites remain lead-owned and were not run.

## Contract amendment and shared files

C1's local data contract now separates `VerifyBlobManifest` for admission from
`ReadBlobManifest` for admitted-record lookup. `ObjectBackend` has the matching
`VerifyManifest` operation. The existing read operation remains necessary for
record lookup metadata; it is not a compatibility alias. The amendment is recorded
in `c1-data.md`. No new foreign DESIGN request remains from this review.

The minimal shared W1 integration is `pkg/store/blob_record_runtime.go`: its
existing reader interface gains verification, and admission passes the submitted
members instead of reading another list. Shared tests are
`pkg/store/blob_records_runtime_test.go` and the new
`pkg/store/blob_manifest_stream_test.go`. Acceptance changes are confined to
`data_binding_test.go`, `storage_manifests_test.go`, and
`storage_materialization_test.go`. Lead-owned handoffs and amendments were not
edited. Source documentation is in `internal/blob/README.md` and GoDoc.

Mechanism added: a whitespace-normalizing streaming JSON reader and a
request-derived representation budget behind the admission verifier. Replacing
`ReadAll` with a plain JSON decoder is insufficient because that decoder can
buffer arbitrary padding or individual hostile values. Removing the verifier and
restoring the member-returning admission call undoes the structural change.

## Deletions and budget

Replaced the whole-object buffer and repeated decode, removed stale comments,
and kept every prior legacy service/cache/proxy/chart deletion intact. This wave
adds **232** and removes **58** production Go lines in W7: net **+174**.
W7 against main, including the untracked new production file, is **-6,701**
(**5,109 added / 11,810 deleted**), **71 lines within** the -6,630 budget.
No legacy file was restored.

## Existing handoffs and friction

The previous report's unrelated integrations remain assigned to their owners:
W1/W2's repository-bound remote-cache reader and verified default-ref selection;
W1's remaining selected-output projection and elected expired-ticket selector;
and W2/W3/W8/W9's authenticated data/log transports and production host bindings.
This review does not claim those broader rewrite integrations are complete.
Materialization remains covered by the real controller/host byte-copy proof.
There are no open questions for findings 1-7.

Friction: the first standards match saw no diff and returned only standing cards;
matching after the shared seam change discovered the remaining applicable cards.
No required capability failed. Test Git writes were confined to temporary source
fixtures. Local repository Git config has no injected user/core.worktree entries.
All scratch and caches under `/tmp/sw-astra-W7` are removed after checks. The
shared `/tmp/sw-gocache` was not used or deleted.

<!-- design:end -->

STATUS: COMPLETE
