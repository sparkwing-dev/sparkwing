# Agent trial fixture

`testdata/trial-repo` is the repository `bin/agent-trial.sh` copies into
a throwaway directory before handing an agent a prompt. This file
explains it; the fixture itself must not, which is the point below.

## The fixture is a stage set, not documentation

Everything inside `testdata/trial-repo` is read by the agent under
measurement, so anything in there that mentions sparkwing hands it the
answer. An earlier version of the fixture README explained that it was a
fixture and that "nothing here mentions sparkwing" -- and the trial
agent's first observation was that the README hinted at a tool called
sparkwing. It had skipped discovery entirely.

Keep `testdata/trial-repo` reading like an ordinary project: no
sparkwing, no CI, no commentary about being a test.

## Why this shape

- **several packages** so "shard the tests across N workers" is real
  work rather than a single-package no-op
- **`migrations/*.sql`** so migration ordering, and rules about shipping
  schema changes with code, have real files to check
- **a multi-stage `Dockerfile`** so "build an image and test from it" is
  a real build
- **a `.golangci.yml`** so lint has a configured entry point

Keep it small. It is a measurement fixture, not a sample application:
every file the agent has to read is time on the clock.

## The prompt has to name sparkwing

`testdata/prompts` holds the committed trial prompts, and each one says
"use sparkwing" on purpose.

Without it the agent has no reason to believe sparkwing exists: the
fixture is an ordinary repo, and the obvious way to add CI to an
ordinary repo is `.github/workflows`. A trial run against a prompt that
did not name the tool produced seven GitHub Actions workflows and never
invoked the CLI once. Earlier runs only found sparkwing because the
fixture README leaked the name -- so every number from before that leak
was fixed came from an accident.

This matches how the flow really starts: someone who has installed
sparkwing tells their agent to use it. Discovering the tool unprompted
is a different question, worth asking separately, but it is not what
these trials measure.

## Running more than one trial

One agent's result is one agent's habits. `bin/agent-trial-matrix.sh`
runs the quickstart prompt once per locally available configuration --
three Claude models, three Codex reasoning efforts -- and prints one
table, so a CLI change can be judged against more than a single set of
reading habits. The trials are serialized: they contend for CPU, and a
contended run has been measured at six times its uncontended time.

`bin/agent-trial-report.sh <trace.jsonl>...` reads the traces those runs
leave in `$TMPDIR/sparkwing-agent-trial/` and splits wall-clock into
model time and tool time, then attributes calls to the phases of
authoring a pipeline. Tool time is a small fraction of the total, which
is the standing argument for spending CLI design effort on removing
round-trips rather than on making any single command faster.

## The other fixture

`agent-trial.sh --fixture miniflux` runs against a pinned commit of a
real upstream project instead. That one ships its own CI, so it measures
translating an existing setup rather than authoring from a description.
The two numbers are not comparable.
