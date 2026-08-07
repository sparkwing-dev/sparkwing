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

## Running a trial

Each scenario is a committed prompt plus a fixture. Nothing here needs
arguments beyond these; the defaults are the measured configuration.

| what you want to measure | command |
|---|---|
| simple pipeline, from a description | `bash bin/agent-trial.sh --prompt-file internal/agenttrial/testdata/prompts/quickstart.txt` |
| a PR gate with lint + test | `bash bin/agent-trial.sh --prompt-file internal/agenttrial/testdata/prompts/lint-and-pr-gate.txt` |
| complex: a five-pipeline CI/CD set | `bash bin/agent-trial.sh --prompt-file internal/agenttrial/testdata/prompts/full-cicd.txt` |
| migrating existing GitHub Actions CI | `bash bin/agent-trial.sh --prompt-file internal/agenttrial/testdata/prompts/migrate.txt --fixture migrate` |
| migration at real-world scale | `bash bin/agent-trial.sh --prompt-file internal/agenttrial/testdata/prompts/migrate.txt --fixture miniflux` |

Add `--agent codex`, `--model sonnet`, or `--effort high` to vary the
harness. `--name <label>` keeps a run's artifacts from overwriting the
last one's.

**Install the CLI you mean to measure first.** The trial runs whatever
`sparkwing` is on PATH, so `bash bin/install.sh` before a trial, or the
numbers describe the last release rather than the working tree. This has
silently invalidated a sweep before.

Read the number labelled `time-to-green`, not `wall-clock`. The prompts
ask the agent to close with a FRICTION report, and writing it costs
~12s that no real user waits for.

## Running more than one trial

One agent's result is one agent's habits. `bin/agent-trial-matrix.sh`
runs the quickstart prompt once per locally available configuration --
three Claude models, three Codex reasoning efforts -- and prints one
table, so a CLI change can be judged against more than a single set of
reading habits. Pass `--prompt-file` / `--fixture` to sweep a different
scenario. The trials are serialized: they contend for CPU, and a
contended run has been measured at six times its uncontended time.

`bin/agent-trial-report.sh <trace.jsonl>...` reads the traces those runs
leave in `$TMPDIR/sparkwing-agent-trial/` and splits wall-clock into
model time and tool time, then attributes calls to the phases of
authoring a pipeline. Tool time is a small fraction of the total, which
is the standing argument for spending CLI design effort on removing
round-trips rather than on making any single command faster.

## Reading a result

The wall-clock says whether the flow is fast enough. The ORIENTATION
PATH says why it wasn't, and the FRICTION section says what the agent
had to guess at -- that one has been the most useful of the three, and
every ticket in the GitHub Actions parity group came out of it.

Two things worth checking before trusting a number:

- **Did the agent stay in the trial repo?** The report says so
  explicitly. Agents have been observed wandering into other checkouts
  on this machine, and work done there is not being measured.
- **Does lint+explain green actually mean the prompt was satisfied?** It
  does not. The oracles check that a pipeline compiles and is
  well-formed, not that it does what was asked. One trial scored clean
  having produced a pipeline with no trigger at all.

## The fixtures

`testdata/trial-repo` is the default: small, offline, no existing CI.

`--fixture migrate` overlays `testdata/gha-overlay` onto that same tree,
so authoring and translating differ in exactly one thing. The workflow
in it is deliberately ordinary and deliberately hard -- a build matrix,
a postgres service, `actions/cache`, job-level `needs` and `if:`, an
artifact upload, five third-party actions. Those are the parts with no
one-to-one sparkwing equivalent, which is the point of measuring it.

`--fixture miniflux` runs against a pinned commit of a real upstream
project, which ships ten workflows nobody wrote for this test.

The three measure different jobs. Do not compare their numbers.
