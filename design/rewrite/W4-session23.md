# W4 amendment integration report

Continues `14d5cf690` and `W4-session22.md` in the supplied worktree.

All ten merge conflicts have resolved file contents. Amended contract shapes win.
The merge keeps W4's typed protocol, hello binding and secret preflight alongside
W3's newer stream masking, sink queues and masked state. No conflict markers
remain. The Git index still reports the ten paths as unmerged because the swarm
brief forbids Git writes. The lead must stage the resolutions and finish the merge.

## Amendment work

| Rows | Result |
| --- | --- |
| A1-01, A1-02, A1-05 | Mask now posts the shared request to `/node/v1/masks` and waits for an object acknowledgement. Annotation and summary writes use their declared routes, preserve location/step/markdown, and propagate canonical errors. Fake-host acceptance and registration share a lock. Colliding masks fail before registry mutation. Later initial/generated declarations use the same registry. |
| A1-03, A1-04 | Consumed the merged error decoder and record bound. W4's two former decoder test exclusions are removed. |
| A1-06 | Cancel accepts job with optional step or target request ID. Mixed and empty selectors fail. Job cancellation selects its steps and evals. Foreign bindings fail. Acknowledgement joins selected work while preserving each original reply. |
| A1-07 | Structural validation preserves omitted concurrency options and rejects explicit null/empty/zero. Go serializes absent scope/policy/cost as absent and its required capacity explicitly. Resolved consistency/defaults remain C4's responsibility. Host caller migrations use `settings.ResolveConcurrency`, whose body remains a W6 stub. |
| A1-37 | Existing Cache builders remain implemented. Added public `DirCache.Key(workdir string) (string, error)`, with exact-byte hashing and root-confined reads. Migrated pipeline lint to declared caches. Removed old lint-cache HTTP credentials/calls and ToolSlot budgeting. The linter uses `--allow-serial-runners` and the existing ignored per-worktree cache directory. |
| A1-51 | Extended the single v2 corpus with ten declaration/hash vectors and six inner-error vectors. Covers omission/default/zero, snake_case, approval conditions, generated declarations and secret IDs. Updated both schema snapshots. Migrated pipeline and Kubernetes fixture entrypoints to `sparkwing.Main`, plus production pipeline callers of removed SDK APIs. W5 still must consume the new vector rows. |
| A1-54 | Retained approval SkipIf, added a fake-host evaluation check, and preserved the removed executable approval hooks. The amendment resolves the previous approval question. |

Other affected rows need host/engine/storage integration rather than another SDK
API. A1-19 still needs every placement to supply its verified engine and granted
runner. A1-36/A1-48 need W3's concrete storage and shared environment builders.
A1-52/A1-53 require native and real placement evidence.

Local W4 contract additions are `DirCache.Key` and the internal operations
`Document` method used by the fake host to validate declaration snapshots. The
Go SDK notes document the key and shared corpus. No new contract change is
requested from another area. No new environment variable, limit or compatibility
alias was introduced.

## Deletions and ports

The lint implementation diff removes 65 old lines and adds four. Its tests remove
43 lines and add one, retiring the removed ToolSlot/budget behavior. No additional
legacy files or SDK compatibility surfaces were retained. Existing W4 hard cuts
remain intact; its production packages contain no Old-suffixed scaffolding or
unimplemented marker.

Earlier inventory ports remain in place. No new inventory source test was ported.
New regressions cover joined cancellation, selector validation, acknowledged route
writes/refusals, canonical write failures, concurrency presence, six identifier
collision classes, collisions in both expansion/registration orders, approval
conditions, and public cache keys including symlink/traversal refusal.

## Verification

All Go checks used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W4/cache, and GOTMPDIR/TMPDIR=/tmp/sw-astra-W4/tmp.
The paths below are relative to the supplied worktree. Commands used `go -C`
with its absolute path.

Passed:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go test -race -p 4 ./internal/sdkserve -run 'TestCancel|TestServeMetadataDoesNotBlockCancellation|TestServeCancellationKeepsBothReplies' -count=1
go test -race -p 4 ./sparkwing/sparkwingtest -count=1
go test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v
```

The three local-go-linux hello, version/hard-cut and SDK fake-host/linkage cells
pass. Their new collision, later-declaration, cache-key and approval subtests pass.
The linkage cell's HTTP subprocess subtest remains socket-blocked. Mask's SDK
service subtest passes, while its full target remains skipped for the real host
placement driver. No native cell is promoted by component evidence.

The seven package exclusions are inherited: three filesystem fallback cases
encounter the sandbox's empty ancestor `/tmp/.git`, and four service-port tests
need sockets. They were not retried. W4 decoder tests now run and pass.

The nested `.sparkwing` module also passes `go build -p 4 ./...` and vet over its
platform-selected production job files. Its test package cannot compile: existing
fixtures still call deleted registry/getter APIs, SetWorkDir, RuntimePlumbing,
RunWork and EventStepEnd. Those tests need a public fake-host port. The failed
compile ran no tests. This remains separate from the passing W4 package checks.

Formatting, schema drift checks and `git diff --check` pass. No Sparkwing binary,
repository Git write, release or deployment ran. Hook enforcement and full
pipelines remain unverified and belong to the lead. Shared Git config retains
Korey's identity and has no core.worktree override.

## Real integration and remaining work

- W2/W3 must mount the mask/annotation/summary routes with current process
  authority, sink-wide acknowledgement, collision checks and step membership.
  W4's RoundTripper fakes exercise their declared transport without sockets.
- W6 must implement ResolveConcurrency and wire resolved group consistency into
  acceptance/expansion. W3's host callers now compile against that interface.
- W3/W1 still own persisted expansion/replay hashes, child/latest-output adapters,
  claims/lifecycle and storage authority. Canonical vectors do not prove those
  runtime integrations.
- W5 must use `plans` and `inner_errors` in the shared corpus. W9/lead must port
  remaining CLI/host/pipeline runtime fixtures and regenerate public references.
- Native Windows/Darwin, real HTTP/api.sock, remote placements and the complete
  acceptance matrix remain unverified. The prior architectural questions about
  masks and approval conditions are resolved by amendment 1.

## Shared files touched

Merge resolutions outside W4:

- internal/nodehost/{attempt,plan,podnode,scheduler,session,steps}.go
- internal/orchestrator/main.go
- internal/secrets/mask.go

Minimal concurrency caller migrations:

- internal/nodehost/{admission,declarations,run_admission}.go
- cmd/sparkwing/action_explain.go

Shared contracts and acceptance:

- design/rewrite/contracts/{c3-protocol-notes,c3-go-sdk-notes}.md
- tests/acceptance/{protocol_test,protocol_amendment_test,protocol_cache_test}.go
- This report.

Pipeline SDK caller migrations:

- .sparkwing/jobs/budget_test.go
- .sparkwing/jobs/build_images.go
- .sparkwing/jobs/example.go
- .sparkwing/jobs/gate.go
- .sparkwing/jobs/integration.go
- .sparkwing/jobs/jev_lint.go
- .sparkwing/jobs/jev_sweeper.go
- .sparkwing/jobs/lint_contention.go
- .sparkwing/jobs/lint_contention_test.go
- .sparkwing/jobs/lint_worktree_test.go
- .sparkwing/jobs/pre_commit.go
- .sparkwing/jobs/pre_push.go
- .sparkwing/jobs/pre_release.go
- .sparkwing/jobs/release.go
- .sparkwing/jobs/store_postgres.go
- .sparkwing/jobs/template_verify.go
- .sparkwing/jobs/tool_cache.go
- .sparkwing/jobs/weather.go
- .sparkwing/main.go
- testdata/k8s-e2e/repo/.sparkwing/main.go

The pipeline changes replace annotations, retry backoff, approvals and failure
policy with their retained SDK forms, and keep tooling cache paths local to the
repository's existing ignored `.sparkwing/cache` directory. Test migrations are
partial because the remaining old runtime fixtures require the larger port above.

Friction: denied sockets, empty `/tmp/.git`, an Overwing binary ten commits behind
its source, and the inherited nested-module test compile failures. All started
checks finished. Temporary caches and scratch under `/tmp/sw-astra-W4/` were
removed after verification. No scratch outside that directory was created.

STATUS: CONTINUE
