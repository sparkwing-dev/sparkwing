# Migrating to the next release

One breaking change, in the Plan() purity guard. A pipeline whose callbacks
thread the context they were handed needs no change. A callback that hands a
guarded helper some other context does, and so does any caller that reaches a
guarded helper with no run around it.

## The Plan() purity guard runs on a granted permission

The guard used to refuse a context carrying the plan-time mark and allow
everything else. A `Plan` body that ignored the context it was handed and passed
`context.Background()` did state work inside `Plan`, and nothing fired. The
purity test over every registered pipeline stayed green with that hole in place.

A guarded helper now runs only where the runtime granted permission. The
orchestrator grants once per process that executes pipeline work, and
`Registration.Invoke` seals that grant for the `Plan` body.

Two kinds of call change, and both are decided by the context the helper is handed.

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
