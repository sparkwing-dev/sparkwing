# W2 signing and fixture cleanup continuation

<!-- design:begin -->

Started from 6eb05e97b. No Git writes. Lead-owned handoffs and amendments are
unchanged. This continues W2-principal-reporting-handoff.md.

## Implemented and deleted

Removed the duplicate controller filesystem and S3 signers. W7's physical
backends now own object validation, signing and byte verification. Added
NewCloudFrontDataBackend over the real S3 backend. It delegates validation to S3
before signing an ingress GET with the same key and expiry. Direct requests keep
regional S3 URLs. PUTs keep S3's size and checksum constraints. Embedded S3 methods
preserve manifest, publication, deletion and log-deletion behavior.

The final download handler carries ingress selection in a private request context.
Forwarding headers select transport only, after the existing authentication,
read-set authorization, metadata read and download charge. They grant no object
access. The standalone controller uses this backend instead of the removed
WithSignedDownloads setup. Invalid nonempty non-S3 configuration still refuses.

Deleted the old output-upload signing/commit helpers and their pending/final-key
machinery. The active log-reader configuration is named WithS3Logs. Deleted the
unused principal/holder-string execution-location parser and obsolete tenant,
source-repository, credential-budget, decoder, login-failure and storage-refusal
helpers. No old credential verifier or route alias was added.

Authenticated-request metrics now count final policy identities using the closed
account/machine/service/operator/local/anonymous/other label set. The old token
cache collector is gone. Claim-unavailable counting uses the final runner,
launcher and named claim routes. Docs, migration guide, API snapshot and embedded
mirrors describe the cut.

W2 production accounting follows the lead's script, excluding tests, testdata,
acceptance and paths containing contract, and including new untracked files.
This session adds 116 and deletes 815 lines, net -699. Against main, W2 adds
12981 and deletes 16313, net -3332. The budget is -250. Four obsolete test files
remove 595 lines, and the old cache-metric test removes another 40 lines.

## Invariants and ports

- Issuance may persist only requested known grants held by the current session.
  CredentialIssuanceValidatesEveryGrant uses real SQLite and every declared grant.
  RefusedGrantProperty generates unknown and mixed grant sets and checks that
  refusals create no capability row or echo the private input. These replace the
  six tokens_test.go cases. The source-scanning Scope* constant test retires.
- Authentication metrics never label a credential, subject ID or request value.
  RequestPrincipalMetricLabelsProperty generates hostile kinds and identities and
  checks the gathered metric's complete label set. The HTTP test counts successful
  authentication and later authorization refusal once, excluding dead credentials
  and unmatched paths.
- CloudFront signs one backend-validated object within the download lifetime.
  CloudFrontDataBackendSignedObjectProperty generates tenants, digests and sizes,
  decodes the policy, checks resource/expiry and verifies the RSA signature.
  Changed policy bytes fail verification. Configuration, cancellation and invalid
  object tests retain private-key privacy and size/checksum-bound S3 PUTs.
- DataReadHTTPChoosesCloudFrontOnlyForIngress uses the actual controller handler
  and S3 signer with fake authority/metadata/charging facts. Both forwarding
  headers select CloudFront. Direct requests retain S3. Each path charges once
  before signing. This is component evidence, not a real distribution test.
- Browser fixtures now use actual account sessions, sealed CSRF-key persistence
  and member capability issuance. Session-header writes require CSRF. API refusals
  preserve cookies. Nonlocal pages require sign-in before bootstrap. The operator
  page checks the configured account gate, removal and capability-session refusal.
- The local run-directory deletion test passes Authorized to a fake TeamWriter
  and retains directory/sibling/path refusal checks. It does not prove the missing
  production committing deletion adapter.
- Chunked claim bodies preserve final kinds and reject removed pipeline/lease
  selectors. Rotation retains explicit, zero and default grace. Empty final
  bodies refuse. These replace chunked_body_test.go's four legacy-route cases.
- Execution-location tests retire with legacy identity inference. Existing final
  engine claim and run-detail tests retain granted runner identity and persisted
  generation/attempt history. Existing DataStorageSignedFilesystemRoutes exercises
  actual signed transfers and verified bytes through W7's backend.

The port inventory records these mappings. No scenario catalog, Harness or
boundary registry changed. No native credential-matrix cell is newly completed.

## Integration points and findings

Lead decisions 07:40 items 2, 3, 5, 7 and 10 were implemented before this session.
Their existing contract consumers remain. No new foreign interface change is
needed for this session's implementation.

W7 finding: the shared physical signer must reject a PUT beyond
limits.DataUploadURLTTL. A temporary TestW2ProbeSharedUploadLifetime called both
blob.NewS3 and blob.NewFilesystem with a valid pending key, one-byte size, valid
SHA-256 and expiry now + 16 minutes. Both accepted it. The exact command was
`go test -p 4 ./internal/controller -run '^TestW2ProbeSharedUploadLifetime$' -count=1 -timeout=180s`
with the common environment below. Both subtests failed with:
`shared signer accepted a 16-minute PUT; the contract maximum is 15 minutes`.
The probe was removed. W7's objects.go validateObject checks the GET maximum but
its PUT branch returns validatePending without an upload-lifetime check. Fix the
shared validator and add a property over expiry boundaries there. W2's Upload
already caps the requested reservation and rejects an overlong returned expiry
before signing. No second validator was added here.

W1 still needs peer authentication and committing local authority, roleless
sessions, the configured console committing gate and BindingState.CancelReason
projection. Job cancellation intent must precede run intent in the one snapshot.
The seeded bootstrap issuer/admin reader, W1b financial-event settlement and the
W1/W8 launcher award fence/correlation remain pending. The prior report and linked
handoffs retain the other credential/source/data/account/enrollment/sizing/run-control
transactions. Standalone startup supplies physical objects but still needs W1's
trusted data-authority, metadata and charging composition for authenticated data
routes. Missing adapters continue to refuse.

Legacy team-token, identity, claim and log fixtures still need ports or deletion.
The full controller attempt reached TestBusyRunnerHeartbeatUpdatesObservedLivenessWithoutPolling,
which failed with `tokens: insert: SQL logic error: no such table: tokens (1)`,
then TestClaimAttentionNamesWhyARunWaits panicked because the sandbox refused its
listener. No complete controller suite, native socket, Kubernetes, CloudFront,
Postgres or provider deployment pass is claimed. No new architecture question
requires a human decision in this report.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp, and go -C with
this checkout's absolute path. Tests use -p 4, -count=1 and -timeout=180s.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Full capability and apierr tests.
- Controller selection, 5.243s: Test prefixes CloudFront, Engine, Policy,
  ServerPolicy, CredentialIssuance, FinalCredential, Browser, OperatorPage,
  DeleteRunOnALocal, RequestPrincipalMetric, CredentialLabel, Audit,
  S3LogSegmentURLsSignTheSizeAndMD5, CacheGrantRouteIsAbsent and
  ServicesDoNotAnnounceRemovedDataRoutes, plus FuzzEngine seeds. The legacy
  StreamsOutlive and ReadsStayInside fixtures are excluded.
- W2 executable acceptance selection, 15.766s: Test prefixes Credential,
  CapabilityManagement, Data, Discovery, Payment, IdentityLink, LoginHTTP, OAuth,
  Session, Source, Secret, OIDC, Account and RootProvision, plus Credential/Data
  fuzz seeds. Existing skips are not counted as completed native cells.
- Actual filesystem signed-transfer property, 0.133s.
- Focused controller startup/configuration tests, 0.042s.
- Full apidiff, apiref and apispec tests. Focused docs symbol, privacy, migration,
  changelog and mirror tests.
- Vet for controller, executable acceptance, controller startup, API snapshot and
  API reference/schema packages.
- API snapshot regenerated. Documentation sync/check, changelog check and
  git diff --check pass. Commentcheck is clean across 2198 Go files.

TypeScript source and setup are unchanged. The previous session's native Linux
TypeScript pass was not rerun. The lead owns native deployment evidence, pipelines,
review, commits and landing. No Sparkwing binary or pipeline ran here. Default Git
identity is unchanged and core.worktree is unset. Session scratch is deleted at
handoff. The shared Go cache is retained.

## Shared files touched

Outside the W2 package, cmd/sparkwing-controller/main.go migrates the removed
signer/configuration calls. tests/acceptance/discovery_http_test.go migrates the
log configuration name. tests/acceptance/data_reads_http_test.go adds the real
signer HTTP handoff proof. Other shared artifacts are C2's own contract, the port
inventory, CHANGELOG.md, docs/observability.md, docs/migrations/_unreleased.md,
their generated mirrors and .apidiff/pkg_controller.txt. No W1, W7 or W8 source
or lead-owned contract file changed.

Mechanisms added: one CloudFront adapter over the existing concrete S3 backend,
and a private request-context transport selector. They preserve the documented
CloudFront/regional split without another physical validator or credential path.
The removed signer stack could not serve the final object interface. No new
setting, flag, environment variable, store, cache, timeout, schema, customer cap or
service was added.

<!-- design:end -->

STATUS: CONTINUE
