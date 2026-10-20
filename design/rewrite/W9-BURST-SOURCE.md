# Burst source bundle contract

Invariant: declared secret paths and excluded untracked file contents never enter the uploaded bundle, including its Git object history.

Invariant: no declared local secret value or registered encoding appears in a reachable bundle object or file path.

`projectconfig.Pipeline.SourceFiles` uses `source_files` in YAML and JSON. Each entry names one literal repository-relative file. An explicit declaration includes ignored files too. Missing files fail intake. Directories, traversal, Git metadata and Git pathspec syntax are rejected. Removing the field and the burst snapshot caller reverses this addition.

`captureBurstSnapshot(ctx, checkout, sourceFiles, secretFilePaths)` returns the existing private snapshot object. Its bundle contains the actual index's tracked paths with working-tree edits, staged additions and unstaged deletions, plus `source_files`. Secret file paths and their descendants are removed before staging or hashing, even if also declared in `source_files`. A parentless commit carries only selected content because shipping original history could expose excluded secret blobs. The source checkout's refs, objects and index remain unchanged.

`projectconfig.ValidateSourceFiles` is the shared configuration and capture path validator. `worktreeSnapshot.rejectSecrets(ctx, values)` scans reachable commit, tree and blob objects after intake resolves declared local secrets and before upload. It uses the shared masker's registered encodings and retains enough overlap to catch matches across read chunks. Its errors expose neither source paths nor secret values.

The requesting run path uploads `BundlePath` with `client.UploadSourceSnapshot` after checking Cloud connectivity and passing `rejectSecrets`. Both sealed execution SHA and pipeline source ref must equal snapshot SHA. It must not add original history through `includeRevision`. `source.PrepareCheckout` imports the authorized content-addressed bundle through the existing source upload contract. Closing the snapshot removes its private scratch.

Existing `pipeline trigger --working-tree` selection keeps its prior semantics. Burst uses the same capture and safety checks with an explicit path selection. No archive format or controller storage schema changes.

Tests exercise tracked edits, staged additions, deletions, declared ignored files, undeclared files, path rejection, secret object absence and real remote checkout materialization through a fake blob transport. `FuzzBurstBundleSecretExclusion` generates secret contents and paths. `FuzzBurstSnapshotRejectsSecretValues` checks raw values, Base64 and read-boundary matches. Existing snapshot tests cover private scratch, deterministic commits, index preservation, raw bytes and symlink safety.

Production addition is 216 net Go lines: 166 in source selection, scanning and original branch accessors, 30 in the shared snapshot implementation, 19 in shared path validation and one project field. These extend the existing bundler and masker instead of adding another source transport. Deleted production lines: 11, replaced inside the shared capture code.

## Source upload maintenance scope

The SQL scope guard reproduced an unreviewed cross-team read in `expireSourceUploadsTx`. Its only caller is `Operator.ExpireSourceUploads`, which checks the maintenance singleton lease in the same transaction before invoking it. The sweep must select expired uploads across teams. Every subsequent object deletion uses the selected team/ticket key, quota release receives that team, and the SQL deletion predicates on both team and ticket. `internal/store/tenant_sql_scope_guard_test.go` now records that existing operator sweep in `reviewedUnscopedSQL`. Production SQL and tenant access are unchanged.

Verified `go test -p 4 ./internal/store -run '^(TestTenantSQLScope_|TestSourceUploadQuotaExpiryAndUniqueAccounting$)' -count=1` passes (0.259s). The initial scope-guard run failed at the sweep's SELECT before the annotation. `git diff --check` passes for the guard and this report. This follow-up adds no production lines.
