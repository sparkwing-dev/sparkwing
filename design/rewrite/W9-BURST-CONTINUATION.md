# W9 burst launch continuation

Starting HEAD: `bd46f0fd5`. No Git writes, installed Sparkwing command execution,
services, deployment or release ran. This is the new launch vertical authorized
by decisions 112 and 116. Earlier selector/prune and local integration work was
retained.

## Implemented

- User `run.routing`, repository `defaults.run` and pipeline `run` merge by
  explicit field. Local remains the default. Explicit Cloud uses one sealed
  Cloud-owned request with RunnerHere and ordered here/machines/cloud or
  name/pool targets. Default holds are five seconds for here and machines.
  Unreachable-controller preflight falls back locally with a message.
  Authorization failures and post-intake failures never create a local copy.
- Production here hosting now binds the existing native host, authenticated
  private state, source access, data, logs, process services and leases.
  Preparation/intake and attached hosting use separate signal lifetimes, so
  Ctrl-C requests Cloud cancellation. An interrupt during an accepted intake
  also sends cancellation before attachment. Here authority is retained before
  a requested run-handle file is published.
- The local admission daemon provides actual capacity and free resources,
  including zero. Here claims and renewals carry these observations. Private
  snapshot reads and self-claims refresh them before canonical placement.
- Git bundles contain tracked files with working edits and deletions plus exact
  declared `source_files`. Parentless snapshots exclude original history and
  undeclared untracked content. Declared secret paths can be excluded before
  hashing. The final pre-upload scan rejects known local secret values and
  registered encodings in every reachable object and filename.
  Original invoking/default branches survive the detached inspection clone.
- Configured `local_secrets` select names from the pipeline's complete SDK
  declarations and resolve them from the requester environment. Values travel
  outside the seal, remain in a volatile registry and use the existing masked,
  live, declared claim release path. Responses and SQL retain names only.
  Replay cannot replace values, memory loss fails unavailable, retry requires
  fresh submission, and committed terminal completion releases values.
- Personal machine registration and claiming check authenticated account
  provenance. IDs of different subject kinds cannot impersonate each other.
  The CLI flag and client wire carry personal scope. Independent account
  sessions can register through the real API; the CLI bearer gap remains below.
- Per-account monthly overflow controls default to no cap. Request caps only
  tighten the saved cap. Cloud admission includes active reservations and
  compute and still enforces existing team standing/ladder/budgets. Personal
  and local capacity remains eligible after Cloud budget refusal. GET/PUT
  overflow API returns only the authenticated account's facts. Write-only PUT
  returns its mutation receipt without requiring a separate read grant.

## Contract changes made

Recorded in C1, C2, C3 and C4 contract files and the four W9-BURST component
reports. Lead-owned handoffs and amendment files are untouched.

- C4 adds routing fields and Pipeline.SourceFiles with shared validation.
- C1/C3 add sealed BurstPlacement and local secret names. CreateRunRequest gains
  the private values map. ClaimRequest gains here capacity/OS/architecture;
  RenewalRequest gains optional current availability. RemoteHostStore supplies
  the refresh callback and uses it before placement decisions.
- C1/C2 add Personal enrollment/origin, creator subject-kind provenance and
  overflow control storage. The rewrite baseline advances from 4 to 5. Version-4
  stores are refused; native QA must use fresh disposable state.
- C2 admits personal registration by an editor's independent account session.
  Team machine registration still checks team administration in the handler and
  store. No member-bearer exception to independent consent was added.
- C3 adds typed OverflowControl/OverflowControlWrite. Authorization inventories,
  OpenAPI, API references, exported API snapshots and embedded docs match.

## Decisions and real integrations still open

1. **DESIGN: requester secret delivery across controller processes.** The
   volatile registry belongs to one Store process. Intake, plan preflight and
   secret release must reach that process. A different replica correctly fails
   unavailable. Cloud needs verified process affinity or an unpersisted relay
   before this supports a multi-replica service. Persisting raw values or
   shipping them in source is not an acceptable fix.
2. **Personal registration from the CLI.** The profile supplies a member bearer,
   while C2 requires an independent account session to create an independent
   machine root. The real client enrollment test proves the bearer is refused.
   Add session-authenticated enrollment transport or obtain a separately
   reviewed bounded-ancestry issuance contract. The flag's fake-boundary test
   is component evidence, not a working public enrollment claim.
3. **Native composed Cloud acceptance.** No new native acceptance cell is
   unskipped or claimed complete. The lead must run an actual routed run through
   local hosting, personal-machine claims and Cloud execution, verify streamed
   logs/artifacts/Ctrl-C, and test saturation/holds. A burst of concurrent
   self-claims can race local admission between observations; refresh-before-
   snapshot/reconcile reduces stale data but is not an atomic cross-process
   admission reservation. Check that native load spills work promptly.
4. The earlier W9 report's foreign gaps remain: owner/eval downstream authority,
   annotation locations, Cloud launcher enforcement/cleanup, captured trigger
   environment, cache proof, publication, and remaining receipt/history/source
   integration. This session did not close those by adding placeholder data.

## Assumptions and mechanisms added

The new routing, source selection, ephemeral registry, placement observation and
account controls implement the explicitly assigned feature. No environment
variable, extra run owner, controller chaining or compatibility alias was added.
The smaller alternative of a local-first boolean cannot express ordered holds.
Existing blob, intake, host, masking, authorization and billing paths are reused.
Removing the new routing entrypoints and sealed optional fields reverses this
vertical before launch; the schema remains a disposable baseline cut.

Cloud overflow captures the current working tree and refuses --sw-ref and
--sw-pipeline-ref before connecting, with pipeline-trigger guidance. It remains
attached, as placement F already requires. These are named capability boundaries,
not ignored flags. The lead should ratify this committed-source behavior or
assign explicit revision support. A configured pinned source outside the captured
working tree is refused by name. All applicable run flags still pass the common
validator. --sw-run-handle-file is honored.

## Verification

Every Go command used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package workers.

Passed:

- Final whole-repository build and focused vet of the CLI, nodehost, settings,
  project config, wire, controller/client, controller and store.
- Full short userconfig, projectconfig, runspec, controllerwire,
  controller/client, clisurface, apispec and apiref suites.
- Focused CLI routing/fallback/source/handle/branch/masking/runner tests and
  nodehost observation, controller intake/release/overflow/personal, and real
  store placement/ownership/budget tests.
- Authorization route declaration, serving-registration and mutation-property
  contracts, updated for the two overflow routes and enrollment alternatives.
- CLI attachment, routing/capacity/handle and nodehost observation tests under
  race. Component reports retain registry and real secret-release race proof
  and source/registry/resolver fuzz campaign results.
- Surface/prune/engine-amendment acceptance selections, doccheck, embedded doc
  privacy/symbol/changelog/mirror checks, generated CLI references, commentcheck
  against main, and diff whitespace checks.

The broad pkg/docs invocation reached socket-based WebClient tests and could not
run in this sandbox. Its socket-free affected checks pass. A broad component
selection also hit an unrelated runner-image contract failure and existing
socket-based controller tests; the exact feature selections pass. Tests first
reproduced missing routing APIs, absent production here binding, stale placement
observations, lost handle-publication hook, missing snapshot branch metadata,
and source/secret/authorization failures. A temporary docs test was removed
before one compiler finished reading it; generation was rerun successfully and
that temporary file was removed after the successful run joined.

No archived test was newly ported. No native acceptance cell was newly marked
complete. New source, masking, scope and ownership properties are new coverage
for the added launch feature.

## Size and deletion

Final production Go versus starting HEAD: +1,623/-96, net +1,527 across the whole
vertical, including shared packages. No whole legacy file was replaced by this
new feature. The unavailable here-runtime body was removed; existing source and
host paths were extended rather than duplicated as services.

W9-owned production versus main: +18,077/-23,824, net -5,747. This remains within
the -2,900 W9 budget. W9-owned change versus starting HEAD is +757/-39, net +718.
Counts exclude tests, testdata, acceptance and contract declaration files.

## Shared files

- `.apidiff/pkg_controller.txt`
- `.apidiff/pkg_controller_client.txt`
- `.apidiff/pkg_projectconfig.txt`
- `.apidiff/pkg_store.txt`
- `CHANGELOG.md`
- `api/openapi.yaml`
- `design/rewrite/contracts/c1-data.md`
- `design/rewrite/contracts/c2-credentials.md`
- `design/rewrite/contracts/c3-controller-wire.md`
- `design/rewrite/contracts/c4-surface-config.md`
- `internal/apispec/wire.go`
- `internal/capability/issuance.go`
- `internal/capability/routes.go`
- `internal/nodehost/burst_availability_test.go`
- `internal/nodehost/remote_host_store.go`
- `internal/runspec/burst.go`
- `internal/runspec/burst_test.go`
- `internal/runspec/contracts.go`
- `internal/runspec/seal.go`
- `internal/userconfig/contract.go`
- `internal/userconfig/routing.go`
- `internal/userconfig/routing_test.go`
- `internal/userconfig/validate.go`
- `internal/controller/burst_overflow.go`
- `internal/controller/burst_overflow_test.go`
- `internal/controller/capability_management.go`
- `internal/controller/credential_route_expectations_test.go`
- `internal/controller/engine_claim.go`
- `internal/controller/engine_intake.go`
- `internal/controller/engine_lifecycle.go`
- `internal/controller/local_secrets_test.go`
- `internal/controller/root_provisioning.go`
- `internal/controller/secret_reads.go`
- `pkg/docs/changelog.md`
- `pkg/docs/mirror/api-reference.md`
- `pkg/docs/mirror/api.md`
- `pkg/docs/mirror/cli-run.md`
- `pkg/docs/mirror/cli-runners.md`
- `pkg/docs/mirror/local-execution.md`
- `pkg/docs/mirror/migrations/_unreleased.md`
- `pkg/docs/mirror/self-hosting.md`
- `pkg/projectconfig/burst_source_test.go`
- `pkg/projectconfig/contract.go`
- `pkg/projectconfig/validate.go`
- `internal/store/authority_issue.go`
- `internal/store/authority_provision.go`
- `internal/store/binding_secret.go`
- `internal/store/burst_here.go`
- `internal/store/burst_overflow.go`
- `internal/store/burst_placement.go`
- `internal/store/burst_placement_test.go`
- `internal/store/capabilities_contract.go`
- `internal/store/capability_rotation.go`
- `internal/store/claim_attention.go`
- `internal/store/dialect.go`
- `internal/store/enrollment_runtime.go`
- `internal/store/expansion_accept.go`
- `internal/store/intake_runtime.go`
- `internal/store/local_secrets.go`
- `internal/store/local_secrets_test.go`
- `internal/store/owner_placement.go`
- `internal/store/plan_contracts.go`
- `internal/store/plan_seal.go`
- `internal/store/run_spec.go`
- `internal/store/run_state.go`
- `internal/store/schema.go`
- `internal/store/schema.sql`
- `internal/store/secret_preflight.go`
- `internal/store/secret_reads.go`
- `internal/store/secrets.go`
- `internal/store/store.go`
- `internal/store/tenant_sql_scope_guard_test.go`
- `internal/store/unit_billing.go`
- `internal/store/unit_claim.go`
- `internal/store/unit_matching.go`
- `internal/store/unit_placement.go`

Shared source-option callers in cmd/sparkwing/local_host.go, local_run.go,
local_run_host.go and schedules_local.go moved to hostSourceOptions with an
explicit local/remote policy. No removed production API has an unmigrated caller.

Friction: sandbox sockets prevent native services; personal CLI consent and
multi-process ephemeral delivery need real contracts. Overwing reports an
installed/source version lag. No approval or operational binary was requested.
Scratch is removed after all checks join; /tmp/sw-gocache is retained.

STATUS: CONTINUE
