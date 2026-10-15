# W8 retry awards, chart admission and size

Continues `5d39c795d` and `W8-AWARD-FENCE-AND-BUDGET.md`.
**A1-38 is closed for W8: launcher-side award fencing and claim-generation
correlation are implemented.** W1/W2 still owe the transactional sync reader.

## Implemented

Invariant: execution authority names the live run, while source acquisition
retains the original request and accepted seals. Retry intake preserves those
seals, so their run ID can differ from the execution run. Cloud claim and
bootstrap now compare accepted source identity with the original request and
keep the capability binding fenced to the live run. Changed accepted request
hashes fail. The new property failed for all three Cloud unit kinds before the
fix, then passed through claim and exchange. The existing sync-generation
property now uses original-run seals while retaining current/stale Job checks.

Invariant: an incomplete node award cannot reach controller confirmation or
native service binding. The hosting validator now requires execution settings
alongside the accepted plan and engine. The generated omission test failed for
missing settings before the fix and now covers all eight input combinations.

Both Helm charts pass `runner.repos` through the existing repeatable `--repos`
flag. The default empty list still admits no repository. The chart test parses
rendered arguments through the real runner grammar and repository matcher,
checking configured and foreign repositories. Bundle version is 0.1.12, parent
version is 0.2.1, and the parent dependency archive and lock are refreshed.
Source versions changed only; no release or deployment ran.

The engine cache, installer and runner updater share one verified-release reader.
Malformed, missing or ambiguous verification records miss the cache and are
repaired through normal verified resolution. SDK installation still fails on
bad verification. Removed the unused exported `PrepareUpgrade` method because
`Upgrade` with `DryRun: true` already supplies the preview. This is a W8-local
contract cut, reflected in `contracts/c3-engine-version.md`. No caller needed
migration. SDK installation decodes its stream directly because the request
type already owns strict JSON validation.

## Production lines

The lead's production-Go metric excludes tests, testdata, acceptance code and
contract declaration paths. Counts include the uncommitted changes.

| W8-owned package | Net production Go lines against main |
| --- | ---: |
| `internal/cluster` | -1,967 |
| `internal/engine` | +2,016 |
| `internal/engine/sdkinstall` | +171 |
| `internal/runners` | +1,376 |
| Removed `internal/runners/k8s` | -1,465 |
| `internal/runners/launcher` | +1,058 |
| `internal/runners/runnerflags` | +64 |
| `internal/runners/service` | +553 |
| `internal/runners/service/unitfile` | +376 |
| Removed `internal/runners/warmpool` | -406 |
| `internal/wingd` | +1,679 |
| `internal/wingd/client` | +413 |
| `internal/wingd/localcontroller` | +306 |
| **W8 total** | **+4,174** |

W8 remains **6,474 lines over the -2,300 budget**. This session adds 28 and
removes 62 production Go lines, **net -34**, all in W8 paths. Production Go
outside W8 changes by **0 lines**. Chart templates and values add six
production YAML lines, counted separately from Go. No legacy package was newly
deleted. The old W8 worker, pool, trigger and pod executor cuts remain in place.

## Package justifications

`internal/engine` retains 2,016 lines because decisions 101-103 require exact
Go/npm pins, signed policy and verified fallback, historical closed release
matrices, executable verification/cache and verified upgrades. These additions
were not sized in the frozen budget. The shared reader removes duplicate
verification loading and the unused preview surface is gone. Removing a remaining
owner would remove a named contract behavior.

`internal/engine/sdkinstall` retains 171 lines because the selected verified
engine must install and compile both SDKs. It uses the existing local Go cache
and npm rather than separate placement installers. Moving its binary-cache
dependency into engine creates an import cycle through runspec/store. Together
the two engine packages add 2,187 unsized lines.

`internal/runners` retains 1,376 lines because one claim loop must keep held
hosts live while policy refresh, cancellation and committed-award adoption occur.
It also owns captured award inputs, repository admission, credential startup and
native verified replacement. Shared hosts own source acquisition and renewal.
Replacing the loop with synchronous claiming would drop those lifecycle rules.

`internal/runners/runnerflags` retains 64 lines because entrypoints and installed
service inspection must parse the same grammar. It consumes the registry and
adds no command vocabulary. Separate parsers would duplicate that ownership.

`internal/runners/service` retains 553 lines because installation, inspection,
refresh and removal must preserve Linux/macOS service state and refuse another
configuration or registration source. It replaces the deleted 645-line
agentservice package. That deletion is outside W8 paths and is not credited in
the table. Removing source checks or stopped-state handling would lose required
service preservation.

`internal/runners/service/unitfile` retains 376 lines because runners, local
controllers and schedules share native argument parsing, escaping, atomic file
publication and loaded-registration checks. Shell splitting cannot replace the
required systemd and XML argument rules. Folding the package would move lines
while restoring separate parsers.

`internal/wingd/localcontroller` retains 306 lines because the elected process
must open the store once, compose its queue and maintenance, and join workers
before closing services/store. Folding it into wingd creates a cycle through
runners/nodehost. Independent adapter openers would violate single ownership.

The existing launcher grows by 1,058 lines for the common plan/eval/work builder,
root/DinD, private bootstrap publication, exclusive reservations, network evidence
and restart-safe UID-fenced teardown. The existing wingd/client packages grow by
2,092 lines for local lifecycle, four activity holds, joined HTTP/dashboard drain,
managed units and verified Unix/Windows transport. Their retained admission
daemon serves the unchanged admission socket. The audit found no safe large
deletion in these owners. These are overage justifications for lead review.

## Verification

- Whole-repository `go build -p 4 ./...` passes with workspace disabled.
- Full cluster, runners/launcher/service/unitfile, engine/sdkinstall and chart
  suites pass. Focused vet passes. Corrupt verification-record repair cases pass.
- `go test -p 4 ./tests/acceptance -run 'W8|LauncherCloud'` passes, including
  native-store, bootstrap and launcher HTTP/store components. No native placement
  acceptance cell is newly closed. Chart rendering is real Helm execution.
- The hosting omission property passes 8,919 generated executions and the Cloud
  retry property passes 4,208. Focused race checks pass for both properties,
  current/stale generation correlation and restart cleanup.
- `go run ./internal/commentcheck -base main .` is clean across 2,125 files.
  `git diff --check` passes. Independent review found no actionable correctness
  issue and accepted the simplifications.

No legacy tests were newly ported. Existing W8 port dispositions remain in
`contracts/t1-port-inventory.md`.

## Amendments and remaining integration

- A1-08/09/18/19/26/27/36/39/51: unified claiming, captured identity and seals,
  shared hosts/keeper and W8 legacy executor retirement remain implemented.
  Production native service construction and actual SDK hello still need final
  source/log/storage/process bindings. The HTTP native-store component uses an
  in-process transport and tenant in place of a production remote store adapter.
- A1-10/11/12/43/45/46/48/50/52/55: selected-engine installation, captured
  source/environment, verified fallback, historical matrices and named here
  claims remain implemented. Engine duplication is reduced this session. Final
  CLI/source wiring, native Windows packaging and placement QA remain open.
- A1-20/49: private credential startup and CLI repository fencing remain
  implemented. Chart repository binding is now implemented too. Enrollment,
  rollback/revocation, Actions issuance and startup bootstrap issuance remain
  W1/W2/W6/W9 production composition points.
- A1-38: **W8 closed**, including retained source seals on retry. W1/W2 must
  implement `LauncherSyncReader.ReadLauncherSync` with transactional ancestry,
  award and observed Job-instance checks. Only its interface/test implementations
  exist in this checkout.
- A1-40/41: election, owned store/queue/maintenance, readiness, activity holds,
  managed units and joined drain remain implemented. Local state/activity,
  orphan reconciliation, shared authorized handlers and CLI service bindings
  remain production composition work.
- A1-42/53: deployment-specific `KubernetesBoundary.Probe/Clear`, enforced
  egress, DaemonSet inventory, escape canary, full DinD teardown, aggregate
  scratch eviction and native Linux/Darwin/Windows evidence remain open.

The approved pending-claim outcome/cancellation work and four dead declaration
cuts remain finished. `cmd/sparkwing-runner` still supplies an empty Runtime
through `MainWithVersion`; it is not a working production host entrypoint yet.

## Foreign contracts and scope

W3/W9 need to agree how the remote native store contract is supplied.
`RemoteOwnerStore` implements scheduling, reconciliation and expansion only.
`NativeHostStore` additionally requires sealed snapshots, owner transitions,
completion and owner/node renewal. The wire `ScheduleSnapshot` contains scheduling
state rather than the full persisted seals and owner state. W2/W9 also need to
supply the authenticated principal required by `Hosting.Bind`; decoding the
opaque award is not authentication. W8 continues testing these declared
boundaries with fakes instead of inventing another policy or wire contract.

The earlier W3/W6 source-contract request remains open: distinguish an uploaded
working-tree snapshot from a committed Git-bundle seed before implementing
working-tree consent. No foreign contract changed this session.

No production caller outside W8 needed migration. The only changed contract doc
is W8's engine contract. The lead-owned amendments and handoffs are unchanged.
No W3 path, including internal/orchestrator, was edited or deleted.

Friction: the sandbox has no deployment/native platform evidence and the brief
prohibits Sparkwing execution, so candidate installs and pipelines remain with
the lead. Overwing standards discovery/read works despite its stale-binary
notice. No Git writes ran. Worker scratch is removed and
`/tmp/sw-gocache` is retained.

STATUS: CONTINUE
