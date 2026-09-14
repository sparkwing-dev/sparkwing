# Migrating to the next release

Two breaking changes. One affects a machine whose `agent.yaml` still carries
`name` or `coordinators` and anyone running `sparkwing fleet agents enroll`.
The other affects Go code that sized a claim budget with the recommendation
helpers. One schema migration also takes longer than the others on a large
database; it needs no action.

## The claim budget recommendation helpers are removed

**Before:** `controller.RecommendedClaimsPerMinute` (1200) and
`controller.RecommendedClaimsPerMinuteForSlots(slots)` sized a per-runner claim
budget as `1200 x max_concurrent`, on the model that every offer slot spends a
preparation plus an offer per round at the 500ms cadence.

**After:** both are gone. `controller.CompliantClaimPollsPerMinute()` reports
what an empty-queue poll cadence costs one runner in a minute, and
`controller.ClaimPollInterval` is the cadence it is worked from.

**What to do:** replace `RecommendedClaimsPerMinuteForSlots(n)` with a multiple
of `CompliantClaimPollsPerMinute()`. The `cloud` limits profile uses four times
it and `cloud-free` twice; an operator who wants the old headroom can pass
`--claims-per-runner-minute` with any number they like.

```go
// before
budget := controller.RecommendedClaimsPerMinuteForSlots(8) // 9600

// after
budget := controller.CompliantClaimPollsPerMinute() * 4 // 480
```

**Why:** no shipped runner spends a preparation and an offer per slot per
round. A pool runner claims one node at a time whatever its concurrency, so the
old figure was about twenty times what a compliant runner asks for and bounded
nothing. The budget also no longer charges an awarded claim, so a slot count
does not belong in it: what it bounds is empty polling.

**Edge cases:** a controller already running `--claims-per-runner-minute=9600`
keeps that setting; nothing reads the removed constants at runtime. A
controller that took its number from a limits profile gets the new, lower one,
which no compliant runner reaches.

## Enrolled agent configuration is removed

**Before:** `agent.yaml` accepted `name` and `coordinators`, which selected
enrolled mode. The controller refuses that credential on both the claim route
and the offer route, so v0.50.2 made the agent refuse to start and name the
state, and `--allow-enrolled-preview` started the unfinished path anyway.

**After:** both keys are gone from `agent.yaml`, and so are
`--allow-enrolled-preview` and `sparkwing fleet agents enroll`, whose one-time
output was a `coordinators` block. A file that still sets either key fails to
load:

```
parse ~/.config/sparkwing/agent.yaml: enrolled mode has been removed;
delete name and coordinators from agent.yaml to run in claim mode, which
executes work
```

**What to do:** delete `name` and `coordinators`. What remains is claim mode,
the mode that executes work. Set `holder_prefix` to the name you want in the
dashboard; `controller`, `logs`, `token`, `labels`, `max_concurrent`,
`contribution`, `local_admission`, `local_reserve` and the rest keep their
meaning. `sparkwing cluster runners add` and `install/service-install.sh` write
that shape.

**Why:** enrolled mode had no execution behind it in any release. Keeping the
keys meant an operator could still write a config the runner would not run.

**Edge cases:** `sparkwing fleet agents enroll` is removed with the format it
printed. It minted an executor-bound credential and printed it as a
`coordinators` block, which no `agent.yaml` accepts. Add a machine that
executes work with `sparkwing cluster runners add`. `fleet.yaml` keeps its
`executors` list and every reader of it. Write that list by hand, and bind each
entry to a live runner credential with `sparkwing cluster agents enroll
--token-prefix` against a controller serving this machine's state database; a
`--sw-fleet` run refuses to start when the list is empty or an entry has no
binding, naming the file or the executor.
