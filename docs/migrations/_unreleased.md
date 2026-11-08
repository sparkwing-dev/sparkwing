# Migrating to the next release

## Parse command output with Capture

Hosts on Linux, macOS and Windows run SDK commands themselves. A command run with `Run`
writes its output straight to the masked job log, and its result keeps only the last
64 KiB of masked stdout and stderr, enough for error messages. Masking happens
before truncation so a secret crossing the cut cannot leak a suffix. Code that reads
`Run`'s `Stdout` or `stdout` must switch to `Capture`, which returns every byte and
writes nothing to the log:

```go
res, err := sparkwing.Exec(ctx, "go", "list", "./...").Capture()
```

```ts
const res = await ctx.exec(["go", "list", "./..."]).capture();
```

`String`, `Lines`, `JSON` and `MustBeEmpty` (`string()`, `lines()`, `json()` and
`mustBeEmpty()`) already capture and need no change.

For host-run Go commands, `ExecError.Cause` no longer wraps `*exec.ExitError`.
Use `errors.As` to read `*sparkwing.ExecError`, then inspect `ExitCode` and
`Terminated`. Deadline causes remain available through `errors.Is`.

