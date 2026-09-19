# Migrating to the next release

One breaking change, in the Plan() purity guard. A pipeline whose callbacks
thread the context they were handed needs no change. A callback that hands a
guarded helper some other context does, and so does any caller that reaches a
guarded helper with no run around it.

**The compiler will not find this for you.** Removing `planguard.With` and
`planguard.Active` is a compile error, but the grant requirement is a run-time
refusal: a consumer who only ever called `sparkwing.Bash` gets a clean build and
a panic the first time an unconverted call runs. Audit by reading, not by
building, and pay particular attention to cleanup and error paths, which a test
run often never reaches.

## Which helpers are guarded

Every one of these refuses a context carrying no grant:

- `sparkwing.Bash` and `sparkwing.Exec`, at the point the command runs
  (`Run`, `Capture`, `String`, `MustBeEmpty`) rather than where it is built.
- every `sparkwing/git` helper that shells out to git. `FilesetHash` is the
  exception: outside a git tree it hashes the filesystem directly, runs no git
  command, and so is not refused.
- `sparkwing/docker`: `Build`, `BuildAndPush`, `Push`, `Login`, `Run`,
  `BuildxPlatforms`, `FilterBuildxPlatforms`, `ComputeTags`, `ComputeTagsIn`.
- `sparkwing/services`: `WithServices` and `WithServicesAddrs`.

A `*Cmd` reads the context it was built with, so a command built in one place
and run in another is judged by the context at the build site.

## Cleanup that must outlive a cancelled run

Code that deliberately detached from cancellation by passing
`context.Background()` should use `context.WithoutCancel(ctx)` instead. It drops
the deadline and keeps everything else, so the grant, the logger, the node and
step identity, the resource reporter and the run's secrets all survive:

```go
// before: cleanup survived cancellation, and now refuses
cleanup := func() { _, _ = sparkwing.Exec(context.Background(), "docker", "rm", "-f", name).Capture() }

// after
cleanupCtx := context.WithoutCancel(ctx)
cleanup := func() { _, _ = sparkwing.Exec(cleanupCtx, "docker", "rm", "-f", name).Capture() }
```

Reach for `sparkwing.Grant` only where there is genuinely no run context to
carry — a standalone tool, or a test. Granting a fresh context inside a run
passes the guard and loses the logging and resource accounting the run context
carried.

## The pipeline linter gained two rules

`sparkwing pipeline lint` now reports a `Plan` body that calls `sparkwing.Grant`
or `planguard.Grant`, because granting inside `Plan` is how the seal is evaded.
It also treats a `sparkwing/services` call inside `Plan` as plan-time I/O, as it
already did for `sparkwing/docker` and `sparkwing/git`. Both can turn a
consumer's lint red on code this release does not otherwise change.

## The Plan() purity guard runs on a granted permission

The guard used to refuse a context carrying the plan-time mark and allow
everything else. A `Plan` body that ignored the context it was handed and passed
`context.Background()` did state work inside `Plan`, and nothing fired. The
purity test over every registered pipeline stayed green with that hole in place.

A guarded helper now runs only where the runtime granted permission. The
orchestrator grants once per process that executes pipeline work, and
`Registration.Invoke` seals that grant for the `Plan` body.

Two kinds of call change.

- **A callback that mints its own context.** It was silently allowed; it is now
  refused. Thread the context the callback was handed.

```go
// before: ran, inside Plan, unnoticed
sha := sparkwing.Bash(context.Background(), "git rev-parse HEAD").String()

// after: read the SHA in a Job body and hand it downstream through Ref[T]
```

- **A call with no run around it**, such as a tool, a test, or work that happens
  before the orchestrator starts. It was allowed by default; it now needs the
  grant spelled out.

```go
func tidy(ctx context.Context) error {
    _, err := sparkwing.Bash(sparkwing.Grant(ctx), "rm -rf tmp").Run()
    return err
}
```

`sparkwing.Grant` is a grant, not an override: applied to the context a
callback was handed, it leaves a sealed context sealed. It does not reach a
context `Plan` never touched, so a `Plan` body that builds a fresh context and
detaches that still runs; what the guard removes is the silent version of that
evasion.

### Two inspection paths report a refusal as a result

`sparkwing pipeline plan` evaluates `SkipIf` predicates under the seal, and
recovers a panicking predicate by reporting the node as `would_skip`.
`--describe` recovers a panicking `Plan` and degrades to an empty set of args and
risks. Both behaved this way before, for a predicate or a `Plan` body that
threaded the context it was handed; what changes is that one minting its own
context now reaches the same recovery instead of running. A predicate or `Plan`
body that shells out therefore reads as "would skip" or as "no risks" rather
than as an error. Move the work into a Job body.

`planguard.With` is now `planguard.Seal`, which refuses side effects on the
context it returns, and its counterpart `planguard.Grant` grants them. A
process that dispatches pipeline work calls `Grant` once at its root;
`sparkwing.Grant` is the spelling for a tool or a test. `planguard.Active` is
gone with no replacement: a caller calls the helper, and an ungranted context
panics, rather than testing the context first.

A consumer package that opted its own helpers into the guard with
`planguard.Guard(ctx, "yourpkg.Helper")` compiles unchanged, and its helpers
now refuse an ungranted caller exactly as the SDK's own do. Every caller of
such a helper is covered by the two cases above.

A `*Cmd` reads the context it was built with, not the one it runs under, so a
`Cmd` built inside `Plan` and executed in a step is refused at execution and
names `Plan` in the message. Build it in the step.
