# W7 storage handoff

W7 remains incomplete. This session replaces artifact capture/staging and memo
materialization with the blob contracts, removes the remaining gitcache source
client, and keeps the repository build passing. Production host bindings,
transactional storage and native acceptance remain open.

## Implemented in session 3

- `internal/artifacts` uses injected `blob.Store` and `blob.RecordStore`.
  Capture uploads verified files and a deterministic manifest, then commits
  every member. An existence answer cannot replace an uploader receipt.
  Stage requires an authorized record, compares manifest bytes with all
  authorized members, checks size/digest/EOF, and replaces files only after
  verification. Corrupt downloads preserve the prior file.
- Shared `blob.Manifest` and `DecodeManifest` keep artifact and memo validation
  identical. The manifest includes file path, digest, size and mode. Workspace
  confinement, symlink handling and mode masking have component tests.
  Windows replacement handles a prior read-only file. Windows test compilation
  passes, but native Windows execution has not been run.
- `blob.MaterializeMemo` atomically copies authorized records through
  `RecordStore.CopyMemo`, then verifies output, manifest and members within the
  lookup budget. Missing follower output, missing bytes, corruption, refused
  edges and failed manifest copies become misses. Parent cancellation fails.
- The old host memo storage path no longer reads arbitrary source run outputs,
  copies a bare manifest digest, or treats a failed manifest write as success.
  It publishes output bytes and scoped memo records through blob contracts.
  Lookup precedes slot coordination so the store can resolve trusted default
  fallback. Slots coordinate cold executions with their legacy memo key field
  empty. Promoted holders recheck the authorized record. The user no-memo flag
  retains its bypass behavior.
- Host artifact publication re-resolves the authenticated binding after
  execution because retries advance the attempt. Old artifact-store injection
  was removed. A job declaring artifacts without a binding fails explicitly.
- Trigger and node source fetches use direct Git. Declared refs support branch,
  tag and SHA. Working-tree baseline acquisition uses the same credential
  resolution and a repository checkout without requiring `.sparkwing` or
  fetching submodules. The baseline-without-pipeline regression failed before
  the review repair and passes afterward.
- Removed the obsolete direct artifact transport and its discovery fallback.
  Snapshot validation and raw checkout restoration remain with the signed
  source-bundle client because that client still needs them.

Sessions 1 and 2 remain committed. They supplied signed byte transfers, edge and
scope predicates, filesystem/S3 physical backends, remote-cache reads, the
blob depcache backend, the cache-service/chart/package-proxy deletion, direct
SDK Git operations and the signed binary client. Those were not reimplemented.

## Deletions

Session 3 deletes eight complete files containing **2,137 lines**:

- `internal/artifacts/{capture_test.go,stage_test.go}`: replaced by tests at the
  new byte/record boundary, using real workspace files.
- `internal/bincache/{fetch_test.go,promptless_test.go}`: obsolete cache-server
  source tests. Direct-ref and baseline behavior have replacement tests.
- `internal/nodehost/memo_unix_test.go`: old protocol, cache-row and event
  integration assertions. Storage behavior is covered by the new component
  tests. Final lifecycle and generator assertions still need native T1/W3 ports.
- `internal/orchestrator/{direct_artifact.go,direct_artifact_test.go,
  supervisor_artifact_internal_test.go}`: the removed artifact transport.

The retained `internal/bincache/bincache.go` loses 412 lines of gitcache source
helpers. Retained host and cluster files also lose old source retries, source
selection, arbitrary memo reads and manifest-copy code. Across all three
sessions, 73 complete files containing 16,003 lines have been removed.

## Acceptance and test ports

**Native storage acceptance: 0 activated, 424 cells skipped.**
`acceptance.New` and `acceptance.Run` still panic. Catalog and port-inventory
checks pass, but do not prove a native target. No fake was labelled SQLite,
Postgres, S3, Windows runtime or placement evidence.

The missing-follower-output and failed-manifest-copy findings now have direct
contract tests in `blob.TestMemoMaterializationFailures` and
`nodehost.TestMemoMaterializationRunsOnRefusal`. They cover initial lookup,
materialization after a leader, and a promoted holder. They also cover missing
members, corrupt bytes, manifest/member disagreement, budget exhaustion and
refused starts. Namespace tests from earlier sessions remain.

Artifact tests replace the old store API fixtures with verified capture,
manifest commit refusal, missing/unauthorized/corrupt/short/long staging,
mutation during upload, directory matching, sorted entries, symlink escape,
file replacement and mode tests. Direct source ports cover branch/tag/SHA,
unsafe refs, baseline identity, baseline grafting and baselines without a
pipeline directory.

The native one-terminal-event assertion, generator replay, retry rehydration,
transactional laundering, quota, retention and all placement cells remain
open. The earlier physical upload retry/checksum ports still need W1's receipt
and quota transaction portions.

## Contracts and integration still needed

W7-local additions are `artifacts.Data`, record-based artifact edges,
`blob.Manifest`/`DecodeManifest`, and `blob.MaterializedMemo`/`MaterializeMemo`.
The stable artifact key is `KindManifest`, `Name=run ID`, and scope
`repo/ref/pipeline/producer job` with an empty scope key. Attempt/generation
remain in `Record.Source`. W1's memo copy must create the destination manifest
under this layout. These additions are documented in `internal/blob/README.md`
and asserted in the artifact contract test. Existing other-area interfaces
and schema were not changed.

Other-area contract changes and real integration points:

- **W1:** implement tenant `RecordStore`, read sets, atomic memo copy, live blob
  existence, upload ticket/reservation/receipt lifecycle, quota and retention.
  The upload lifecycle still lacks declared tenant methods. Do not adopt
  object existence as an authorized receipt.
- **W2:** replace `directdata` and cache grants with authenticated capability
  transport. Reauthorize uploads at commit, bind quota before signing, commit
  exact receipts and mount the filesystem signer or configure S3.
- **W3/W8:** final run/node inputs need per-attempt byte/record bindings. The
  old `nodehost.Config.Artifacts` callback now consumes the binding but the
  production orchestrator constructors do not yet provide it. Artifact jobs
  fail without it and memo runs cold. Bind against the current server attempt,
  including after retries. Complete store-owned memoized lifecycle and retry
  materialization through the final reconciler instead of old host methods.
- **W4:** finish `.Cache` and migrate the repository lint declaration. The old
  lint-cache HTTP API stays deleted.
- **T1:** implement the native driver and activate the complete target cells.

Remaining W7 cuts include runner/agent/launcher `gitcache` flags and options,
`WithGitcache`, dependency-proxy environment plumbing, old controller data
handlers and cache grants, and old storage clients still serving controller
or local-controller paths. The source-bundle and binary clients still use the
prior `directdata` transport. Logs/publication also need final runtime binding.
No product design decision or approval question is pending.

## Verification

Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and `GOCACHE`,
`GOTMPDIR`, `TMPDIR` beneath `/tmp/sw-astra-W7`. Each command used at most `-p 2`.

Passed:

- Whole-repository `go build -p 2 ./...`.
- Vet on `internal/blob/...`, `internal/artifacts`, `internal/bincache`,
  `internal/nodehost`, `internal/orchestrator`, `internal/cluster`.
- Full `internal/blob/...` and `internal/artifacts` tests.
- Focused nodehost tests matching `TestArtifacts|TestMemoMaterialization`.
- Direct-ref and baseline bincache tests, including
  `TestAdoptWorkspaceBaseline*`; source URL orchestrator tests; cluster test
  package compilation.
- Windows amd64 artifact test cross-compilation, without native execution.
- Acceptance catalog/documentation/port inventory, with all storage cells
  explicitly skipped; source/embedded docs equality; `git diff --check`.

Review found and fixed default-fallback lookup ordering, manifest/member
agreement, read-only replacement on Windows, cold-miss coalescing, and baseline
acquisition wrongly requiring a pipeline directory.

Friction and limitations:

- Socket-backed source/credential and runtime tests cannot bind listeners in
  the sandbox. Local Git fixtures and injected transports provide component
  evidence only. No real S3, Postgres, cluster or native Windows test ran.
- A legacy nodehost follower test failed while opening the old schema because
  `sparkwing_meta` is absent. Its storage assertion was replaced by the new
  contract test. Full nodehost/orchestrator suites are not claimed green.
- Checks run during concurrent edits saw transient compile/import failures.
  The final checks above were rerun after the repairs.
- The installed `sparkwing info --for-agent` was invoked once during bootstrap,
  before loading the swarm's no-binary rule. No candidate, daemon or pipeline
  command was run. No git metadata was written.
- Temporary files and caches under `/tmp/sw-astra-W7` are removed at handoff.

## Shared files touched in session 3

- `internal/cluster/{pipeline_source_test.go,runner_pool_cli.go,trigger_loop.go,
  trigger_loop_test.go}`: direct source callers and fixtures.
- `internal/nodehost/{scheduler.go,artifacts.go,memo.go,artifacts_test.go,
  memo_artifacts_test.go,memo_unix_test.go,events_test.go,podnode_test.go}`:
  storage binding, replaced storage consumers and retired API fixtures.
- `internal/orchestrator/{claim_exec.go,hosted_run.go,pod_node_host.go,
  run_node_remote.go,trigger_host.go,envelope_coverage_test.go,
  repo_admission_test.go,run_node_remote_cancel_test.go,source_url_test.go}`:
  minimal caller migration for removed source/artifact/memo APIs.
- The three deleted orchestrator artifact files listed above.
- `CHANGELOG.md` and generated `pkg/docs/changelog.md`: record the cuts.

STATUS: CONTINUE
