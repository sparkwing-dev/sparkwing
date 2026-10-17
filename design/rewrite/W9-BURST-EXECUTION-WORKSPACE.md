# W9 burst execution workspace continuation

Starting HEAD: `7d9570b22`. The previous contention and requester-secret work is
retained. No Git writes, merge, operational Sparkwing execution, installation,
deployment or release ran. Lead-owned amendments and handoffs are unchanged.

## Implemented

- Added a composed proof that uploads real Go SDK source, acquires it under the
  awarded claim, builds it with the shared SDK installer and executes the built
  binary through NativeProcesses and ProtocolSession. Here, personal-machine and
  exchanged Cloud awards all deliver the requester secret, mask raw and base64
  logs, publish an artifact and retrieve its bytes through authorized storage.
  The existing intake fixture also exercises ranked holds, contention, default
  uncapped overflow, zero-cap Cloud refusal, personal eligibility and fencing.
- The new proof found empty artifact manifests on all three placements. SDK
  execution used the checkout root while publication searched `.sparkwing`.
  Publication and consumed-file staging now use the same existing working-directory
  selector as SDK execution.
- Reproduced and fixed the same mismatch for relative cache directories, cache
  key files, package-manager lookup and package-manager execution. A nested
  pipeline directory now saves and restores the cache the job actually uses.
- Updated the SDK guide, C3 contract clarification, changelog and embedded mirrors.

Invariant: artifact and dependency-cache paths resolve in the same checkout as
SDK execution. The artifact property varies file names, bytes and pipeline depth,
puts conflicting bytes in the SDK directory and verifies publication and consumed
restoration from the execution root. Existing archive, capture and authority
confinement properties remain in force.

## Verification

All Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W9/go` and
`TMPDIR=/tmp/sw-astra-W9`, with at most four package or fuzz workers.

Passed:

- Whole-repository `go build -p 4 ./...` and focused vet on nodehost, controller
  and CLI.
- Complete short nodehost suite. Controller burst and requester-secret selections,
  including the new compiled-source proof on all three placements. CLI burst,
  preparation, tracked edits, routing, fallback and secret-exclusion selections.
- Race checks on the new compiled-source proof, cache workspace and package-manager
  regressions, artifact workspace property and selected confinement properties.
- Three-second artifact workspace fuzz campaign: 77 executions. Its seeds and
  both cache regressions fail with starting-HEAD production files through a Go
  overlay and pass with this fix. The initial compiled-source execution also
  failed on all placements before the artifact fix.
- Focused cluster, runners and localcontroller direct-dependent checks.
- W9 surface grammar, query/prune, management authority, wire-detail and run
  observation acceptance selections. No native matrix cell was newly unskipped.
- Embedded docs excluding listener-based WebClient tests, full doccheck, mirror
  drift and whitespace. Commentcheck against HEAD is clean across eight Go files.

Commentcheck against main still reports the inherited untagged pinnedClock
comment at `pkg/store/store.go:108`. No archived test was newly ported. Existing
test ports and legacy cuts remain as recorded in earlier W9 reports.

## Contract changes made

C3 protocol/SDK clarifies that artifacts, consumed files, relative cache paths,
key files and package-manager queries share SDK execution's checkout root. The
pipeline directory remains the SDK source/install directory. It records the new
compiled-source proof and its fake boundaries. No foreign API amendment, SQL,
wire field, public flag, environment variable, quota, cap or secret store changes.

## Remaining native integration

The public CLI probes were rerun with `rewrite_integration`. All five execution
cases skip at `listen tcp 127.0.0.1:0: socket: operation not permitted`. Their
parent cancellation test reports PASS with skipped children. This is unverified
native coverage, so all five launch parts cannot yet be reported complete.

The lead should run outside the socket-restricted sandbox:

```bash
env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./cmd/sparkwing -run '^TestBurstPublicRun' \
  -count=1 -v -timeout=15m

env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./pkg/controller \
  -run '^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$' -count=1 -v
```

Real integration points still open:

- Public CLI/controller startup, terminal streaming, Ctrl-C, competing local
  admission and installed personal-runner lifecycle through native sockets.
- Signed release/engine distribution on a fresh requesting or claiming machine.
  The new proof uses a fake engine resolver, a local SDK replacement and the real
  installer in process. It does not exercise selected-engine installation IPC.
- Shipped Cloud runner image, DinD, object service and network isolation on the
  deployed cluster. Cloud Job construction/exchange uses fake Kubernetes.
- Native declared-cache composition and Windows, Darwin and PostgreSQL lanes.

Requester values remain volatile within the supported single-controller launch
topology. Restart requires resubmission. Multiple replicas require an affinity or
ephemeral-relay contract. The existing retry-selection DESIGN remains with the
lead and was not patched again. Nothing here changes either boundary.

## Size, shared files and friction

Production Go this session: +6/-6, net 0. Six references to the pipeline directory
for runtime work are replaced by the existing selector. No new runtime mechanism
or API is introduced, and no callers require migration. No legacy subsystem was
newly replaced. W9 production versus main remains +18,329/-23,824, net -5,495,
within its -2,900 budget. Counts exclude tests, testdata, acceptance and contract
declarations.

Shared files touched:

- `internal/nodehost/artifact_layer.go`, `depcache_records.go`,
  `cache_directory.go`, `cache_directory_unix_test.go`, `workspace_layers_test.go`.
- `pkg/controller/burst_acceptance_test.go`, `burst_sdk_source_test.go` and
  `testdata/burst-sdk/main.go`.
- C3 protocol/SDK contract, CHANGELOG and generated embedded-doc mirrors.

Mechanisms added: test-only source selection and a compiled SDK fixture using
the existing FIFO HTTP test transport. Runtime adapters reuse the existing
working-directory selector. No new service, registry or configuration is added.

Friction: forbidden sockets, the inherited main commentcheck finding and
Overwing's source-lag notice. All checks are joined. Worker scratch is removed
at the end and the shared build cache is retained. Local Git identity and
core.worktree overrides are absent.

STATUS: CONTINUE
