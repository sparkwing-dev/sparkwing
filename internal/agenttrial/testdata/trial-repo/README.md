# paygate

A small billing API. This is the fixture repository `bin/agent-trial.sh`
copies into a throwaway directory before handing an agent a prompt.

It is deliberately ordinary: a Go service with a few packages, table
tests, SQL migrations, and a Dockerfile. Nothing here mentions
sparkwing, so an agent asked to build pipelines has to discover the
tooling rather than pattern-match on an existing `.sparkwing/`.

The shape exists to give realistic prompts something to bite on:

- several packages, so "shard the tests across N workers" is meaningful
  rather than a single-package no-op
- `migrations/*.sql`, so migration ordering and "migrations must not
  ship with code" rules have real files to check
- a multi-stage `Dockerfile`, so "build an image and test from it" is a
  real build
- a `.golangci.yml`, so lint has a configured entry point

Keep it small. It is a measurement fixture, not a sample application:
every file an agent has to read is time on the clock.
