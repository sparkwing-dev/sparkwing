# Acceptance evidence

Run `go test -v ./tests/acceptance` from the repository root to retain JSON
results for executed assertion leaves and their cells. Empty cells and groups
whose leaves were all filtered out cannot pass. See the
[execution contract](../../design/rewrite/contracts/t1-acceptance.md).

The opt-in [local controller feature scenarios](LOCAL_FEATURES.md) exercise a
built CLI and dashboard with real processes, separate actors and isolated homes.

Evidence binds to the worktree digest at run time, the executing platform and
the selected driver. Running a saved binary compiled earlier with `go test -c`
against a changed tree is out of scope. Normal `go test` rebuilds from the
current source.

## Feature scenarios against a local controller

`TestFeatureE2E` builds the controller and CLI from the checkout, then starts
each scenario with a fresh HOME, credential epoch, SQLite database and filesystem
blob store. Two enrolled runners advertise different labels. Separate
account credentials submit and cancel work through HTTP. Runner actors drive the
real claim, admission, report, data and heartbeat APIs. They do not launch the
runner executable or execute an SDK pipeline body.

Run through local Sparkwing admission:

```sh
sparkwing run feature-e2e --sw-route local
sparkwing run feature-e2e --sw-route local -- --scenario placement --repeat 3
sparkwing run feature-e2e --sw-route local -- --scenario retry_failed
sparkwing run feature-e2e --sw-route local -- --scenario child_cancellation
```

The pipeline reserves two cores and builds with `-p 1`. It runs no Cloud jobs
and needs no staging credentials. Each repeat rebuilds from the tested checkout
and creates fresh state. To test `origin/main`, run it from an Xwing worktree
created with that base. The ordinary full acceptance suite includes these tests,
so the nightly full grid picks them up. `-short` skips them because the child
scenarios cross real ten-second recovery intervals.

| Scenario | Outcomes asserted |
| --- | --- |
| `placement` | Sealed repository placement tiers hold the fallback for the original three seconds, including after an unstarted award is returned. Another runner joins mid-hold and gets its tier after one second from the original queue time. Each deadline opens within one extra second. |
| `retry_failed` | Four successful jobs retain output JSON and downloadable artifacts through three retries. Only the failed job gets an attempt. A second account cancels a running retry, its holder sees cancellation on heartbeat, and the run settles cancelled. |
| `child_cancellation` | A running child is cancelled within the ten-second recovery interval plus two seconds when its parent is cancelled, fails, or its creating job ends while the parent remains live. A manual child retry survives another recovery sweep and succeeds. |

The child scenario uses personal runners. Placement and retry seed pending
runs with GitHub source provenance and use repository-scoped shared runners
on an isolated `high_trust` team. Placement also seeds the default-branch tier
lists. Those runners can publish reusable shared records. Source authentication
and policy fetching are fixture inputs because GitHub verification is outside
these scenarios. Plan acceptance, execution, uploads, retries and
cancellation all use the external controller. Personal runners keep private
result scopes even on a high-trust team, so their results fall back to execution.
No setting on a real team changes. Placement advertises two free runner slots
so the preferred runner remains eligible after returning its provisional award.

Verbose output records run IDs, retry lineage and observed timings. Sparkwing
retains that output in the scenario job's logs. Each controller exits and is
reaped before its private state is removed. Failed tests include its last sixty
log lines. A child scenario can take about ninety seconds because cancellation,
retry survival and terminal settlement each cross controller sweeps.

For a focused Go invocation from another directory:

```sh
GOWORK=off GOMAXPROCS=2 go -C /absolute/path/to/sparkwing test -p 1 \
  -count=1 -v -timeout 10m ./tests/acceptance \
  -run '^TestFeatureE2E$/^retry_failed$'
```
