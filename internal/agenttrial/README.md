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

## The other fixture

`agent-trial.sh --fixture miniflux` runs against a pinned commit of a
real upstream project instead. That one ships its own CI, so it measures
translating an existing setup rather than authoring from a description.
The two numbers are not comparable.
