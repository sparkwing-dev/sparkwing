# W4 continuation report

This continues commit `7433ccc12`. No git writes, Sparkwing binaries, pipelines or live services were run.

## Implemented

- Restored the whole-repository build by migrating callers of removed SDK and v1 protocol APIs. Host callers now use hello, flat jobs, structured errors and argv handles. No retired SDK exports or wire aliases were restored.
- Protocol decoding refuses nested null values, duplicate fields and case aliases that Go's JSON decoder would otherwise accept. Partial frame writes fail, and context cancellation closes an idle protocol reader.
- Validation permits different weighted costs in one concurrency group and rejects expansion cycles through generated-group settlement barriers.
- A process bound to a generated job can replay nested ancestor generators before preparing the target body environment. The target bearer remains unchanged. AfterRun consumes the final structured RunResult rather than silently receiving nil.
- The fake host refuses unsupported cleanup execution before ambient environment can select a real ledger. Its summary attributes use the same masked, bounded text as the message. Reference tests now use the error-returning resolver, and Secret names Variable in its guidance.
- Added native Unix/Windows pipe fixtures and Windows support in the Go acceptance process driver. Windows binaries compile here; native Windows execution still needs a Windows host.

## Deletions

Forty files were fully deleted, totaling 2,416 lines. These include the old node client, unused SDK runtime helpers, the pkg/runner entrypoint, obsolete snapshot helpers and fourteen v1 conformance artifacts. The tracked diff removes 4,025 lines in total, including 2,542 production Go lines. W4-owned files account for 347 removed lines, including 287 v1 fixture/transcript lines. These are incremental counts for this session, not the earlier hard cut.

## Acceptance and checks

- `protocol-go-sdk-fake-host-and-linkage/local-go-linux` is enabled. Linkage, public fake host, process plan/output and process body-failure comparison pass. The real HTTP-route subtest is skipped because sockets are denied by the sandbox. This is not a claim that the HTTP integration passed.
- `protocol-mask-acknowledgement/local-go-linux/sdk-service-contract` passes with an explicit acknowledgement barrier and immediate log, annotation and summary emission. The full cell remains skipped because C2 has no acknowledged Mask route contract.
- All seven Go v2 conformance sessions pass, including every eval kind, body/hook failures, repeat plan, unknown operation and major mismatch. The fake driver reads emitted errors independently of the unfinished C2 consumer decoder.
- Root `go build -p 4 ./...` passes.
- Vet passes for `internal/nodeproto/...`, `internal/protofd`, `internal/sdkserve`, `sparkwing/...`, `internal/acceptance` and `tests/acceptance`.
- Tests pass for `internal/nodeproto/...`, `internal/protofd`, `internal/sdkserve`, `sparkwing`, `sparkwing/inputs`, `sparkwing/cleanup` and `sparkwing/sparkwingtest`, excluding exactly `TestParseFrameV2Shapes` and `TestReplyDecodeV2`. Those two still panic in C2's unimplemented `apierr.DecodeInner`; they remain present and unskipped in source.
- Focused race tests pass for validation, framing, server lifetime, generated replay, AfterRun and fake-host behavior.
- Windows/amd64 compile checks pass for the protofd and acceptance test executables. They were not executed on this Linux machine.
- Caller checks pass for focused CLI argument/help/plan/explain/risk tests, repository hello/describe/framing/deadline/resolver tests, secret-cache/matcher tests, and seven focused v2 host tests. Vet passes for CLI/repos/secrets/runtime/controller and selected production orchestration/host files.
- `git diff --check` passes. The full SDK subpackage run also exposed sandbox-only socket failures in `sparkwing/services` and ancestor `/tmp/.git` failures in three `sparkwing/git` filesystem-fallback tests. No product workaround was added for those constraints.

All Go commands used `GOWORK=off GOFLAGS=-buildvcs=false`, `-p 4`, and GOCACHE/GOTMPDIR beneath `/tmp/sw-astra-W4/`. Fixture tests used TMPDIR there too. Commands ran with `go -C /home/bombuntu/.xwing/envs/1082/sparkwing`.

Reproduce the W4 test selection with:

```sh
go test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing ./sparkwing/inputs ./sparkwing/cleanup ./sparkwing/sparkwingtest -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2)$'
go test -p 4 ./tests/acceptance -run 'TestProtocol/(protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -v
```

## Ports and regressions

Retained reference tests now exercise the final resolver signature. The conformance corpus covers before/after/verify in addition to conditions, memo keys and generation, and distinguishes body and hook errors from invocation failures. New regressions cover nested generator reconstruction through Serve, weighted membership, generated-barrier cycles, malformed nested wire values and aliases, idle cancellation, partial writes, fake cleanup isolation and Mask acknowledgement ordering. Earlier secret-identifier and memo-input ports were retained rather than redone.

## Local contract clarifications

`c3-protocol-notes.md` now specifies `EvalResult.ValidateValue(kind)`, the RunResult input for after_run, ancestor generator replay under the target's binding, and per-membership concurrency cost. These stay inside W4's protocol contract. SessionConfig now receives engine, runner, binding and validation facts from callers. Config.CheckSecrets runs preflight before the host persists a plan.

## Contract changes needed in other areas

C2 must define acknowledged Mask registration, including the route/request/response and authorization policy. The SDK still returns canonical unavailable for real transport rather than inventing a route.

## Real integration left open

- W2: implement apierr.DecodeInner and connect structured failures, including failed AfterRun input. Decide how dynamic Mask registration interacts with identifiers already stored unmasked.
- W6: implement limits.Record for oversized annotations and summaries. Keep procenv and sealed limits authoritative when hosts are integrated.
- W3/W8: wire exact fetched engines and the server-granted runner from ClaimedUnit into remote execution. Remote legacy callers have explicit identity fields but cannot derive missing grants from environment. Replace legacy host persistence/scheduling with the declared state contracts, authoritative accepted seals, lease accounting and expansion hash checks. The bounded compile migration is not a W3 acceptance claim.
- W5: run TypeScript against the v2 corpus and replace its old fixture. W4 deleted obsolete v1 transcript artifacts rather than retaining dual-wire fixtures.
- W9: publish v2 schema/documentation snapshots and complete CLI behavior integration.
- Execute the pipe and SDK acceptance fixtures on native Windows, and the real host-route tests outside this socket-restricted sandbox.

Regular nodehost tests still reference v1 APIs in thirty legacy files. Orchestrator test compilation still fails in `node_protocol_serve_test.go`, `node_protocol_run_test.go`, `config_inspect_test.go` and `envelope_fixture_test.go`, with further compiler errors possible. Fixture strings in the host/launcher/CLI end-to-end suites still import deleted pkg/runner. These remain with W3/W8/W9 for behavior ports; no broad test deletion was used to claim their suites passed.

## Open question

How should a host handle a newly registered Mask value that occurs in an already accepted job or step identifier? Masking identity fields changes attribution; leaving the value visible violates the masking promise. This needs C2's registration policy, not a second SDK policy.

## Shared files changed

This report is also new at `design/rewrite/W4-session2.md`.

The following inventory lists every changed path outside W4's four owned package roots. `M` means modified, `D` deleted and `A` added. The migration was required to keep the repository building after the hard cut.

- `M cmd/sparkwing/action.go`
- `M cmd/sparkwing/action_explain.go`
- `M cmd/sparkwing/action_plan.go`
- `M cmd/sparkwing/action_plan_test.go`
- `M cmd/sparkwing/action_test.go`
- `M cmd/sparkwing/blast_radius.go`
- `M cmd/sparkwing/blast_radius_test.go`
- `M cmd/sparkwing/completion.go`
- `M cmd/sparkwing/describe_cache.go`
- `M cmd/sparkwing/help_registry.go`
- `M cmd/sparkwing/init.go`
- `M cmd/sparkwing/local_permissions_unix_test.go`
- `M cmd/sparkwing/ndjson_test.go`
- `M cmd/sparkwing/pipeline_args.go`
- `M cmd/sparkwing/pipeline_args_test.go`
- `M cmd/sparkwing/run_detached.go`
- `M cmd/sparkwing/run_host.go`
- `M cmd/sparkwing/run_risk_gate_test.go`
- `M cmd/sparkwing/runs_capacity.go`
- `M design/rewrite/contracts/c3-protocol-notes.md`
- `M internal/acceptance/protocol_go.go`
- `A internal/acceptance/protocol_go_unix.go`
- `A internal/acceptance/protocol_go_windows.go`
- `M internal/cluster/runner_pool_cli.go`
- `D internal/nodeclient/client.go`
- `D internal/nodeclient/client_test.go`
- `D internal/nodeclient/export_test.go`
- `D internal/nodeclient/leak_test.go`
- `D internal/nodeclient/nodeclienttest/host.go`
- `M internal/nodehost/admission.go`
- `M internal/nodehost/approvals.go`
- `M internal/nodehost/artifacts.go`
- `M internal/nodehost/attempt.go`
- `M internal/nodehost/caches.go`
- `M internal/nodehost/closures.go`
- `M internal/nodehost/debug.go`
- `A internal/nodehost/declarations.go`
- `M internal/nodehost/dispatch.go`
- `M internal/nodehost/events.go`
- `M internal/nodehost/expand.go`
- `M internal/nodehost/logs.go`
- `M internal/nodehost/memo.go`
- `M internal/nodehost/only.go`
- `M internal/nodehost/plan.go`
- `M internal/nodehost/podnode.go`
- `M internal/nodehost/rehydrate.go`
- `M internal/nodehost/retry.go`
- `M internal/nodehost/routes_auth.go`
- `M internal/nodehost/routes_local.go`
- `M internal/nodehost/routes_output.go`
- `M internal/nodehost/run_admission.go`
- `M internal/nodehost/scheduler.go`
- `M internal/nodehost/session.go`
- `M internal/nodehost/session_windows.go`
- `M internal/nodehost/steps.go`
- `M internal/nodehost/steps_range.go`
- `M internal/nodehost/testdata/attemptfixture/main.go`
- `M internal/nodehost/testdata/retryfixture/main.go`
- `M internal/nodehost/testdata/stepsfixture/main.go`
- `M internal/nodehost/timeout.go`
- `A internal/nodehost/v2_migration_test.go`
- `M internal/orchestrator/capacity_record.go`
- `M internal/orchestrator/child_admission.go`
- `M internal/orchestrator/claim_exec.go`
- `D internal/orchestrator/claim_wait_snapshot_test.go`
- `M internal/orchestrator/concurrency_dispatch.go`
- `D internal/orchestrator/dir_caches.go`
- `M internal/orchestrator/handle_local_trigger.go`
- `M internal/orchestrator/hosted_admission.go`
- `M internal/orchestrator/hosted_run.go`
- `M internal/orchestrator/main.go`
- `M internal/orchestrator/orchestrator.go`
- `D internal/orchestrator/plan_document.go`
- `M internal/orchestrator/pod_node_host.go`
- `M internal/orchestrator/repo_ident.go`
- `M internal/orchestrator/testdata/podnodefixture/main.go`
- `M internal/orchestrator/trigger_host.go`
- `D internal/orchestrator/typed_args_help_test.go`
- `M internal/orchestrator/worker.go`
- `M internal/repos/describe.go`
- `M internal/repos/describe_test.go`
- `M internal/repos/describe_windows.go`
- `M internal/repos/resolver.go`
- `M internal/secrets/cached.go`
- `M internal/secrets/mask.go`
- `M internal/secrets/secrets_test.go`
- `D internal/sparkwingruntime/closures.go`
- `D internal/sparkwingruntime/closures_test.go`
- `M internal/sparkwingruntime/config_secrets.go`
- `D internal/sparkwingruntime/config_secrets_test.go`
- `D internal/sparkwingruntime/describe.go`
- `D internal/sparkwingruntime/describe_test.go`
- `D internal/sparkwingruntime/dry_run.go`
- `D internal/sparkwingruntime/inputs.go`
- `D internal/sparkwingruntime/logger.go`
- `D internal/sparkwingruntime/oidc.go`
- `D internal/sparkwingruntime/oidc_test.go`
- `D internal/sparkwingruntime/pipeline_await.go`
- `D internal/sparkwingruntime/pipeline_ref.go`
- `D internal/sparkwingruntime/ref.go`
- `D internal/sparkwingruntime/reflect_secrets.go`
- `D internal/sparkwingruntime/runner.go`
- `M pkg/controller/cache_grant_scope_internal_test.go`
- `M pkg/controller/direct_upload.go`
- `M pkg/controller/finish_run_recovery_test.go`
- `M pkg/controller/github_app.go`
- `M pkg/controller/github_app_automation.go`
- `M pkg/controller/github_app_check_events.go`
- `M pkg/controller/github_app_webhook.go`
- `M pkg/controller/handlers.go`
- `M pkg/controller/oidc_token.go`
- `M pkg/controller/oidc_token_test.go`
- `M pkg/controller/trigger_test.go`
- `D pkg/runner/doc.go`
- `D pkg/runner/runner.go`
- `A tests/acceptance/protocol_mask_test.go`
- `M tests/acceptance/protocol_test.go`

## Friction and handoff

The sandbox blocks sockets and native Windows execution is unavailable. C2/C4 bodies still panic where noted. Overwing's installed binary reports that it trails its source checkout. No approval was requested and no compatibility shim was added to cross these gaps.

The lead owns commits, remaining cross-area integration, pipelines and gates. `/tmp/sw-astra-W4/` was removed after verification. All tracked fixture processes were reaped.

STATUS: CONTINUE
