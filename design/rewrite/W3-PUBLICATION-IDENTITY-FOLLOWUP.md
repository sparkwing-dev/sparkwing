# W3 publication and source identity follow-up

All four findings in W3-review-1792291528-final.md are implemented. The latest
lead native report contains no FAIL for the previous committed state. This diff
still needs the lead's commit, review and post-session native run.

## Implementation and invariants

- Executed committed bytes come from the sealed object IDs. `procenv.Git`
  forces `GIT_NO_REPLACE_OBJECTS=1`. Source Git also sets
  `core.useReplaceRefs=false`. Selectors resolve in the original repository.
  Working-tree selection reads the captured commit's configuration through
  acquisition, without copying ref namespaces. Inspection fetches resolved IDs
  and tags only when checkout policy requests them. Acquisition reads HEAD
  after checkout instead of assigning the requested revision to its result.
- Withheld secret contents cannot pass the source publisher's known-value
  policy. `UploadSourceSnapshot` requires a `source.Publication` containing
  the prepared process base, masker and a nonnil secret classification.
  `OpenPublication` copies the final bundle, scans every included blob, and
  returns that private copy for hashing and transfer. Trigger supplies its held
  configured values. Burst supplies those plus resolved secret-provider values.
  Unchanged blobs and bundled history receive the same scan. The existing
  eight-byte minimum, encoded patterns and PEM-armor handling remain. Errors
  disclose neither file paths nor secret contents.
- Default-branch lookup receives the prepared masker. HTTPS_PROXY credentials
  remain supported and their components register on that masker.
- Complete recovery commands receive grammar validation across string
  construction. Doccheck reconstructs nested constant concatenations and marks
  unknown operands as boundaries. It validates complete lines within those
  strings, including constant expressions without a final newline. Incomplete
  dynamic fragments and explicit data contexts remain distinguishable. The
  prose fixture now ends its complete declarative sentence with a period.

These are the structural fixes authorized by the Integration phase assignment.
No caller-specific secret guard or alternate reference resolver remains.
The current source-selection continuation records the contract amendments.
Lead-owned amendments and handoffs are unchanged. Earlier W3 assignments remain
covered by their committed continuations. No implementation stub blocks this
slice and no new cross-area contract decision is requested.

## Deletion and migration

Deleted the 19-line wildcard reference importer, the 87-line caller-specific
changed-blob scanner, the two-line LocalReplica field/comment and the unused
30-line SourceData/UploadSourceBundle surface: 138 production lines. The new
shared scanner replaces the caller-specific scanner. No compatibility alias
remains. The old uploader had only test callers. Its 123-line test file is
removed. Its mismatched-response and foreign-owner cases are ported to
TestBinaryRecordUploadRefusesMismatchedResponses and
TestBinaryRecordUploadRejectsAnotherOwner because the same file-record helper
still serves binary publication. TestBinaryRecordUpload already covers exact
receipts and failed transfers. The storage-prefix test retains its filesystem/S3
property through signed byte and record interfaces. Existing client/controller
upload tests cover the live publisher. No archived test port was needed.

All changed production Go files total +261/-206, net +55. W3-owned production
against main is +12,747/-26,257, net -13,510, below the -2,300 budget. Counts
exclude tests, testdata, acceptance, contract scaffolding and sourcetest helpers.

Shared production files are cmd/sparkwing/{burst_native,burst_source,local_run,
trigger_native,trigger_prepare}.go, internal/procenv/build.go,
internal/doccheck/sourcehints.go, internal/bincache/source_bundle.go and
pkg/controller/client/source_upload.go. Source edits belong to W3.
Shared test changes are cmd/sparkwing/{burst_local_secret_unix,burst_source,
local_profile,snapshot_acquisition,trigger_worktree}_test.go,
internal/doccheck/sourcehints_test.go, internal/bincache/binary_records_test.go,
the removed internal/bincache/source_bundle_test.go, pkg/controller/{source_upload,
burst_acceptance}_test.go, pkg/controller/client/source_publication_test.go and
tests/acceptance/storage_prefix_test.go. They migrate the removed APIs and check
the current public boundaries. Shared prose changes are docs/{deployment,
local-execution}.md and the existing source-selection continuation. The previous
source-preparation report points here.

## Verification

All Go commands use the absolute worktree through `go -C`, with:

```text
GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache GOMAXPROCS=4
TMPDIR=/tmp/sw-astra-W3 GOTMPDIR=/tmp/sw-astra-W3/gotmp
```

Recreate the scratch directories before reproducing. Tests use at most -p 4.

Passed:

- `go build -p 4 ./...`.
- Vet for source, procenv, doccheck, controller/client, controller, CLI, bincache
  and tests/acceptance.
- Full source (16.091s), procenv (0.038s), controller/client (0.246s),
  doccheck (1.119s) and bincache (1.408s) suites.
- CLI source/secret/snapshot group (45.439s), selected with
  `-run '^(TestBurstSource|TestBurstSnapshot|TestBurstSecretScan|TestBurstPreparation|TestTriggerSnapshot|TestTriggerInspection|TestWorkingTreeTrigger|TestSnapshot|TestDefaultBranchLookup|TestProfileCacheBranch|TestLocalEntryPointsWithholdSigningMaterial|FuzzPreparationGit|FuzzBurstSnapshot)'`.
- CLI trigger preparation and diagnostic group (1.032s), selected with
  `-run '^(TestPrepareTrigger|TestPreparedTrigger|TestTriggerCheckouts|TestTriggerGitDiagnostic)'`.
- Controller `-run '^(TestSourceSnapshot|TestBurst)'` (11.986s), plus
  TestSourceSnapshotPublishesScannedCopyAfterInputChanges (0.166s).
- Existing TestStoragePrefixSourceBundles filesystem/S3 acceptance property and
  source-release HTTP regression group (0.143s). No acceptance skip changed and
  no native acceptance cell is newly claimed.
- Five-second fuzz runs with two workers: sealed replacement objects, 6
  executions; known-secret publication before network, 13 executions; command
  construction, 2,436 executions, including both newline-terminated and constant
  complete forms.
- Commentcheck against main, formatting and diff whitespace checks.

The replacement test first returned B's bytes under A's identity. The proxy
lookup first failed with the missing-masker error. The working-tree trigger
regression first made an upload request with SIGNING_MATERIAL in changed
ordinary.txt. It now fails before any HTTP request. Doccheck first accepted both
split newline commands and a complete constant concatenation without a newline.
All now pass. The publication fuzz covers literal/base64 values, chunk boundaries
and a secret present only in bundled history. Missing classification fails
closed. The mutation test changes the original file at the upload-ticket request
and verifies that the committed digest still identifies the scanned bytes.

Whole-repository tests, socket-dependent native tests, supported-platform QA and
final review remain with the lead. No Sparkwing candidate or service ran, and no
Git write ran. Local Git configuration has no fixture user/core.worktree entries.
Scratch is removed at session end and /tmp/sw-gocache is retained.

Friction: broad namespace preservation had been compensating for selection in a
replica. Resolving in the original repository removes that need. The unused
binary-cache uploader also had to go to leave one production source publisher.
Overwing matching/card reads worked despite its reported ten-commit lag.

STATUS: CONTINUE
