# W3 artifact publication continuation

<!-- design:begin -->

W3 remains incomplete. Artifact capture and the shared artifact executor layer
are implemented. The earlier [amendment audit](w3-amendment-handoff.md),
[environment report](w3-snapshot-continuation.md) and
[required-read report](w3-artifact-continuation.md) retain the remaining work.
The worktree began clean with those sessions committed. No merge repair or
completed implementation was repeated.

## Implementation

The invariant is that a manifest references only bytes uploaded under the
current awarded attempt, and capture never reads outside the workspace.

ArtifactRecords.Publish uploads selected files, checks their receipts, uploads
the manifest and commits its member rows through the declared blob interfaces.
The final result receives a key only after successful record commit. Actual
authority and reference edges remain transaction checks in the byte/record
implementations. No digest, receipt or client struct grants authority by itself.

Rooted file opens enforce workspace confinement during capture. Internal links
retain their declared paths. Wildcard outside matches warn and skip. Literal
outside files fail. Hashing and transfer use one open file. Put rechecks the
expected size and digest, so changed bytes fail publication. Modes retain only
permission bits. Empty captures still upload and commit an empty manifest.

The deterministic manifest contains a sorted files array with path, digest,
size and mode fields. Transactional member rows remain the authority for staging.
No size cap, cache age, environment variable or compatibility path was added.

ArtifactRecords.Prepare stages consumes in order before SDK startup. It
publishes once after successful execution, outside application retries, using
the last committed attempt's binding. Failed execution publishes nothing.
Failed upload or record commit fails the job. A refused terminal transaction
remains an error. Producer is the explicit persisted-record lookup seam.

JobUpload replaces OutputUpload because output and artifact publication need
the same attempt, authenticated scope, receipt and commit checks. The output
acceptance fixture was migrated. No alias remains. The shared check also
requires the record's repo to match the sealed repository when present.

## Scope and open integrations

The local C3 additions are ArtifactRecords.Bind, Producer, Warn, Publish and
Prepare, plus the common JobUpload name. No other area's contract changed.
The existing glob matcher was renamed to artifacts.Matches and used by both
capture paths because copying it would create two matching policies. W7 must
retain that pure helper when cutting its old ArtifactStore implementation.

Real integration still needs W1/W9 terminal output/manifest references and
complete restricted execution snapshots, W2 upload binding and read authority,
W7 verified byte/record clients, and W8/W9 native placement composition. The
earlier owner-policy/approval, cross-process mask, selected-engine host handoff
and W6 Git/environment-digest contract gaps remain. No new cross-area contract
change is requested.

Memo/depcache layers, read-only remote-cache composition, native run/trigger/pod
assembly and the legacy run-host deletion remain W3 work. No old runtime was
deleted here: **0 legacy lines**. No additional parked-test port is complete.
The tests carry legacy artifact capture behaviors but do not close the parked
memo/materialization rows. No new product question needs a decision.

Shared production edit: internal/artifacts/capture.go only, for the matcher
rename and its existing caller. Shared test edits: internal/acceptance/host_artifact.go,
host_artifact_publish.go, host_executor.go and host_output.go, plus
tests/acceptance/skeleton_test.go and artifact_publish_test.go. The staging
fixture now calls the actual shared layer. C3, the port inventory and this
report are the shared prose edits. No production caller outside W3 needed an
API migration.

No public docs impact is claimed because native commands still use the old
assembly. The C3 source contract and acceptance checks describe the new library
behavior. Public CLI/reference generation remains with the native cutover.

## Verification

Every Go command uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, and
GOCACHE/GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W3/. Tests use at most -p 4.

New active cells beneath TestStorage/storage-cas-integrity:

- hermetic-artifact-publication
- hermetic-executor-publication

The first covers real-file capture, round-trip staging, glob overlap, modes,
internal/broken/outside symlinks, empty manifests, bound storage, cancellation,
changed uploads, bad receipts and refused/inconsistent record commits. The
second covers three application attempts, one publication under attempt 3,
failed work, publication failure and a refused terminal result.
The confinement checks inspect all transferred bytes, including refused uploads.
A link swapped at the transfer boundary still uploads its already-open file.

FuzzArtifactCaptureConfinement generates nested relative/absolute symlink
chains and proves outside bytes never enter uploads. FuzzArtifactUploadBinding
generates mismatched teams, runs, attempts, generations, principals, capability
references and record scopes. Mismatches fail before transfer.

| Check | Result |
| --- | --- |
| Whole-repository go build -p 4 ./... | Pass |
| Vet for nodehost/source/lease/orchestrator/artifacts and both acceptance packages | Pass |
| Full source/lease/artifacts and both acceptance package suites | Pass |
| Focused host suite from the environment continuation | Pass |
| All storage-cas-integrity hermetic cells and both new fuzz seed corpora under -race | Pass |
| Generated confinement fuzz run, two workers, 10 seconds | Pass, 2,939 executions |
| Generated binding fuzz run, two workers, 10 seconds | Pass, 5,790 executions |
| commentcheck -base HEAD, sleepcheck -base HEAD and git diff --check | Pass |

The initial publication tests failed against the absent API before implementation.
A manifest-ordering case also failed before explicit sorting was added. Directory
traversal order alone does not sort paths whose directory names prefix file names.
The final race run includes that case, round-trip staging and all publication
failure cases. No OutputUpload Go caller remains.

Rerun the new boundaries with the environment above and this worktree:

```text
go test -p 4 -race ./tests/acceptance -run '^TestStorage$/^storage-cas-integrity$|^FuzzArtifact' -count=1 -timeout 5m
go test -p 4 ./tests/acceptance -run '^$' -fuzz '^FuzzArtifactCaptureConfinement$' -fuzztime=10s -parallel=2
go test -p 4 ./tests/acceptance -run '^$' -fuzz '^FuzzArtifactUploadBinding$' -fuzztime=10s -parallel=2
```

The broad legacy host/orchestrator suites retain their earlier schema/socket
coverage gaps. They were not represented as passing or rerun without a relevant
change. The worktree has no unmerged paths. No local Git user/core.worktree
override appeared during these checks.

Native controller/API-socket, Windows/Darwin, Postgres, S3 and real-repository
QA cells remain skipped or unprovided. The lead retains those drivers,
independent review, pipelines and landing. No Sparkwing binary, git write or
external service ran. Scratch is removed at worker shutdown.

<!-- design:end -->
