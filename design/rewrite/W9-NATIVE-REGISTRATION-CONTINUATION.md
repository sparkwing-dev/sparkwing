# W9 native registration continuation

Base HEAD: `c258ff26e`. The latest lead-native report, dated October 17 at
08:58 MDT, contains FAIL entries. This session addresses its remaining W9
failures and preserves the four structural fixes from the previous session.

## Changes

- Resolved C3-controller-wire and C4 conflict contents by retaining both W9's
  environment/source/entrypoint amendments and the integration branch's launcher
  amendments. No conflict markers remain. No integration deletion was restored.
  The brief prohibits worker Git writes, so the lead must stage these two files
  and finish the merge.
- Migrated the repository's 26 configured SDK registrations from their CLI
  aliases to their configured entrypoints. The shared planner and schema binding
  retain their entrypoint rule. CLI aliases and author run context are unchanged.
  `TestEveryDeclaredPipelineResolvesToItsConfiguredEntrypoint` failed on all 26
  entrypoints before this caller migration and passes afterward.
- Corrected the trigger schema fixture's unrelated registration to `deploy-prod`.
  It previously declared `Deploy` twice and therefore selected the first schema.
  The fixture still proves that the alias's schema cannot replace `Deploy`.
- Gave the tracking-sparks fixture a real repository and SDK module. It now
  reaches dependency resolution before SDK installation and verifies the existing
  `--sw-no-update` guidance rather than failing in Git acquisition.
- Adapted the integration branch's operator-team test to the retained parsed
  invocation adapter. The test package initially failed to compile because that
  call still passed raw arguments to the changed handler.
- Updated Windows verification to select the current probe test name and the
  retained platform-neutral run-handle test. The old Windows-specific file stays
  deleted. The fixture-selection check failed before these changes and passes.

The merge already supplies the controller/profile fixture corrections: real
local-controller intake now passes for unborn, SHA-1 and SHA-256 repositories,
and profile-cache construction uses its committed source SHA. Those were
reverified without repeating their implementation.

The invariants remain: children inherit environment provenance independently of
origin; SDK selection uses the configured entrypoint; both source-selection flags
govern acquisition and the seal; migration destinations parse under the current
grammar. No additional contract shape or product behavior was introduced.

## Verification

Commands used `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`,
`GOMAXPROCS=4`, `go -C`, at most `-p 4`, and temporary directories beneath
`/tmp/sw-astra-W9`. Focused test failures were reproduced before changing their
callers or fixtures.

| Check | Result |
| --- | --- |
| Whole-repository `go build -p 4 ./...` | Pass |
| Vet: cmd/sparkwing, clisurface, controllerwire, controller/client, pipeline jobs | Pass |
| Focused CLI alias, overflow, priority, controller/profile, operator and OLD/HEAD source regressions | Pass, 26.741s |
| Native here-trigger inspection/intake, trigger preparation, retained build policy, scaffold and registered-name CLI tests | Pass, 43.111s |
| Real NativeProcessClient child intake with nonempty Env, descendants and adversarial seal changes | Pass, 0.840s |
| Shared initial-plan and attempt-replan identity tests | Pass |
| Full clisurface, controller/client, doccheck, nodeproto, runspec and source suites | Pass |
| Surface and EngineAmendments acceptance cells | Pass |
| Pipeline registration, Kubernetes plan, EscapeProbe and all WindowsVerify tests | Pass, 0.032s |
| Full doccheck | Pass; 453 documented invocations, zero unresolved |
| Commentcheck against main, gofmt and diff whitespace | Pass |

No acceptance cell was newly unskipped, and no archived test was newly ported.
The previous session's native trigger/overflow, real child intake and split-source
proofs remain in place and pass on this merge. Hook enforcement and the full
native CLI/bin suites remain lead-owned; no Sparkwing pipeline or installed CLI
was run by this worker beyond the required read-only orientation.

An exploratory broader Kubernetes test selection failed before script execution
because its existing helper looks for `.git` in `.sparkwing/jobs`. Those fixture
failures remain open outside this W9 slice. They are separate from the passing
Kubernetes registration/plan test. Native Windows/Darwin, deployed Cloud/DinD,
remote split-source execution and PostgreSQL remain real-integration evidence for
the lead. No new fake implementation or production stub was added.

## Files, contracts and accounting

W9 files changed this session are `cmd/sparkwing/local_describe_unix_test.go`,
`operator_team_test.go` and `trigger_prepare_test.go`, plus the two contract
conflict resolutions and this handoff. Lead-owned amendments and handoffs are
unchanged. Earlier W9 amendment implementations were retained.

Shared files changed under `.sparkwing/jobs` are:

- Production registrations: admission_stress, build, build_images, escape_probe,
  example, gate, integration, jev_lint, jev_sweeper, k8s_e2e, lint, pre_commit,
  pre_push, pre_release, release, security_scan, static_analysis, store_postgres,
  template_verify, test and weather.
- Corresponding registration callers/checks: escape_probe_test, k8s_e2e_test and
  pre_commit_test.
- Windows verification: windows_verify.go and windows_verify_test.go.

W9 production Go versus main is +19,726/-25,174, net -5,448 lines, within the
-2,900 budget. This session changes no W9 production Go. Shared pipeline production
changes replace 27 lines with 27 lines, net zero: 26 alias registrations and one
stale Windows selector. No production file was added or deleted. No API was
removed, and no contract change is needed in another area. There is no new
user-facing documentation impact because these are repository pipeline callers
and test fixtures for the already documented behavior.

The remaining handoff is to stage the resolved contracts, finish the merge,
review/commit and rerun the lead-native harness. Completion cannot be claimed
while its latest report contains FAIL. Overwing reports an outdated installed
binary but its standards discovery worked. The worker made no Git metadata or
harness-memory writes. All checks joined, scratch is removed, and the shared Go
cache is retained.

STATUS: CONTINUE
