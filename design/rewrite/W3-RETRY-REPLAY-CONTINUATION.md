# W3 retry replay continuation

The retry-selection implementation and required mode, alias and restart proofs
were already committed. No further production change was needed in this session.

Extended `TestRetryExecutesCommittedSelectionAfterRestart` to replay admission
before starting the new owner. It checks that replay returns the same execution
and preserves the committed mode and exact source-result fences. In its late
success case, the original source finishes between admission and replay. The
retry still executes the job that was unsuccessful at admission. Both `failed`
and `all` retain their existing executed-job and attempt-count assertions.

## Native blocker for the lead

The latest native report passes store and nodehost but fails four GitHub App
tests at connection start, before retry admission. A temporary socket-free probe
reproduced the same failure through the real router and SQLite:

```text
--- FAIL: TestW3ProbeDefaultGitHubConnection
    connect start = 500: {"error":{"code":"policy_missing","message":"request failed"}}
```

The probe used `newGitHubBrowserFixture`, cleared its injected
`resolveGitHubConnectWriter`, then posted its valid session and CSRF token to
`/github/app/connect`. It has been deleted. The default resolver in
`pkg/controller/browser_github_app_routes.go` requires the tenant to implement
`GitHubConnectWriter`. `pkg/store` implements none of its three authority-bearing
methods: `LinkedGitHubSubject`, `ConsumeGitHubConnect`, and
`BindAuthorizedGitHubInstallation`. C2 already declares their transaction rules
under its browser GitHub App contract. C1/C2 must implement that contract. This
is outside the assigned retry slice. No fallback or fake was added to hide it.

Native HTTP execution with real SDK bodies and placement, and PostgreSQL
execution of the prior retrier-kind query, remain with the lead harness. The
native report still contains FAIL entries, so W3 remains CONTINUE.

## Race finding

The focused race run failed the `all` retry with SQLite `interrupted (9)`.
Narrowing to `TestRetryExecutesCommittedSelectionAfterRestart/all` with
`-race -count=20` reproduced it twice while completing the original source run,
before the new replay assertions. These are failures, not passing race evidence.
The store's committed-selection and retrier-identity race properties passed.
An overlay restoring the committed test reproduced the same source-completion
failure four times in 20 attempts (29.693 s). This predates the replay-proof
extension. The test command was `go test -p 4 -race ./internal/nodehost -run
'^TestRetryExecutesCommittedSelectionAfterRestart/all$' -count=20 -timeout=90s`,
with `-overlay` added for the baseline comparison. Both failing outputs remain
in the session log. No timeout was raised and no error was ignored.

Open finding for C1/W3: requested follower cancellation must not turn successful
job completion into an owner failure, while independent read and subscription
errors must remain visible. `OwnerHost.wait` cancels and joins its state follower
after receiving work completion. `StateFollower` preserves read errors and
`waitFailure` removes context cancellation, but a raw SQLite interruption remains
an error. Whether this accounts for the observed interruption still needs a
targeted trace. No cancellation mechanism was changed in the retry slice.

## Scope and evidence

Changed only `internal/nodehost/retry_selection_test.go` and this report.
No shared production file, API, contract, handoff or amendment changed. No new
acceptance matrix cell was enabled and no archived test was newly ported.
No policy question is open. No mechanism, limit or environment variable was
added. Production deletions this session: zero. Earlier legacy host cuts remain
deleted. Session production delta: zero. W3 production versus main is
**+11,781/-26,047, net -14,266**, using the lead's budget exclusions.

Passed: whole-repository build, focused vet, the tagged retry-mode regression,
controller alias/seal/rate-limit proofs, the full nodehost suite (31.092 s), and
commentcheck against main. Go checks use `GOWORK=off`,
`GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, `GOCACHE=/tmp/sw-gocache`, temporary
paths below `/tmp/sw-astra-W3`, and at most four package workers.

All checks joined before scratch removal. `/tmp/sw-astra-W3` was removed and
`/tmp/sw-gocache` was retained. No Git writes ran and the repository has no local
Git identity or `core.worktree` override. Friction was the missing C1 adapter and
the reproduced scheduler interruption.

STATUS: CONTINUE
