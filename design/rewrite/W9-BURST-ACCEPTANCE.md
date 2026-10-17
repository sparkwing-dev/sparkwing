# Burst acceptance evidence

The composed controller and store paths pass for a local run owner with a
personal-machine worker, a saturated local owner with a personal-machine worker,
a saturated local owner with a Cloud worker, and an available local owner
executing here before overflow. The Cloud retry flow retains two historical
attempts, prepares source, exchanges, starts attempt three and reads source
again. These tests use the real
`EngineClient`, `Server.Handler`, SQLite store, capability issuance, intake,
plan sealing, object storage and source checkout. The HTTP transport calls the
handler directly. Runner version admission and capacity are fixture adapters.
The Cloud worker materializes source under its bootstrap before exchanging it
through the real HTTP endpoint, matching the production source initializer.
Bootstrap secrets, writes and general existence queries are refused. Exchange
revokes the original bootstrap's source authority.

`pkg/controller/burst_acceptance_test.go` proves the following in each flow:

- The requester submits a source bundle and a declared local secret, receives
  the one-time here bearer, claims the Cloud-owned run and seals its plan.
- The sealed here/machines/cloud preferences survive intake. Advancing the
  injected store clock expires the ordered holds. A saturated machine still
  acquires the run owner with zero available cores.
- The requesting user's overflow allowance defaults to no cap. A zero cap
  blocks Cloud after the holds expire. Clearing it admits Cloud, and a personal
  machine remains usable with the zero cap.
- The awarded worker reads the declared secret with `masked=true` and
  `Cache-Control: no-store`. An undeclared secret is refused. The accepted plan
  and persisted snapshot contain no secret value.
- The worker downloads and materializes its sealed source through the real
  `NewSourceAccess` and `source.PrepareCheckout`. Another run's bundle returns
  HTTP 403. Pre-attempt upload and output-record writes are refused.
- A competing Cloud poll cannot acquire the awarded job. Requester cancellation
  reaches both the local owner and worker through their real renewal paths.

`pkg/store/blob_source_setup_test.go` covers first acquisition and retries with
an earlier attempt ordinal. Its sixteen cases permit only the bound run's sealed
source bundle. Foreign run/digest/kind, changed holder/generation, cancellation
and lease expiry refuse the read. An existing output remains unreadable and
upload authority stays unavailable before the attempt starts.
`FuzzBlobSourceSetupReadRemainsFenced` reuses that matrix with generated source
digests, attack selectors, retry ordinals, holder IDs and generation changes.
Its sixteen seed cases pass.

`pkg/store/blob_bootstrap_source_test.go` covers Node, Plan and Eval bootstrap
downloads through the shared authenticator, route authorization, data policy and
store projection. Its thirty cases and thirty fuzz seeds vary source digests,
historical attempt ordinals, foreign run/digest/output records, generation,
holder, lease expiry, consumed bootstraps, revoked ancestry and cancellation.
The exact two bootstrap grants remain unchanged. Exchanged awards retain source
reads. NodeStart allocates the next ordinal and opens normal upload identity.

## Corrections found through the composed path

The initial test failed because a claimed node could not read source bytes before
starting its execution attempt. The store now projects read authority without an
attempt or upload identity for a live pending node and admits only its sealed
source edge. Download projection chooses the authorized read grant instead of
inferring write access from `WriteRef`. The HTTP policy router uses its existing
data error projection for data binding failures, so a foreign digest returns
`forbidden` instead of `internal`. The C1 contract records these changes.

Production files changed by this worker:

- `internal/capability/authentication.go`
- `internal/capability/authentication_types.go`
- `internal/capability/data.go`
- `internal/capability/policy.go`
- `internal/capability/routes.go`
- `pkg/controller/data_authority.go`
- `pkg/controller/engine_routes.go`
- `pkg/store/blob_persistence.go`
- `pkg/store/blob_read_authority.go`
- `pkg/store/blob_authority_projection.go`
- `pkg/store/blob_authority.go`
- `pkg/store/blob_data_access.go`
- `pkg/store/authority_binding.go`
- `pkg/store/authority_issue.go`
- `pkg/store/authentication_binding.go`
- `pkg/store/node_state.go`
- `pkg/controller/policy_router.go`

No endpoint, grant, environment variable, schema, queue, service, or persistent
secret store was added. Internal authentication adds the narrow `sourceBundle`
binding and decoded `RequestEvidence.ResultAttempt` so transition source
ordinals stay distinct from active execution identity. No API removal required
caller migrations. The shared launcher snapshot fixture accepts a prior attempt
count and seeds the Eval source's completed attempt where needed.

## Verification

Commands ran from the shared worktree with this exact prefix:

```sh
env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing
```

The arguments and results were:

| Arguments | Result |
| --- | --- |
| `test -p 4 ./pkg/controller -run '^TestBurstAcceptance' -count=1` | Passed, 0.953s. |
| `test -p 4 ./pkg/controller -run '^(TestBurst\|TestRequesterLocalSecret\|TestSourceSnapshot)' -count=1` | Passed, 4.359s. |
| `test -p 4 ./pkg/controller -run '^TestBillingModelControllerNewCloudAwardCarriesLimits$' -count=1` | Passed, 0.225s. Retains the team New-tier Cloud admission proof beside user controls. |
| `test -p 4 ./pkg/store -run '^TestBlobSourceSetupReadRemainsFencedWithoutAnAttempt$' -count=1` | Passed, 0.499s. |
| `test -p 4 ./pkg/store -run '^(TestBlobSourceSetupReadRemainsFencedWithoutAnAttempt\|FuzzBlobSourceSetupReadRemainsFenced)$' -count=1` | Passed, 1.135s, including all sixteen fuzz seeds. |
| `test -p 4 ./pkg/store -run '^(TestBlob\|TestSourceUpload)' -count=1` | Passed, 50.394s. |
| `test -p 4 ./pkg/controller -run '^TestBurstAcceptanceSaturatedLocalOwnerCloudRetryWorker$' -count=1` | Passed, 0.390s, including pre-exchange materialization, NodeStart, exact replay and after-start source. |
| `test -p 4 ./pkg/store -run '^(TestBootstrapSourceBundle\|FuzzBootstrapSourceBundle)' -count=1` | Passed, 1.791s, including all thirty fuzz seeds and postexchange source/upload checks. |
| `test -p 4 ./internal/capability -count=1` | Passed, 0.077s before the final transition-proof addition. The lead owns the final combined checks. |
| `git diff --check` | Passed. |

The command table escapes `|` for Markdown. The executed regular expressions
used ordinary alternation characters.

## Native verification still required

These are HTTP/store acceptance tests. They do not start a native CLI host,
registered-machine daemon, Kubernetes Job, or SDK step. The lead still verifies
the installed CLI's local output stream and Ctrl-C behavior, actual personal
runner polling, Cloud launch/exchange/execution and secret masking in worker
logs. PostgreSQL and native Windows/Darwin remain separate acceptance cells.

The CLI's existing burst source selection tests cover tracked edits, declared
untracked files and secret exclusion. The composed tests here start with a Git
bundle and prove its authenticated upload-to-worker materialization path.
The CLI's routing/default/fallback tests are separate evidence owned by the lead.
This report does not claim native Cloud launch completion.
