# W9 full-area review fixes

Session base: `4f9342f23`. No Git writes. Lead-owned amendments and handoffs
are unchanged. Rulings 1-4, 6 and 8-12 and the five simplifications retain their
earlier implementations. Ruling 5 and the standalone part of ruling 7 remain open.

Implemented this session:

- Cloud overflow retains `--sw-no-bincache` and `--sw-hash-all-files` during
  inspection and in the source seal used by hosts and retries. Inspection now
  uses the acquired SDK entry through the shared planner. The second CLI process,
  its repeated preparation and its separate description decoder are deleted.
- Cloud relative priority uses the same queue resolver as local runs. It resolves
  after source upload, before intake, and retains flag provenance. Integer
  overflow still fails before submission.
- Local `--sw-index` and the hook index are copied into private environment
  storage. Git normalizes the copy into a self-contained index, including split
  indexes. Intake secures the rewritten file before publishing it. Snapshot loads
  verify its directory, private-file ownership/permissions and content hash.
  Detached hosts and retries retain the captured contents. Missing, nonregular,
  malformed or changed index files fail. Cloud routing refuses this local path.
- Removed the restored `--target` engine flag from parsing, projection, help and
  current docs. The v0.6.0 migration already removed targets in favor of pipeline
  names. C4 records the cut. The changelog and v0.67.0 migration explain it.
  Author arguments named target still follow `--`. The lead must carry this cut
  to Korey under ruling 7.
- The signed released-engine fixture now replaces corrupted cached bytes
  atomically. A real engine blocked on installer input makes the old in-place
  corruption fail deterministically with `text file busy`. The replacement still
  proves that offline acquisition rejects corrupted cached engine bytes.

Production accounting: +181/-110, net +71 across all packages this session.
W9 alone is +152/-110, net +42. W9 versus main is +19,497/-25,168, net -5,671,
within its -2,900 budget. Counts include the new untracked production file and
exclude tests, testdata and contract declarations. No whole production file was
deleted. Removed 110 production lines, including the inspection injection struct,
subprocess preparation, duplicate result parser and replaced inline priority
logic. Deleted the two injected inspection test files (341 lines) and a dependent
45-line fake test, replacing their relevant behavior with real integration paths.
The old parser-only index test remains.

The build-policy invariant is that inspection, hosting and retries use the same
selected source policy. The index invariant is that a local run checks the
captured staged contents, and altered retained bytes never reach process startup.
The proxy invariant is that generated proxy credentials never appear in inspection
logs or returned errors. New tests ran red on lost build selections, lost Cloud
priority, unbound index contents and executable-inode corruption before the fixes.
They use real CLI parsing, Go compilation, source acquisition, SDK protocol,
controller routing, source upload and retry projection. The index test removes the
repository's shared-index files before retry inspection and generates 32 corrupt
retained payloads. Inspection generates 16 proxy passwords through the real SDK
process and checks raw and base64 output. No preparation or controller fake was
added. The existing W6 checking-tool test uses the retained-file contract and still
executes a real Git subprocess for plan, attempt and eval environments.

| Option | Kept or cut | Source and state |
| --- | --- | --- |
| Relative --sw-priority | Kept, implemented | C4 Registry/run and wingwire relative-priority contract. Local and Cloud preparation resolve the observed queue rank. |
| --sw-no-bincache | Kept, implemented | C4 Registry/run and uncached compiler. Local and Cloud inspection, hosting and retries carry the selection. |
| --sw-hash-all-files | Kept, implemented | C4 Registry/run and per-materialization hash policy. Local and Cloud paths carry the selection. |
| --sw-allow-unadmitted | Kept, pending | C4 Registry/run and decision 91 standalone mode. Its explicit refusal remains. |
| --sw-index | Kept, implemented locally | C4 procenv.Runtime.GateIndex and the existing staged-index contract. Private captured file survives retry. |
| --target | Cut | v0.6.0 migration and changelog remove target declarations and make pipeline names the selector. Ruling 7 permits retiring it. C4 amended. |
| --sw-request-id | Kept, implemented earlier | C4 Registry/run and detached tracing contract. |
| Hooks --fleet and --all | Cut earlier | Ruling 4 and C4 --all-repos grammar. |
| profiles set --token | Cut earlier | Ruling 12. Use --token-stdin. |
| profiles add --scope | Cut earlier | Simplification 5. Use member credentials. |

Contract changes made: C4 records single-owner inspection, Cloud policy/priority
binding, target removal and retained private index format. There is no new public
API, wire field, flag, environment name, route, table or execution limit.

Assumptions and mechanisms added: index files join existing private environment
snapshots because detached hosts and retries cannot depend on a caller's mutable
file or a split index's shared files. Keeping the original path was rejected
because changing it changes the checked staged contents. Removing the capture and
load verification restores the previous index refusal. No new cache or durability
protocol was added. Relative queue ranks remain observations, not reservations
against concurrent submissions. The source module owns source/engine validation;
inspection no longer repeats checks needed only by injected acquisition fakes.

Remaining work:

1. Ruling 5: capture present, allow-listed `run.submit_env_allow` values, transport
   them privately through controller intake and awards, consume them on hosts and
   mask them. The private local environment owner exists. Remote retention and
   delivery remain unconnected, and trigger's explicit refusal remains. Values
   must stay out of public names/digest snapshots.
2. Ruling 7: bind `--sw-allow-unadmitted` to decision 91's standalone local owner
   and admission semantics. Removing its refusal alone would silently run admitted.
3. Native verification after the lead commits this session. The latest native
   report still contains FAIL, so this area cannot be complete. Its old
   `./internal/projectconfig` target must become `./pkg/projectconfig`. This
   session fixes its released-engine fixture failure, but only the harness can
   replace that evidence. No harness file was edited.
4. Lead review, native Windows/Darwin, deployed Cloud, DinD, PostgreSQL,
   fresh-machine distribution and A1-53 remain the lead's integration lanes.

The prior amendment/handoff audit still applies: A1-09/16/17/38/41/43/49/50/51/52,
source attempts, step reports/windows, pipeline identity, launcher grammar,
doctor, local composition, owner/eval separation, shared logs, service handover
and decisions 110/111/113/114 retain their implementations. No new W9 assignment
appeared in the current handoffs. No foreign stub blocked this session. No further
contract change is requested from another worker. No inventory cell was newly
unskipped or archived test newly ported. Existing `TestSurface*` and
`TestEngineAmendments*` acceptance cells pass.

Shared files touched: `internal/nodehost/environment_snapshot.go` and its
`gate_index_test.go` caller migration. Generated documentation mirrors include
`pkg/docs/changelog.md` and `pkg/docs/mirror/{cli,cli-run,migrations/v0.67.0}.md`.
W9 inspection callers migrated in `trigger_native.go` and `burst_native.go`.
The dependent fake test in `describe_schema_unix_test.go` was replaced by the real
inspection test. No other area's production caller needed migration.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
TMPDIR=/tmp/sw-astra-W9 and GOTMPDIR=/tmp/sw-astra-W9/go. Recreate the scratch
Go directory before replaying. Commands run in the supplied worktree with `go -C`
and at most four package workers:

```sh
go build -p 4 ./...
go vet -p 4 ./cmd/sparkwing ./internal/nodehost ./internal/clisurface ./pkg/docs
go test -p 4 ./cmd/sparkwing -run '^Test(NativeHereTrigger|Trigger|NativeTrigger|PrepareTrigger|BurstNativePreparation|RunIndex|ParseRunFlags|LocalRunRejectsUnsupportedSelections|PipelineDescribe)' -count=1
go test -p 4 ./cmd/sparkwing -run '^Test(RunIndex|TriggerInspectionUsesRetainedBuildPolicy|BurstNativePreparationRetainsAdvertisedRunOptions|ReleasedEngineOfflineUsesSignedPolicyAndVerifiedCache|RunRelativePriority|LocalRunRejectsUnsupportedSelections|ParseRunFlags)' -count=1
go test -p 4 -short ./internal/nodehost -run 'Test(CapturedEnvironment|SDKGateIndex|NativeSDKStartsLoadCaptured)' -count=1
go test -p 4 -short ./internal/clisurface ./pkg/projectconfig
go test -p 4 ./tests/acceptance -run 'Test(Surface|EngineAmendments)' -count=1
go run ./internal/commentcheck -base main .
go run ./internal/sleepcheck -base HEAD .
go run ./internal/doccheck ./docs .
bash bin/sync-docs.sh --check
git diff --check
```

These focused checks pass. No full native CLI/bin suite or whole-repository test
suite was run. The broader short nodehost run found existing failures in
`TestBurstSharedOwner*`, `FuzzBurstMachineWaitNeverConsumesClaim` and
`TestOwnerDrainsPendingSupersessionIntent`. The burst fixture is refused at claim
setup as reserved for the requester/selected Cloud executors. The owner-drain test
hits its context deadline. Both groups reproduce with the original HEAD
`environment_snapshot.go` restored through Go's `-overlay`, so they were left for
their owner. The migrated gate-index and captured-environment tests pass. The docs
web suite cannot bind a socket in this sandbox. Its source drift check passes.

Friction: the original released-engine test passed initially, then the real
blocked engine made its inode overwrite failure deterministic. No sleep or retry
was added. The index fixture exposed Git replacing the copied index with broader
permissions, fixed by securing the normalized file. Overwing's binary reports
behind source. Temporary diagnostics were removed. Test process cleanup checks
pass. Git's local user.name and core.worktree remain absent. Scratch is removed
once checks join, and the shared build cache is kept.

STATUS: CONTINUE
