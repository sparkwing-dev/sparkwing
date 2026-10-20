# W3 retry native continuation

Continued from `8aa3ea61e` and the lead's native report. Committed retry selection,
alias replay and restart consumption were already implemented. Those proofs remain
green, including `TestReviewRetryModeIsPartOfCommittedReplay`.

The remaining retry tests used removed raw trigger documents, empty retry bodies,
reverse links or stale credential fixtures. The hourly-limit test now uses the
authenticated controller client with an explicit mode and key. It admits one
retry, replays it without spending a slot, then refuses a second key without
creating an execution. The plaintext and team-boundary tests are folded into the
existing sealed-input and generated authority proofs. Public source and retry
reads now also prove secret redaction before and after plan acceptance. The
fixture supplies its selected engine and the existing version-policy double.

This port exposed a committing rate-limit defect. Invariant: a retry consumes the
authenticated retrier's subject-kind/ID budget independently of the source actor.
`FuzzRetryRunRateUsesRetrierIdentity` failed before the fix for both cross-kind
cases. The query combined the destination's principal ID with the immutable
source's actor kind. It now reads retry kind from committed retrier lineage.
SQLite and PostgreSQL expressions changed in place. No new mechanism was added.

Deleted 133 obsolete test lines. Total test delta is **+88/-134, net -46**.
Production delta is **+2/-2, net 0**, entirely in shared store code. W3-owned
production versus main is **+11,769/-26,032, net -14,263**, using the lead's
exclusions for tests, testdata, acceptance and contract declaration files. Earlier
legacy hosting cuts remain deleted. No API or production caller migration was
needed.

The port inventory now records these three ports and the previously completed
failed/all proof. No acceptance cell was newly enabled. C1's contract clarifies
the existing retrier budget rule. No type, wire or schema contract changed, and
no further retry-selection contract decision is needed. Lead-owned handoffs and
amendments were not edited. The accepted cross-host masking residual remains
closed by the lead's decision.

Shared files touched are `internal/store/compute_limits.go`, `intake_rate_test.go`,
`internal/controller/retry_action_native_test.go`, `compute_limits_test.go`,
`secret_args_display_test.go`, `retry_team_test.go`, and the C1 contract and port
inventory. No W3 production file changed.

Verification passed:

- Whole-repository build and vet of store, controller, controller client and host.
- Tagged retry-mode regression, retry action/alias proofs, authority/validation
  seeds and the ported controller rate-limit proof.
- Store rate-limit properties and 120 generated fuzz executions.
- Focused race proofs in controller, store and host, including committed
  selection after restart.
- Full host and controller-client suites (26.448 s and 0.091 s).
- Commentcheck against main and whitespace checks.

The full store suite failed after 250.280 s in
`TestGitHubAppAutomationConsentIsolationAndRevocation`: consent was enabled with
no error, but its timestamp preceded the test's `time.Now()` baseline. This is
outside the changed retry path. The unchanged failing check was not rerun.

Native completion stays with the lead. The latest native file still contains
FAIL entries. Its four GitHub App tests fail at connection start with HTTP 500,
before retry admission, and remain outside this slice. Socket-dependent execution,
PostgreSQL execution of the changed query, real SDK bodies and placement remain
native integration points. The local attempt at the old native-fixture selection
panicked at the sandbox TCP bind, as before. The in-process authenticated router
proofs above require no socket.

All Go checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-gocache`, and temporary paths below `/tmp/sw-astra-W3`, with at
most four package workers. No Git writes, operational Sparkwing command, pipeline,
installation, deployment or release ran. Friction was socket refusal, stale
native fixtures and Overwing's reported source lag. No new policy question is open.
All checks joined before removing `/tmp/sw-astra-W3`. The shared build cache is
retained. Git has no local identity or `core.worktree` override.

STATUS: CONTINUE
