# W9 burst contention and requester-secret integration

Starting HEAD: `3adbcd26b`. No commits, merges, installation, deployment or
operational Sparkwing execution ran. Lead-owned amendments and handoffs remain
unchanged. Earlier W9 routing, source, personal enrollment and overflow controls
are retained.

## Implemented

- Burst readiness acquires logical concurrency without local compute. A busy
  requesting machine can release jobs to its ranked personal and Cloud claimants.
  Run resource settings still apply to the sealed jobs.
- A successful here self-claim acquires a job-specific machine lease before
  starting an attempt and retains it through cleanup. Capacity stolen after the
  observation queues that awarded job. Cancellation joins the queue wait.
  Setup refusal uses the existing fenced result path and consumes no attempt.
- Burst claimants leave logical concurrency with the requester. A registered
  runner sharing the requester's machine no longer charges its semaphores twice.
- A missed self-claim retains logical concurrency while waiting for placement.
  The existing state follower wakes on changed execution eligibility as well as
  persisted lifecycle changes. Each wake rechecks the store's fenced claim.
- Unhinted jobs match against the same one-core charge the local queue already
  uses. The shared admission constant replaces the duplicate queue constant.
  Trigger ownership remains claimable on a saturated machine, so scheduling and
  overflow can start. Explicit resource settings and Cloud allocation are unchanged.
- The real requester-secret HTTP proof now inspects SQLite and WAL bytes and
  opens a second Store over the same database. The second process refuses lost
  ephemeral values with 503 while the original process still serves its live
  claim. Generated values exercise the whole intake, identifier refusal,
  declaration, release and stale-fence path.
- The public native probe now includes a held machine-capacity lease followed
  by a here-first run that must complete on a personal machine. Its source,
  masking, artifacts and Cloud ownership assertions reuse the existing probe.

Invariants: unclaimed burst jobs hold no machine capacity. Every self-claimed
attempt holds its own machine lease. Logical concurrency is charged once and
retained while placement is unresolved. Requester values never enter SQLite,
WAL, persisted projections or the source bundle and never fall back after loss.

## Verification

All Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W9/go`,
`TMPDIR=/tmp/sw-astra-W9` and at most four package/fuzz workers.

Passed:

- Whole-repository `go build -p 4 ./...` and focused vet on CLI, admission,
  wingd, nodehost, store and controller.
- Complete short nodehost, admission, match, limits, cluster, runners and local
  controller suites. Store claim, placement, owner, metering, burst, overflow,
  personal-machine and local-secret selections.
- CLI burst source/preparation/routing and overflow selections. Controller
  burst intake, ranked claims, default and explicit resource headroom, account
  controls, requester-secret release, SDK execution and Cloud Job selections.
  The native Go SDK here/personal/Cloud proof uses FIFO HTTP transport and fake
  Kubernetes, with real process pipes, authorization, store and artifact storage.
- Real-store owner success, zero-attempt setup refusal, and saturated-owner to
  registered personal-machine completion. The latter retains requester
  concurrency until the other machine's persisted result.
- Race checks on burst owner/admission/follower and requester-secret paths.
  Generated release and concurrency properties pass. Three-second campaigns:
  unclaimed resources 3,003 executions; single concurrency charge 2,594;
  requester-secret persistence/release 9.
- Embedded docs excluding listener-based web-client tests, complete doccheck,
  mirror drift, whitespace, and commentcheck against HEAD.

The broad host suite exposed an inherited cancellation fixture race. The same
failure reproduced with starting-HEAD production files through a Go overlay.
Its fake executor reported on a cancelled work context. It now uses the bounded
uncancelled report context required by the production executor. Twenty repeats
and the full short host suite pass. No archived test was newly ported.

W9 surface grammar, run query/prune/detail/observation, enrollment, credential
policy and cancelled-host log acceptance checks pass. No native matrix cell is unskipped or
claimed from fake-backed evidence.

## Contract changes made

- C1: matching projects the existing one-core admission charge for all-zero
  node resources through the common matching-input reader. It changes no seal,
  SQL schema, Cloud price or billing allocation.
- C3 protocol/SDK: burst concurrency stays with the requester, compute follows
  the self-claim, and setup failure retains attempt zero. Owner state waits can
  wake on execution eligibility, which grants no execution authority.
- C4: records separate burst logical-concurrency and machine-resource lifetimes.

No foreign contract change is requested. No new flag, environment variable,
limit, persistent registry or credential transport is added. The old private
queue charge constant is removed and its production/test callers migrate to
`admission.DefaultChargeCores`.

## Remaining native integration

The public CLI probes compile but all five here/personal/contention/cancellation
cases skip because this sandbox refuses loopback sockets. They are not passes.
The lead should run:

```bash
env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./cmd/sparkwing -run '^TestBurstPublicRun' \
  -count=1 -v -timeout=15m

env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./internal/controller \
  -run '^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$' -count=1 -v
```

Real Cloud runner-image/DinD execution, network isolation, released fresh-machine
engine resolution, native declared-cache composition and native platform lanes
remain external verification points. The Cloud Job proof uses fake Kubernetes.

Requester secrets work under the supported single-controller launch topology.
A restart loses their volatile values and requires a new submission. Multiple
replicas still need a verified affinity or ephemeral relay contract. No value
persistence or relay was added. The prior retry-selection DESIGN remains with
the lead and was not patched again.

Commentcheck against main still reports the inherited pinnedClock comment at
`internal/store/store.go:108`. Listener-based docs web-client tests cannot run here.

## Size and shared files

Production Go this session: +94/-24, net +70. W9-owned production this session:
0. W9 versus main: +18,329/-23,824, net -5,495, within the -2,900 budget.
Counts exclude tests, testdata, acceptance and contract declarations. No legacy
subsystem is newly replaced. Earlier legacy cuts remain.

Shared implementation/test edits:

- `internal/admission/admission.go`.
- `internal/nodehost/claimed_node.go`, `local_admission.go`,
  `native_admission.go`, `owner.go`, `owner_schedule.go`, `state_follower.go`,
  `scheduler_test.go`, `store_host_fixture_test.go`, `burst_admission_test.go`
  and `burst_store_admission_test.go`.
- `internal/wingd/server.go` and `unit_test.go`.
- `internal/store/unit_matching.go`.
- `internal/controller/local_secrets_test.go` and `runner_capacity_test.go`.
- C1/C3/C4 contracts, CHANGELOG and generated embedded-doc mirrors.

The CLI native probe and local-execution guide are W9-owned.

Mechanisms changed: the existing owner admission now wraps awarded compute
separately from logical concurrency. The existing follower observes eligibility
and retains its joined subscription while waiting. A readiness-only change
would release logical concurrency during a missed self-claim. A compute-only
change would still let unhinted jobs claim a saturated machine. These changes
reuse the same matcher, queue, follower and fenced result owner. Reverting the
burst-specific admission and follower changes restores the prior lifetimes.

Friction: forbidden sockets, the inherited commentcheck finding, and the
standards tool's source-lag notice. No approval was requested. All checks are joined. The process sweep found no
worker scratch executables. `/tmp/sw-astra-W9` is removed and `/tmp/sw-gocache`
is retained. Local Git identity and core.worktree overrides are absent.

STATUS: CONTINUE
