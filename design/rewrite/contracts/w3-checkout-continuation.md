# W3 checkout-planning continuation

<!-- design:begin -->

W3 remains incomplete. The shared owner now has a default checkout-planning
assembly. Native CLI, trigger, runner and launcher entry points still use the
legacy runtime. The previous handoffs remain the inventory for that cut.

## Implementation

`OwnerHost.Planner` supplies the default when `Prepare` is absent. The owner
reads the nearest sparkwing.yaml between the acquired pipeline directory and
the acquired checkout root. `os.Root` confines every read, including symlinks.
An ancestor outside the checkout is never configuration for that run. Source
entries expose their existing acquired root through `CheckoutDirectory()`.

`SDKPlanner.PrepareCheckout` uses the shared project parser and restricted
declaration projection. The shared settings resolver merges request arguments
over pipeline arguments and defaults. SDK describe fills remaining defaults and
retains secret classifications. The shared guard parser evaluates the selected
when/unless block after describe. An explicit empty block replaces defaults.
Unknown default branches refuse both when and unless predicates. App and sealed
default-branch facts take precedence over acquisition discovery. Origin HEAD
does not create App provenance.

Guard diagnostics carry the predicate's field and index. They contain no raw
predicate or argument value, so escaping cannot reveal a secret. Raw predicates
remain in the restricted declarations and spec. A refusal bypasses SDK planning
and submits a validated empty plan and the
refusal through normal acceptance. The shared decider produces the final verdict.

`OwnerPlanning.Resolve` computes the final proposal from the validated plan.
It runs for fresh plans and cache hits before acceptance. The store still reruns
resolution and validation in its transaction. Intake secret classifications are
unioned after resolution. Resolution failure cannot accept or dispatch work.

Cache identity now binds the acquired project bytes and relative filename, plus
the sealed team, repository and entrypoint. The placement still supplies complete
source, executable, environment and secret-version fingerprints. Missing inputs
remain a normal cache bypass. No ambient filesystem or environment fingerprint
is substituted for an absent capture.

The shared SDK run-context projection also retains pull-request Action and
BaseSHA. The new acceptance assertion reproduced their omission before the fix.
Planning, attempts and evals use the same projection.

## Local contract additions

- `RunInput.GrantedRunner` carries the award identity into default planning hello.
- `OwnerHost.Planner` and `Planning` supply the default SDK assembly and validation
  context. `Prepare` remains the trusted adapter seam.
- `OwnerPlanning.Resolve` resolves plan-dependent settings after validation.
- `CheckoutPlanningInput` and `SDKPlanner.PrepareCheckout` expose the same assembly
  to controller-owned planning.
- `source.Entry.CheckoutDirectory` exposes the acquisition-owned root for confined
  reads. It does not grant source access.

These additions belong to W3. No other area's contract shape changed.

## Acceptance and ports

`environment-origin-head-and-guards/hermetic-checkout-planning` passes sixteen
cases through the common owner and acceptance boundary. It covers argument
precedence, SDK defaults, empty guard replacement, local/controller and argument
guards, App/sealed/discovered defaults, feature/default-branch rejection,
unresolved defaults, missing where context and invalid legacy vocabulary.
Every started fake SDK process is closed and cleaned.

The host half of inventory row 60 is ported, including the original feature
admission and trunk refusal. The existing direct-source tests supply real Git
origin-HEAD acquisition coverage. This continuation also supplies the shared
checkout/guard behavior for the removed launcher tests
`TestLaunchedNode_LayersTheCheckoutsYAMLArgsAndGuards` and
`TestLaunchedNode_AGuardJudgesTheSDKsDefaults`. Native placement execution remains
unverified. The shared PR assertion retains the old
`TestHostedRunContext_CarriesThePullRequest` fields.

The default owner path is exercised with an acquired native Git checkout and
real project files. Store, SDK, engine and validator adapters remain fakes.
Additional tests cover nearest-file discovery, the root boundary, cancellation,
changed project cache identity, cached/fresh resolution and resolution failure.

Two fuzz invariants are checked: no guard diagnostic reveals a secret argument,
and no project read returns bytes from outside the acquired checkout. Both
3,000-case runs pass. The guard fuzzer first found a fixture mistake: YAML
refuses DEL in a guard value. The property now checks safe refusal as well as
safe diagnostics, and keeps DEL as an explicit seed.

## Contract changes needed in other areas

New C1/W6 gap: optional secret declarations cannot survive the current seal.
`Resolved.SecretNames` authorizes secret release in `internal/store/binding_secret.go`,
while `preparePlanSealTx` sends every name to required-secret preflight. There is
no place to retain `SecretDeclaration.Optional`. Omitting optional names would
deny their release. Keeping them makes absent optional secrets fail preflight.
W3 retains every declared name and does not invent a second seal shape. C1/W6
must carry optionality through acceptance, expansion and retries. The component
store fake does not prove that integration.

Previous gaps remain: full restricted execution reads, result/failure transport,
materialized-result source transport, owner policy and approval edges, generation
zero eval logs, cross-process dynamic masks, memo source/publication, selected
engine host invocation and native runner/bootstrap wiring. Native source still
needs W6's procenv.Git implementation. The shared parser and installer still have
W4/W8 panic stubs in this checkout. None is replaced by a host-private contract.

## Remaining work and scope

The three old run-host entry points, scheduler, sessions, layers and routes still
need their coordinated hard cut. No legacy production code or legacy test file
was deleted in this continuation. No external production caller required migration.
There are no new flags, environment variables, services, credentials or limits.
The added planner and resolve seam assemble frozen behavior. The lead retains
independent review, Git writes, native drivers, pipelines and landing.

Shared files touched are `internal/acceptance/host_owner.go`, the new
`internal/acceptance/host_checkout.go`, `tests/acceptance/skeleton_test.go`, the new
`tests/acceptance/checkout_test.go`, C3 contract prose and the port inventory.
All other changes belong to W3. Only this worktree and `/tmp/sw-astra-W3/` were
written. No Sparkwing binary or Git write ran.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and at most
`-p 4`. GOCACHE, GOTMPDIR and TMPDIR are under `/tmp/sw-astra-W3/`. Scratch is
removed before the final report.

Whole-repository build, scoped vet, full source/lease/acceptance suites and the
focused current host suite pass. The host selection is the previous handoff's
selection plus TestSDKCheckout, TestCheckoutProject and the new fuzz seeds.
Commentcheck against main reports the same three inherited findings in
controllerwire/contracts.go, store/state_materialize.go and
store/state_result_records.go. No W3 finding is reported. Sleepcheck against
HEAD passes.

Final race checks pass for OwnerHost, checkout planning/discovery, the new
acceptance cell and both fuzz seed sets. `git diff --check` passes. The repository
has no local user identity or core.worktree override. All tracked check processes
finished before scratch cleanup.

Native HTTP/api.sock, real store authorization and preflight, Postgres/S3,
Windows/Darwin, launcher/runner deployment and real-repository QA remain
unverified. The native acceptance targets remain skipped. Broad legacy tests
were not rerun because the previous handoff records schema/socket failures in
the runtime being replaced.

<!-- design:end -->
