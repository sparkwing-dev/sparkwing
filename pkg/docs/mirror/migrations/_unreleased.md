# Migrating to the next release

One breaking change, which affects a machine whose `agent.yaml` still carries
`name` or `coordinators` and anyone running `sparkwing fleet agents enroll`.
One schema migration also takes longer than the others on a large database; it
needs no action.

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
executes work with `sparkwing cluster runners add`. A `--sw-fleet` run whose
`fleet.yaml` lists no executors still refuses to start, naming the file.
