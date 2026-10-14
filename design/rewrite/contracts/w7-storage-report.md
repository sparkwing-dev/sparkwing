# W7 storage handoff

W7 is incomplete. The byte client and cache-service deletion build and pass their
focused checks. The transactional storage, host integration and native acceptance
work cannot be completed against the other areas' stubs in this checkout.

## Implemented

- All functions declared in `internal/blob/contracts.go` and `log_contract.go`.
  Uploads check signed identity, size and SHA-256 before commit. Downloads require
  verified EOF. Presence lookups keep the caller's deadline and discard partial
  answers on timeout.
- Exact uploader and reference-edge predicates, encoded memo/depcache namespaces,
  server-derived write keys, binary provenance checks and tagged log validation.
- A signed HTTP transfer client that refuses redirects and carries only signed
  headers. It has no cache-service route fallback.
- Read-only remote-cache access through injected records and byte clients. It
  requires verified default-branch evidence, verifies returned bytes and permits
  subsequent reads only of accepted output, manifest, member and archive digests.
- A signed-data dependency-cache backend. It verifies an archive before publishing
  extracted files. Hosts bind it with `depcache.WithBackend`; local files remain the
  default when no backend is supplied.
- Local compilation and warmup no longer fetch or upload remote pipeline binaries.
- Cache-free runner/full charts and current release executable/image lists.

## Deleted

59 files contain 12,787 deleted lines, including 4,493 production Go lines.
The complete tracked diff removes 14,741 lines, including edits to retained files.
These counts include tests and docs and exclude the binary chart archive.

Removed `cmd/sparkwing-cache`, `internal/cache`, `pkg/storage/sparkwingcache`,
controller gitcache proxy handlers and routes, cache-service deletion calls,
cache Deployment/Service/PVC/NetworkPolicy templates, package-proxy chart wiring,
cache credentials in the charts, and the associated cache-service tests.
The runner chart archive was rebuilt from its source.

The hard cut is not complete. `internal/bincache` still contains gitcache source
and binary helpers used by the old host/runner implementations. `internal/artifacts`
still uses `storage.ArtifactStore`, and the old nodehost memo/artifact code and SDK
toolcache client remain. Replacing those callers belongs with W3/W4/W8 integration;
no new compatibility adapter was added here. General CLI/docs and Kubernetes QA
scripts still need their owners' removal of old cache settings and terminology.

## Acceptance and test ports

No native storage acceptance cell was activated. Running `TestStorage` reports
424 skipped cells. `acceptance.New` and `acceptance.Run` still panic. The catalog
suite passes, which is not runtime acceptance evidence.

`TestPresentIsBoundedAndAnswersAMissPastItsBudget` is ported at the byte-client
boundary as `TestHasManyBudget`, including rejection of a partial timed-out answer.
The remote memo test also proves an expired budget yields a miss. No worker-count
assertion or sleep is retained.

The remaining storage-hotfix ports are not complete. New component tests cover
same-run receipt laundering, all five admitted edge kinds, owner/publication
fences, manual-ref namespace separation, binary provenance, early close,
equal-size corruption, missing/truncated bytes, large presence requests, remote
cache provenance, and archive integrity. These do not replace the missing
transaction, quota, GC, memo-event, filesystem/S3 signing and native host tests.

## Contract changes and integration needed

Local W7 contract repairs are recorded in `internal/blob/contracts.go`:

- `Authority.Upload` carries a server-resolved owner/publication identity. The
  original authority had only an optional job attempt and could not fence owners.
  W2 must populate it after its live authority check.
- `RemoteCacheOptions.Records` supplies the record lookup dependency absent from
  the original constructor.
- `RecordLookup.VerifiedDefaultOnly` asks the record owner to resolve the ref from
  sealed default-branch evidence. W1 must implement this restriction, also checking
  C2's read scope, before the remote cache can be used with the real store.

Other owners' contract/runtime work remains:

- W1: implement `Tenant.LookupRecord`, `LookupMemo`, `LookupDepcache`, `CommitRecord`
  and `CopyMemo` in `result_contract.go`, plus transactional existence/read-set,
  receipts, unique-byte accounting, quota and retention. They remain panics here.
- W2: implement `capability.AuthorizeData`, the signed-data adapter and authenticated
  record transport. Object transfer alone does not establish existence or authority.
- W3/W4/W8: provide bound byte/record clients to artifact, memo and depcache host
  consumers. The old configs expose `storage.ArtifactStore`; the final host inputs
  do not declare that binding. Cut the old bincache/SDK callers with those hosts.
- Acceptance owner: implement the native driver before unskipping its target cells.

No changes were made to W1's store contracts or W2's capability contracts.
There is no new architecture choice awaiting approval. The missing runtime seams
above prevent a completed W7 claim.

## Verification

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `-p 4`, and
`GOCACHE`, `GOTMPDIR`, `TMPDIR` under `/tmp/sw-astra-W7`.

Passed:

- Root `go build -p 4 ./...`.
- `go vet -p 4` for blob (including depcache), depcache, orchestrator, cluster,
  controller, storage subpackages, CLI, charts and the pipeline jobs package.
- All tests in `internal/blob/...`, `internal/depcache`, `internal/releaseasset`
  and `charts`. The full chart suite took 49.637 seconds.
- Pipeline `TestPublishImageTagsGuardsPublishedDigests`.
- Acceptance catalog tests. Storage's 424 native cells stayed skipped.
- Both Helm renders, changed release-script shell syntax, and `git diff --check`.

Could not run:

- `pkg/storage/storeurl` tests stop at `httptest.NewServer`: socket operation not
  permitted. The CLI's `TestMaterialize_ColdCacheContactsNoController` stops for
  the same reason. Neither is reported as passing.
- Sparkwing pipelines, native acceptance, live S3/Postgres, Windows and deployment
  checks were not run. The brief reserves the pipelines/gates for the lead.

## Shared files touched

- `cmd/sparkwing/compile.go`, `spark.go`: remove local remote-binary transfers.
- `internal/orchestrator/pod_node_host.go`, `run_node_remote.go`: remove the old
  depcache grant injection and deleted HTTP cache-client dependency.
- `pkg/controller/server.go`, `handlers.go`, `deletion.go`, plus new
  `stream_deadline.go`: remove proxy routes and retain unrelated log deadlines.
- Five cache-service tests in `internal/cluster` were deleted with their server.
- `pkg/storage/doc.go`, `storage.go`, `storeurl/storeurl.go`: remove the HTTP
  cache backend and its documentation.
- Chart shared templates, values, READMEs, rendering tests and vendored archive.
- `internal/releaseasset/target.go` and its test, `.github/workflows/release.yaml`,
  `.sparkwing/jobs/{build,build_images,publish_image_tags_test}.go`, four release
  scripts under `bin`, and `DELIVERY.md`: remove the shipped cache component.

## Friction and cleanup

The contracts started without executable storage or acceptance implementations.
`runspec -> nodeproto -> depcache` prevents depcache from importing blob, so the
remote archive backend lives in `internal/blob/depcache` and uses depcache's
exported archive functions. This is the added package boundary; moving the cache
spec to a neutral package later would remove that need.

The sandbox prevents socket tests and git writes. No Sparkwing executable was run
and no git write was attempted. The repository-local `user.name` and
`core.worktree` overrides remain unset. `/tmp/sw-astra-W7` is removed at handoff.
