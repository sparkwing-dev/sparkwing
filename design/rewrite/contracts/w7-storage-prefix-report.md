# W7 storage URL prefix

<!-- design:begin -->

Invariant: every physical object read, write, copy, signed URL, listing and
deletion stays under the configured storage root.

The controller now preserves the parsed S3 URL prefix at backend construction.
`NewS3(client, bucket, prefix)` requires that value; `PhysicalKey` applies it
at the physical boundary for CAS, staging, manifests, logs and CloudFront.
Prefix validation refuses traversal components, backslashes and control bytes.
Filesystem storage already confines every operation through its configured
`os.Root`; its acceptance fixtures now use a nested data directory.

The Cloud example uses `s3://sparkwing-cloud-data/controller` for data and
measurement. Documentation covers scoped object/list permissions, prefixed
log lifecycle rules and CloudFront's empty origin path. Embedded storage docs
and changelog match their source files.

## Evidence

All Go checks used `GOWORK=off GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W7/go-tmp` and
`go -C /home/bombuntu/.xwing/envs/1084/sparkwing`.

- Reproduced the failure before applying the mapping with
  `test -p 4 ./tests/acceptance -run '^TestStorage/storage-reference-edge-laundering/s3-manifest-records$' -count=1`:
  `PUT /bucket/teams/default/pending/member-a-0` escaped the configured prefix.
  The same command passes after the fix.
- `build ./...` and `vet ./...`: pass on the final production code.
- Full `internal/blob`, `pkg/storage/logseg`, `internal/doccheck` and Helm chart
  package tests: pass.
- Focused S3/CloudFront controller tests and filesystem/S3 controller startup
  tests: pass. Startup tests cover the actual storage URL parser and constructor.
- `test -p 4 ./internal/blob -run '^$' -fuzz '^FuzzS3StoragePrefix$' -fuzztime=10s -parallel=2`:
  pass, 87,128 executions. Seeds include traversal, spaces, Unicode, literal
  percent escapes, empty roots and malformed object keys. Log deletion also
  generates nested prefixes and checks every listing/deletion against them.
- Source-bundle upload, record creation and verified download properties pass
  through filesystem and S3 signed transfers. Record authority is a fixture;
  the existing manifest-record cases use real SQLite transactions.
- Embedded doc/changelog equality and scrub tests: pass.
- `test -p 4 ./tests/acceptance -run '^TestStorage($|PrefixSourceBundles)' -count=1`: pass.
  All storage cells pass with nested filesystem/S3 roots: CAS/integrity, real
  upload/receipt and manifest-record transactions, artifacts, source bundles,
  logs, reachability/retention, memo, dependency caches and publication.
  Runtime: 151.319 seconds.
- `commentcheck -base HEAD .`: clean across all 24 task Go files.
  `commentcheck -base main .` reports the existing untagged comment at
  `pkg/store/store.go:107`; that line is unchanged from HEAD.
- `git diff --check`: pass.

The first broader storage run exposed an existing stale `record_refused`
expectation: it required an authorization refusal to become a memo miss.
The invariant is that authority errors remain errors. Its assertion now expects
`ErrDigestNotReadable`; production memo behavior is unchanged. No archived tests
were ported and no acceptance cells were unskipped.

## Contract changes made

C1 records the required S3 prefix argument, shared physical mapping and relative
persisted log keys. No other contract change is needed. No open design question
remains for this slice. Lead-owned handoffs and amendments were untouched.

## Shared files and size

Shared edits: controller `data.go`, `data_test.go`, `main.go`; CloudFront adapter
and tests; `pkg/storage/logseg/s3_test.go`; acceptance storage fixtures, the memo
assertion, source-bundle property and mechanical S3 constructor callers; Cloud
chart example/README/render test; C1; storage docs and embedded mirror; changelog
and embedded mirror. Other implementation edits stay in `internal/blob`.

This slice replaces 21 production Go lines with 41: net **+20**, including
**+14** in W7 (29 added, 15 replaced). No production files or legacy services were added or
removed. W7 against main is **-6,599** (5,215 added / 11,814 deleted), **31 over**
the -6,630 budget. The inherited overage was 17; this fix adds the remaining 14
for explicit storage-root state, validation and one mapping across all S3
operations. Removing the validation would leave URL traversal ambiguity;
duplicating prefix joins would weaken the single construction boundary. This
small overage is submitted to the lead for review.

Friction: full controller-secret and docs-web tests require sockets denied by
the sandbox; focused tests avoid that dependency. No live S3/EKS deploy or
Sparkwing pipeline was run. Repository Git metadata was not written. The shared
Go cache is preserved; worker scratch is removed after checks.

<!-- design:end -->

STATUS: COMPLETE
