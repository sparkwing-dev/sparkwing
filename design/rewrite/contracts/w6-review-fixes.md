# W6 review corrections

All five major findings and both minor findings are fixed. This report supersedes
the credential-directory handoff in `w6-implementation.md`: the existing runner
configuration loader now reads the private credential file.

## Findings and regression evidence

| Finding | Status | Evidence |
| --- | --- | --- |
| 1. Secret classification shrinks at the second seal | Fixed | `TestSecretClassificationCannotShrinkAtSecondSeal` first exposed plaintext through display and reproduction. SealSpec now refuses a shrinking classification, and display fails closed on inconsistent input. The acceptance cell uses separate sealed public and secret fixtures instead of expecting plaintext after mutation. |
| 2. Proxy credentials evade masking | Fixed | `TestProxyCredentialsMaskedInConsumerSpellings` first exposed scheme-less HTTP proxy passwords, dependency proxy userinfo and escaped passwords. Local and remote builds now register decoded and original credentials, including Go proxy lists and pip extra indexes. |
| 3. YAML aliases cause exponential validation | Fixed | `TestNestedAliasGraphCannotExhaustValidation` timed out after three seconds inside schema validation on a 30-level merge graph. Validation now visits each node/type pair once and reaches yaml.v3's excessive-alias refusal. `TestAliasValidationUsesEachDestinationSchema` preserves unknown-field checks across types. |
| 4. Enrollment writes rejected configuration | Fixed at the configuration boundary | `TestRunnerEnrollment*` first failed on token/allow_repos fields, including the service test failing before installation. Enrollment now validates the final schema, writes repos and credentials_dir, and stores agent-token privately. `TestLoadReadsPrivateRunnerCredential` proves the runner consumes it and refuses a missing file. Service and bad-pattern integration assertions now require their named failure. The integration fixture limitation is below. |
| 5. Admission inference disappears | Fixed | `TestAdmissionClassInferenceAtPlanSeal` first refused a schedule restricted to batch. Acceptance derives the fallback from the sealed origin. Explicit code and request values still win, and intake stays unchanged. |
| 6. Guard lint uses removed vocabulary | Fixed | `TestGuardMisuse_LocalAndControllerBothRequired` first returned zero findings for final YAML containing both where predicates. It now parses project YAML before linting and reports the contradiction. Other guard fixtures use where tokens. |
| 7. Competing configuration parsers | Fixed | The cache parser first accepted NaN, infinity and overflow in its existing test. Cache and CLI callers now use userconfig.ParseByteSize. Budget consumers use userconfig.Budget and ParseBudget directly. Both duplicate implementations are deleted without aliases. |

The red commands used `go test -p 4` with each test name above. The YAML red
command additionally used `-timeout 3s`; no production timeout or size cap was
added.

## Verification

Go checks use `GOWORK=off GOFLAGS=-buildvcs=false`, with `GOCACHE` and `GOTMPDIR`
under `/tmp/sw-astra-W6/`. Commands below run in the assigned worktree.

- Whole repository: `go build -p 4 ./...` passed.
- Full package tests passed for runspec, procenv, settings, limits, userconfig,
  projectconfig, agentconfig, pipelines and pipelinelint.
- `go test -p 4 ./tests/acceptance -run '/.*/runspec-config$' -count=1` passed
  all 16 W6 cells. These are library boundary proofs, not placement execution.
- Focused runner enrollment, budget resolution, cache configuration and YAML
  documentation tests passed.
- `go vet -p 4` passed for W6 and the affected CLI, agentconfig, bincache,
  cluster, orchestrator, wingd and pipelinelint packages.
- Commentcheck and `git diff --check` passed.
- Lint across the affected packages reported only three unchanged panic stubs in
  `internal/wingd/lifecycle_contract.go` (lines 32, 64 and 152), owned by W8.
- W6-only lint passed with zero issues.

## Deletions and ports

Removed 226 lines of duplicate implementation: 183 lines of wingd budget types,
methods and parsing, plus 43 lines of bincache byte parsing. Budget form, bound
and nonfinite tests moved to userconfig. Byte-size form tests moved there with
the new invalid-number cases. Cgroup-format tests remain with wingd.

## Contracts and assumptions

- Local C4 addition: `settings.Context.Origin` carries the server-owned sealed
  request origin into admission fallback resolution. No other area's frozen
  contract needs changing.
- Enrollment uses a new private credential directory per replacement because
  replacing a shared credential file first could change the old configuration
  when the YAML write fails. The simpler shared-file overwrite was rejected for
  that reason. Removing this choice requires a config-plus-credential atomic
  replacement contract. Credentials remain readable if config replacement
  reports an error after rename.
- YAML's visited-pair set exists only for one decode. It prevents repeated graph
  traversal without introducing a new customer-visible refusal limit.
- No new flags, environment variables, services or compatibility aliases were
  added. Open questions: none.

## Shared files

- CLI: `cmd/sparkwing/cache.go`, `runners.go`, `runners_test.go`,
  `runner_enrollment_test.go`.
- Acceptance: `internal/acceptance/settings.go`.
- Runner config: `internal/agentconfig/agentconfig.go`, `agentconfig_test.go`.
- Cache parsing: `internal/bincache/prune.go`, `prune_test.go`.
- Budget consumers: `internal/cluster/headroom.go`,
  `internal/orchestrator/wingd_daemon.go`.
- Wingd: `budget.go`, `budget_nonfinite_test.go` (moved), `budget_resolve.go`,
  `budget_resolve_test.go`, `budget_startup_test.go`, `budget_test.go`,
  `budget_visibility_test.go`, `journal_events.go`, `queuestate_test.go`,
  `restore_reconcile_test.go`, `unit_test.go`, `wingd.go`.
- Lint: `internal/pipelinelint/lint.go`, `lint_test.go`.
- Contracts: `c1-data.md`, `c4-surface-config.md`, and this report.

## Friction and unverified integration

The three controller-backed runner regression tests stop while their fixture
creates its admin token: `SQL logic error: no such table: tokens`. That fixture
still uses the removed store API. Its migration belongs with the CLI/controller
credential cutover. The focused enrollment tests cover validation, persistence,
private credential loading, forced replacement and the service failure without
that fixture. They do not claim token issuance or revocation coverage.

No Sparkwing binary, real service manager, native Windows runtime, or deployment
pipeline was run. No Git command wrote repository state. The lead owns the
integration gates and landing.

All check processes exited. `/tmp/sw-astra-W6/` was removed after verification.
