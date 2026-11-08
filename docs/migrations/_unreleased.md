# Migrating to the next release

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

## Bound object presence requests

`POST /api/v1/data/has-many` requires a nonzero RFC3339 `deadline` alongside
`digests`. Send the caller's query deadline; a missing deadline returns `400`.
Both Go data clients require an explicit presence budget. The server stops
presence work at the earlier of the caller's deadline and its 30-second response
window. Requests that expire or are cancelled return an error without partial
presence results.

## Upgrade the store in place to schema baseline 37

Schemas 30 through 36 upgrade in place. Schema 37 drops the unused team
build-trust table and preserves secrets, teams, tokens, runner registrations,
GitHub App installations, schedules, subscriptions and Actions records, including
both direct team foreign keys. The `/api/v1/team/build-trust` route, including
its GET and PUT operations, is removed. Remove calls to this endpoint. Execution
trust still follows sealed provenance and host facts. Older binaries refuse the
upgraded store.
