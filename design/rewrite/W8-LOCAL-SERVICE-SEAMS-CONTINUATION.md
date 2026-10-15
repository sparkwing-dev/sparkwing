# W8 local service continuation

Continues `4f200d128` and `W8-AUTHENTICATED-ATTEMPT-CONTINUATION.md`.
The local-controller priority remains incomplete.

## Implemented

`NativeServices` accepts `NativeConfig` with the existing shared host-state,
authentication and binding interfaces. The old constructor required a concrete
store and tenant. The embedded queue still passes its elected store and tenant.
The constructor opens no store. Its state follower, process authorities, node
claims, renewal and capacity binding use the supplied services. A transport
adapter can supply the same interfaces for foreground hosting.

Invariant: process service construction keeps the refreshed award and persisted
state in the same execution and team. Foreign refreshed authority, run identity,
tenant or request team fails before transport opens. The property exercises all
combinations with generated suffixes. The constructor test uses fake state and
authority readers, retained environment files and an in-memory listener, then
constructs W3's real owner, claimed host and evaluator. It proves construction,
not completed execution through a remote transport.

`NewScheduleService` constructs the local cron service over the elected owner's
existing default-team tenant. It captures the kernel account and hostname and
binds the tick lock and pin root to the owner's home. It requires the shared
`crons.Launcher` interface. Its SQLite test registers a declaration, pins,
unpins, repins, ticks, reads committed fire history, disarms and checks pin
removal. Compilation and launch are fakes. The fake launcher names a seeded run
so the real fire-history foreign key is checked. The factory has no production
caller until the local operations below have a transport and intake binding.

The controller's three workers now start directly through one small launcher
closure. The worker table is deleted. Failure still cancels and joins every
worker, and independent errors remain joined.

The old positional `NativeServices` signature is removed. Its only production
caller is migrated. There is no forwarding API. No whole legacy production file
is deleted this session. Production edits remove 28 lines and add 78 in W8,
net +50. Worker startup alone shrinks by five lines.

## Contracts and real integrations still needed

| Owner | Required boundary and evidence |
| --- | --- |
| W2/W1 | A verified peer needs an authority usable by shared delegation. `TestLocalClaimantW8UsesPersistedPeerAuthority` still fails with `invalid authority reference` from a fresh real store. The peer snapshot has no authority reference. W8 does not replace this with root provisioning. |
| W3/W2/W9 | Foreground hosting needs authenticated private `ReadSnapshot`, `ReadNode`, `TransitionRun` and `TransitionNode`, plus the parent-authentication and binding adapters used by shared process delegation. W8's constructor now consumes those existing interfaces. The fetched-engine client does not supply them. Public scheduling snapshots cannot substitute for private execution state. |
| W2/W9/W1 | Local registration needs the absolute checkout, selected declarations/write mode, captured environment, proof/follow choice and durable pin association. Pin/unpin and tick operations also need a local transport. `ScheduleRepoRequest` accepts a clone URL and lacks these inputs. `ScheduleManager` has no pin or tick operation. `CronSchedule` has no captured-environment association. |
| W1 | Trusted schedule intake still needs the sealed intake path. The controller cron launcher uses the legacy trigger intake and validates clone URLs. The local factory requires `crons.Launcher` and tests with a fake instead of using that path. |
| W3/W7/W2 | Native owner policies, authorized outputs/records, controller log projection and process binding relations remain the integrations recorded in the preceding report. |
| Lead/T1 | Native sockets, Windows/Darwin, Kubernetes, Cloud isolation/egress and privileged sidecar teardown still need platform evidence. |

The unchanged W9 production probe still reports controller construction success,
local-run admission socket refusal by the sandbox, and local schedules failing
with `local schedules require checkout, environment and pin registration`.
Constructor success does not establish queue readiness. Foreground CLI binding
and the command schedule factory remain incomplete.

## Existing assignments

The 12:35 retry-origin fix remains implemented across hosting, launcher claim
and Cloud exchange. Their package suites pass. A1-38's immutable Job/award fence
and persisted synchronization integration remain implemented. The W8 acceptance
selection passes, including the store-backed launcher proof. Native Kubernetes
execution remains open.

`KeepAliveReasons`, `LocalPlatformPolicies`, `CloudUnitPolicies` and
`CloudPodInvariants` remain deleted. No finished work was rebuilt.

All W8 amendment dispositions in the preceding report remain applicable.
A1-39/A1-40 advance through the interface-based native constructor and schedule
factory. A1-45/A1-46/A1-48 retain captured-environment loading in the new
constructor test. They do not gain a completed production placement claim.
No additional T1 inventory test was ported or acceptance cell unskipped.

## Verification

All Go commands used `GOWORK=off GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, scratch under `/tmp/sw-astra-W8`, `GOMAXPROCS=4`
and at most `-p 4`.

- Whole-repository `go build -p 4 ./...` passes on the final source.
- Full short `internal/wingd/localcontroller` and `internal/runners/...` suites pass.
- `tests/acceptance -run 'W8|LauncherCloud'` passes.
- Focused CLI local-service, native owner and schedule-factory tests pass.
- Scoped vet passes for localcontroller, runners and CLI.
- Final focused race checks pass for native binding, schedules and controller composition.
- `FuzzNativeBindingsRejectForeignScopeBeforeTransport` passes 1,000 generated cases.
- `commentcheck -base main .` passes across 2,262 files. `git diff --check` passes.
- The tagged peer and W9 production probes retain the failures above.

The internal local-controller source document describes the new boundaries.
No public CLI grammar or wire contract changed. No pipeline, candidate binary,
installation, Git write, deployment or release ran. Scratch was removed after
checks joined. The shared build cache was retained.

## Shared files and volume

The only `cmd/sparkwing` edit is `local_host.go`, migrating the constructor call.
It is +4/-1, net +3 production lines outside W8. There are no other production
caller migrations and no edits in `internal/orchestrator` or lead-owned contracts.
The owned test caller migration is `internal/wingd/localcontroller/native_test.go`.

Counts use the lead's production exclusions and Git's rename detection, including
untracked production files. Tests, testdata, acceptance and contract declarations
are excluded.

| W8 path | Net production lines versus main |
| --- | ---: |
| internal/cluster | -1,967 |
| internal/runners | +1,397 |
| internal/wingd | +1,798 |
| internal/engine | +2,257 |
| Total | +3,485 |

W8 is 10,284 added / 6,799 removed against main. This session is +78/-28,
net +50. W8 remains 5,785 lines above its -2,300 budget. No waiver is claimed.
The existing engine-policy/distribution and native lifecycle additions retain
the justifications in the preceding report. A broader simplification or lead
budget decision remains required.

Mechanisms added: the requested schedule factory and a native dependency bundle.
The bundle replaces concrete positional dependencies because foreground hosting
must not open the elected store. Reverting the constructor and its one caller
undoes that change. The private listener seam is package-local and tests transport
construction without sockets. There is no new state store, credential issuer,
environment variable, user option, timer or compatibility path.

Friction: the three missing shared authority/transport boundaries above prevent
command integration. Source size also remains above budget. The next work needs
those contracts rather than another W8 state or credential implementation.

STATUS: CONTINUE
