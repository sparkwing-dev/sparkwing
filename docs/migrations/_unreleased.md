# Migrating to the next release

One breaking change so far, and it affects only a machine whose
`agent.yaml` carries `name` or `coordinators`.

## Enrolled agent configuration refuses to start

**Before:** `sparkwing-runner agent` accepted an `agent.yaml` that set `name`
or `coordinators`, entered enrolled mode, and polled its coordinators. The
controller refuses that credential on both the claim route and the offer route,
so the agent claimed nothing and logged an error on every slot.

**After:** the agent refuses that configuration, prints one message naming the
state, and exits non-zero:

```
enrolled execution is not available; remove name and coordinators from
agent.yaml to run in claim mode, or pass --allow-enrolled-preview to start
the unfinished enrolled path
```

**What to do:** remove `name` and `coordinators` from `agent.yaml`. What
remains is claim mode, which executes work. Set `holder_prefix` to the name you
want in the dashboard; `labels`, `max_concurrent`, `contribution`,
`local_admission` and `local_reserve` keep their meaning. `sparkwing cluster
runners add` writes that shape for you.

**Why:** a machine in enrolled mode ran a supervised loop that could never
claim work. Failing at startup, once, says so; polling and logging an error
every slot did not.

**Edge cases:** `--allow-enrolled-preview` starts the unfinished enrolled path
unchanged, for the developers of that path. `sparkwing fleet agents enroll`
still prints a `coordinators` membership, so a config merged from its output
needs that flag until enrolled execution ships.
