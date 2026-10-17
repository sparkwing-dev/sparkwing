# W9 full-area review fixes

Base: `7afbac89c`. No Git writes. Lead-owned amendments and handoffs are unchanged.
The supplied native report has no FAIL for the last committed state. This work
still needs the lead's commit, review and fresh native harness run.

Closed rulings: 1, 2, 3, 6, 9, 10 and 12.

- Here intake uses native committed-source preparation and selects RunnerHere.
  Request.Reason survives sealing, intake, replay and retry. The new regression
  uses the real parser, Git checkout, SDK compilation, selected-engine installer
  and inspection, HTTP client, controller handler and SQLite store. It injects
  no preparation. First intake returns a bearer, replay returns none, and a
  changed reason under the original key conflicts. The obsolete refusal test
  no longer expects here intake to fail.
- Hook proofs and generated hooks use plain output. A proof's actual process
  environment passes through the run parser. Generated hooks execute through sh
  and retain explicit pretty/json/plain overrides. The quiet-output string test
  and prose are replaced.
- Repository plan subprocesses use --pipeline. Any failed baseline restores the
  module snapshot and stops before bump or commit. The uncompared success state,
  type, projection and separate comparison list are deleted. A real SDK fixture
  runs through execOps.Plan and the public subprocess parser. The existing
  updater component test now rejects its formerly accepted baseline failure.
- Git source selection and inspection checkout both supply procenv's masker.
  Real Git fixtures accept HTTPS_PROXY=https://user:pass@proxy. A generated-input
  regression checks that proxy userinfo stays out of acquisition diagnostics.
- Catalog and completion metadata join by configured entrypoint and display the
  invoked name. A real compiled SDK supplies metadata for two aliases, alongside
  an unrelated registration whose name collides with one alias. The old fake
  catalog/completion test is replaced; its separate registered-flag assertion
  remains.
- Launch guides present Local and Cloud. Self-hosted runners join Cloud.
  Operator onboarding uses operator teams create. Deferred controller onboarding,
  stale pending claims and the cache-service promise are removed. Generated API
  docs and embedded mirrors are refreshed.
- profiles set --token and profiles add --scope are hard cuts. --token-stdin
  remains. Real config and stdin tests preserve other settings. Usage refusals
  leave credential bytes unchanged and never echo supplied token values.

Invariants fixed: here reasons survive the immutable request; hooks emit only
accepted output selections; an uncomparable baseline cannot authorize an SDK
update; proxy credentials remain masked while Git still receives network
configuration; alias metadata belongs to its configured entrypoint; credentials
never enter the profiles command through argv. Proxy and profile boundaries have
fuzz targets. Each corrected behavior ran red before the product edit.

Four listed simplifications are done: claim_paths.go is deleted (14 lines),
runSparksResolve plus dispatch/help entries are deleted (34 lines), twelve
interface constructors return newEngineClient directly (48 fewer lines), and
unsupported profile scope, unused completion bookkeeping and deferred onboarding
prose are removed. The first simplification, passing Invocation through all
handlers and deleting reconstruction/duplicate parsing, remains with ruling 4.

Remaining rulings, in order for the next session:

1. Ruling 4 and simplification 1: one Invocation through execution; remove
   --fleet and legacy hooks --all, implement --all-repos and output/usage contracts,
   and exercise every registered command's flags through the real handlers.
2. Ruling 5: filtered run.submit_env_allow capture, private transport/retention
   and host consumption, including masking. The unconditional refusal remains.
3. Rulings 7 and 8: retained run switches and request-id. The placeholder
   refusals and discarded request-id remain. No cuts in this group were made.
4. Ruling 11: released-engine offline fixture through signed policy and verified
   cache. The existing replacement-pin offline fixture is insufficient.

The option table records the retained contract, not completed implementation:

| Option | Kept or cut | Source and remaining work |
| --- | --- | --- |
| Relative --sw-priority | Kept, pending | C4 Registry/run and settings priority; resolve queue position before sealing. |
| --sw-no-bincache | Kept, pending | C4 Registry/run; connect source installation to the existing uncached compiler. |
| --sw-hash-all-files | Kept, pending | C4 Registry/run; connect the retained source/hash policy. |
| --sw-allow-unadmitted | Kept, pending | C4 Registry/run and decision 91's retained local admission model. |
| --sw-index | Kept, pending | C4 Registry/run and procenv.Runtime.GateIndex; retain its verified private snapshot. |
| --target | Kept, pending | C4 Registry/run; bind the selected target through retained preparation. |
| --sw-request-id | Kept, pending | C4 Registry/run requires detached mode; implement the retained contract rather than discarding it. |
| profiles set --token | Cut | Lead ruling 12; use --token-stdin. |
| profiles add --scope | Cut | Lead simplification 5; existing member credentials enter through stdin. |

Contract changes made: runspec.Request adds optional reason, included by the
existing canonical request/spec hashing and retry cloning. C1 data, C3 controller-wire and
C4 surface contracts record it. C4 also records fail-closed baseline comparison.
No database migration, route, grant, credential kind, timeout, limit, environment
variable or compatibility path is added. No foreign contract amendment is needed
to finish these closed rulings. Remaining integration work is listed above.

Shared files touched: internal/runspec/contracts.go; internal/repos/update.go and
update_test.go; api/openapi.yaml; docs/api-reference.md; pkg/docs/changelog.md and
mirrors; and the C1/C3/C4 contracts. The API regeneration also picks up pre-existing
source drift in AppFacts, PullRequest and operator object-store quota bindings.
Other edits belong to W9. Removing UncomparedPipeline migrates its sole CLI
renderer in cmd/sparkwing/repos.go. The unused client paths and sparks resolver
have no remaining callers. Client constructor edits are in engine_client.go,
pools.go, prune.go, queue.go, runlogs.go, runners.go, runreview.go, schedules.go,
secret_management.go and sizing.go.

Verification uses GOWORK=off, GOFLAGS='-buildvcs=false -p=4',
GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W9 and
GOTMPDIR=/tmp/sw-astra-W9/go. At most four package workers run per test command.

- Whole-repository go build -p 4 ./... passes. Vet passes for CLI, clisurface,
  repos, runspec and controller/client.
- Complete clisurface, repos, runspec and controller/client suites pass.
- Focused CLI hook, alias, profile, trigger/preparation/inspection, repository and
  launch-guide tests pass. The new native-here and real plan subprocess tests
  pass. Focused controller intake/here/retry/operator-team tests pass.
- Surface, Here and EngineAmendments acceptance families pass. No acceptance cell
  was newly unskipped and no archived inventory test was newly ported.
- Profile token/scope fuzzing passes 3,438 generated cases. Proxy/Git fuzzing
  passes its seeds and ten real-Git cases with two workers.
- Full doccheck, generated API spec check, embedded mirror/privacy checks,
  commentcheck against main, sleepcheck against HEAD and diff whitespace pass.
- A broad CLI selection stopped at TestDocsMigrationsValidatesWebBoundsBeforeFetching's
  forbidden TCP listener. The complete pkg/docs suite stops at
  TestWebClient_Versions_HappyPath for the same sandbox restriction. These do not
  establish native full-suite success. The lead runs those suites after commit.

The earlier amendment/handoff audit remains: A1-09/16/17/38/41/43/49/50/51/52,
source attempts, step reports/windows, pipeline identity, launcher grammar,
doctor, local composition, owner/eval separation, shared logs, service handover
and decisions 110/111/113/114 retain their completed implementations. A1-53 and
native Windows/Darwin, deployed Cloud/DinD, signed fresh-machine distribution and
PostgreSQL remain lead integration lanes. No foreign stub blocked this session.
The legacy bin/k8s-e2e.sh onboarding stop remains outside these W9 doc edits;
its stale source-scanning assertion was removed from setup_docs_test.go.

Production Go this session: +45/-167, net -122. W9-owned production is -101.
W9 versus main is +19,106/-23,856, net -4,750, within the -2,900 budget.
Whole legacy files deleted: one, the 14-line claim_paths.go. Other dead production
paths are deleted in place. Scratch is removed after all checks join. The shared build cache is retained.

Friction: socket refusal prevents native listener evidence, and the installed
Overwing binary reports source lag. Native completion belongs to the lead
harness. Rulings 4/5/7/8/11 still require implementation.

STATUS: CONTINUE
