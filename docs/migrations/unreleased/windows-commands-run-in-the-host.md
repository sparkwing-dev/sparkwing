## Windows commands run in the host

Windows now runs Go and TypeScript SDK commands in the host, using a Job Object
for cancellation and descendant cleanup. Upgrade the host and SDK pins together.
Commands require a bound host service.

`Run` streams masked output to the job log and retains the last 64 KiB of masked
stdout and stderr. Use `Capture` when parsing complete command output. `String`,
`Lines`, `JSON` and `MustBeEmpty` (and their TypeScript equivalents) already capture.

For Windows Go commands, `ExecError.Cause` no longer wraps `*exec.ExitError`.
Use `errors.As` to read `*sparkwing.ExecError`, then inspect `ExitCode` and
`Terminated`. Deadline causes remain available through `errors.Is`.
