# W1 continuation 41

Starts at 87cc6713c. W1 remains incomplete. The lead owns review, commits,
pipelines and landing. No W1b production file or materialization path changed.

Removed Store and Tenant RequestCancel and CancelPendingTrigger. Removed the
separate requestControllerRunCancel writer. TransitionRun with RunCancel owns
cancellation intent, and the elected unit reaper completes cancelled unclaimed
owner units. Live holders retain result rights. Removed the unused
fenceSensitiveClaimTx and fenceSensitiveTriggerClaimTx helpers. Removed FilterRuns,
runMatches and the in-memory repository helper because no production caller
remains. SQL run selection and counting retain their implementations and checks.

Production Go delta, excluding tests, testdata, acceptance and contract files,
is 1 added, 337 deleted, net -336. W1+W1b is +2,481 against main, versus the
-4,250 target. It remains 6,731 lines over budget. No waiver is requested.

Verification on the final source passes:

- Whole-repository go build -p 4 ./.... Scoped go vet -p 4 on internal/store/...,
  internal/controller/... and internal/orchestrator/.... All Go checks use GOWORK=off,
  GOFLAGS=-buildvcs=false, GOMAXPROCS=4, GOCACHE=/tmp/sw-gocache and
  GOTMPDIR/TMPDIR under /tmp/sw-astra-W1.
- 156 store tests / 680 cases and 11 controller tests / 14 cases pass without
  skips. w1-session41-tests.txt records the reproducible package selectors.
  Existing SQLite plan, transition, matching, launcher, credential and recovery
  acceptance proofs remain green. No native cell is newly activated.
- The five new cancellation tests cover unclaimed trigger/plan units, live
  trigger and exchanged plan holders, cancel-versus-claim races, terminal
  outcomes, and unavailable tenant/session/membership authority. Refusals leave
  run state, events and completion effects unchanged. The generated race varies
  named versus queued claims and cancellation reasons. Existing generated
  holder-result and cancellation-intent proofs remain in the passing selection.
- Store, controller and orchestrator test packages compile with -run '^$'.
  commentcheck -base main is clean across 2,192 files. sleepcheck -base HEAD,
  the store API snapshot, documentation-symbol test, changelog check,
  embedded documentation drift, gofmt and whitespace checks pass.

Ports and limits:

- The three old RequestCancel tests move from raw trigger writers to
  TransitionRun, ClaimUnit, RenewUnit and ReapUnits. The terminal overwrite
  assertion from trigger_absorbing_test.go becomes a generated final-state test.
  The foreign-tenant raw cancellation assertions are replaced by the current
  authority property. The missing-message test drops the retired API case.
- The FilterRuns comparisons are deleted. Existing expected SQL filter, count,
  literal, case and cursor assertions remain. A selected SQL fixture fails on
  CreateRun's missing runs.args_json column. Both changed submission-environment
  cleanup tests fail on CreateTrigger's missing triggers.pipeline column.
  An overlay restoring every changed Go file from HEAD reproduces all three
  failures. These legacy suites are not represented as passing.
- Legacy transport tests seed cancellation intent directly because their subject
  is the response to committed state. Submission-environment and old credit
  fixtures use the existing FinishTrigger solely to mark cleanup work terminal.

Real integrations and remaining work:

- C2 ProvisionSpec still has no seeded bootstrap presentation on this branch.
  Implement the one-use Store.Issue input after W2's declaration arrives, as
  already directed by the lead. No alternate issuer or foreign contract edit
  is introduced. This remains the only foreign declaration needed by this cut.
- Console/verified-local authority, console service issuance, SetRunnerMetered,
  host/source observations and production pool-rule composition remain W1 work.
- StateStoreOld, raw lifecycle/readiness, old claim/trigger/token writers,
  deletion admission/account anonymization and legacy GitHub automation intake
  remain. Active host callers still use several of these paths. Their migration
  remains required, alongside the old-schema fixture migration above.
- Rotation grace, the launcher store fence, the five Operator credit methods,
  the W1 store harness moves and six dead contract table cuts remain complete.
- Materialization belongs to W1b end to end. This session makes no claim about
  its byte-copy integration. PostgreSQL, native placements, api.sock,
  Kubernetes, provider billing and repository pipelines remain lead/owner
  integration evidence.

Foreign caller migrations are test-only:

- internal/orchestrator/submission_environment_test.go
- internal/orchestrator/trigger_consumer_test.go
- internal/orchestrator/trigger_host_test.go
- internal/controller/claim_route_internal_test.go
- internal/controller/claim_run_test.go

Store test callers changed in claim_tokens_test.go, not_found_message_test.go,
run_filter_display_test.go, tenant_node_state_test.go, trigger_absorbing_test.go
and trigger_claim_credits_test.go. The last is W1b's fixture and changes only its
removed-API caller. cancel_unclaimed_test.go now exercises the final contracts.
Shared artifacts are C1, the port inventory, .apidiff/pkg_store.txt, CHANGELOG.md,
the migration guide and generated mirrors. Lead-owned handoffs/amendments are
unchanged. No shared production caller changed.

Friction is the retained old-schema fixtures and remaining host caller cutovers.
There is no new product decision or contract redesign request. No Git write,
Sparkwing binary, pipeline, service, deployment, release, external message or
harness-memory write ran. Git config has no user identity or core.worktree
override. Assigned scratch is deleted after final verification. The shared
build cache is retained.

STATUS: CONTINUE
