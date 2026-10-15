# W8 local integration blockers and cuts

Continues `858076d68` and `W8-LOCAL-SERVICE-SEAMS-CONTINUATION.md`.
The local-controller priority remains incomplete. Its implementation has not
changed this session because the remaining command bindings need foreign
contracts or implementations listed below.

The production probes reproduce three outcomes:

| Probe | Result |
| --- | --- |
| W9 local controller constructor | Passes. Construction alone does not establish a running queue. |
| W9 local run | Reaches admission transport, then the sandbox refuses the Unix socket. Foreground hosting still has the explicit private-state binding refusal in `local_run.go`. |
| W9 local schedules install | Fails with `local schedules require checkout, environment and pin registration`. |
| W8 persisted peer claimant | Fails with `invalid authority reference` during shared delegation from a fresh real SQLite store. |

The unchanged local owner already supplies the single store, RunStore,
reconciliation, queue/activity/maintenance workers, TypeSafeAPIKey, W2's HTTP/UI
handlers and joined cleanup. `NativeServices` consumes W3's real host interfaces.
`NewScheduleService` uses the elected store and the shared cron launcher
interface. Their fake-backed component suites pass. These facts do not close the
command integration requirement.

Contract changes or implementations needed in other areas:

| Owner | Concrete handoff |
| --- | --- |
| W2/W1 | Supply a persisted, delegable authority for the verified local peer through shared authentication/issuance. Reproduce with `TestLocalClaimantW8UsesPersistedPeerAuthority`. W8 does not substitute root provisioning for delegation. |
| W3/W2/W9 | Provide authenticated private ReadSnapshot, ReadNode, TransitionRun and TransitionNode transport, plus AuthenticationReader and BindingChecker adapters, for CLI-process hosting. `NativeConfig` already consumes these interfaces without opening another store. `controllerwire.Controller.ReadSnapshot` returns a scheduling projection and cannot supply private execution state. |
| W2/W9/W1 | Declare local checkout registration with write mode, selected declarations, captured environment and proof/follow choice. Supply pin/unpin and tick transport and a durable environment association. ScheduleRepoRequest/ScheduleManager do not express these operations. |
| W1 | Join scheduled execution to sealed trusted intake. The retained controller cron launcher still calls the legacy trigger intake. The W8 schedule factory accepts the declared `crons.Launcher` and tests it with a fake. |
| W3/W7/W2 | Complete native owner policies, authorized byte/record bindings, process binding relations and controller log projection recorded in the preceding report. |
| Lead/T1 | Supply native sockets, Windows/Darwin, Kubernetes isolation/egress and teardown evidence. |

The 12:35 retry-origin correction remains complete. Hosting, launcher claim and
Cloud exchange tests pass through their existing entrypoints. A1-38's W8 award
fence and persisted synchronization work remains complete at the component
boundary, including `TestStoreLauncherW8`. Native Kubernetes evidence stays open.
KeepAliveReasons, LocalPlatformPolicies, CloudUnitPolicies and CloudPodInvariants
remain deleted. All W8 amendment-1 dispositions in the preceding report still
apply. No newly assigned W8 contract row was found.

This session removes two duplicate mechanisms. Engine JSON decoding now uses
the existing strict JSON dependency for duplicate-key validation. The custom
recursive token walker is deleted. Typed decoding retains its prior behavior.
Invariant: a signed policy is accepted only when every JSON object has unique
keys, including unknown fields. `FuzzSignedPolicyRejectsDuplicateKeys` signs
generated nested objects and arrays, checks refusal of duplicate keys, and
checks acceptance of the corresponding unique-key documents.

Runner `version` now uses the registry parser shared by the execution commands.
Its separate flag definitions, argument validation and format normalization are
deleted. Help and output values follow the declared grammar. The existing help
test now checks the shared help surface. HOSTING.md records the parser owner.
No new dependency, option, environment variable, service or compatibility path
was introduced.

Production changes add 12 lines and delete 66, net -54. No whole production
file was deleted. The accounting uses the lead's production exclusions against
main, including rename detection. Tests, testdata, acceptance and contract
declarations are excluded.

| W8-owned path | Added | Removed | Net versus main |
| --- | ---: | ---: | ---: |
| internal/cluster | 582 | 2,564 | -1,982 |
| internal/runners | 3,632 | 2,235 | +1,397 |
| internal/wingd | 3,819 | 2,021 | +1,798 |
| internal/engine | 2,218 | 0 | +2,218 |
| Total | 10,251 | 6,820 | +3,431 |

W8 remains 5,731 lines above its -2,300 budget. The prior engine distribution,
runner composition and native lifecycle additions retain their previous
justifications. No budget waiver or completion is claimed.

Verification used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and at most -p 4. Temporary Go directories
were under /tmp/sw-astra-W8.

- Whole-repository `go build -p 4 ./...` passes.
- Full engine/... and runners/... suites pass.
- localcontroller's short suite passes, including the real store/schedule factory components.
- Cluster passes with only the native socket metrics-listener test excluded.
- Vet passes for engine/..., cluster, localcontroller and cmd/sparkwing.
- `tests/acceptance -run 'W8|LauncherCloud'` passes. No new native cell is activated.
- The signed-policy fuzz property passes 1,026 generated executions.
- `commentcheck -base main .` is clean across 2,262 files. `git diff --check` passes.
- The tagged production/peer probes retain the results above.

No additional T1 test was ported. No cmd/sparkwing composition file or other
area's production caller changed this session. No internal/orchestrator or
lead-owned contract file changed. No Git write, candidate installation,
pipeline, deployment or release ran. The shared Git config has no local identity
or core.worktree override. Worker scratch was deleted after checks joined.
The shared build cache was retained.

Friction remains the three missing shared authority/transport boundaries and
the area budget. The lead needs those exact interfaces or implementations
before another local command wiring pass can close the priority.

STATUS: CONTINUE
