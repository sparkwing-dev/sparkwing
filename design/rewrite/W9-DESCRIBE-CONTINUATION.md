# W9 describe diagnostics and working directory

`SDKPlanner.planningError` retained the canonical error code but replaced its
message with generic text. The CLI rendered that message, hiding the class.
Pre-secret failures now say `describe failed: <code> (details withheld: secrets
not yet known)`. Unregistered codes fall back to `sdk_error` because their text
can contain undeclared secrets. Message, hint and details from the original
failure stay withheld. The invariant is that only registered failure classes
escape before secret discovery, independently of arbitrary SDK error text.

`ProtocolSessionConfig` used `source.Entry.Directory`, the pipeline installation
directory, as the SDK cwd. `sparkwing.WorkDir()` therefore returned `.sparkwing`
instead of the acquired checkout root. Describe, plan, eval and attempt startup
now use the acquired root. Explicit entries without an acquired checkout retain
their supplied directory.

The diagnostic property test ran red for every seed before its fix. It now
covers raw errors, timeouts, exits, protocol mismatch and unregistered codes.
Fuzzing passed 19,833 generated cases. The pipe regression builds a real
replacement-pinned Go SDK fixture, acquires this checkout without Git writes,
and requires a root file during discovery. It ran red with `plan_failed` before
the cwd fix. Describe and a fresh plan process now pass without a daemon. Engine
resolution and installation are supplied adapters in that test. The SDK,
source acquisition, environment seal and protocol pipes are real.

The exact external fixture used by the lead was not supplied. The checked-in
native probe creates no fixture and invokes `ci`, which this checkout's own
pipeline does not declare. Directly describing this checkout's pipeline over
pipes first failed in `release-verify` because `.sparkwing/.git` did not exist.
Running it from the checkout root gets past that failure, then fails because
`windows-verify` rejects Linux during discovery. That pipeline issue was not
changed. The original native probe remains unverified, and its exact failure
must not be claimed closed from the minimal fixture's evidence.

Verification passed: whole-tree build and vet, full short suites for nodehost,
source and localcontroller, the real SDK pipe regression, detached helper
entrypoints, SDK alias inspection, embedded documentation drift, commentcheck
against main and whitespace checks. Both new regressions and the describe
timeout test also passed under the race detector. Native daemon acceptance remains with the lead. No archived
test was ported and no new acceptance cell is claimed.

Shared production files touched are `internal/nodehost/sdk_planning.go` and
`protocol_session.go`. This change adds 16 production lines and deletes 4,
net +12. No whole legacy file was deleted. W9-owned production versus main
remains +16,886/-23,807, net -6,921. No foreign contract changed. Documentation
changes are `docs/local-execution.md` and its generated mirror. The preexisting
`.sparkwing/go.sum` diff is untouched. No setting, timeout, retry or API was
added. Git metadata was not written. Scratch is removed after checks join and
the shared cache is retained. Friction was the unavailable exact lead fixture,
blocked sockets and unavailable dependency downloads during a discarded
installer-based test setup.
