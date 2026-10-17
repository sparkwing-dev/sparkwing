# W9 burst review rulings

Base: `adfcef9f7`. Implemented B1 and S1-S5 under the lead's integration ruling.
The latest lead-native report has no FAIL for the prior committed state. This
work still needs the harness rerun after the lead commits it. No Git writes ran.

The security invariants are that repository declarations can only narrow machine
authorization, and that burst jobs and requester secrets stay inside the sealed
claimant set. Tests exercise routed CLI preparation, real HTTP handlers, issuance,
SQLite, source storage and secret release. Only the HTTP transport is in-process.
They do not substitute the store, matcher or credential policy.

- B1: project run settings use RunRestrictions. They accept route:local and secret
  names. Profiles, preferences, limits and secret source authorization stay in
  machine run.routing. Repository names cannot change machine source selection
  or classification, including exclusion from local snapshots. Machine source_files authorizes untracked
  paths. Unapproved repository declarations, including ignored files, fail before
  upload. The old fieldwise MergeRunRouting API is deleted, with no callers left.
- S1: claim admission and secret reads independently check authenticated here
  ancestry, personal enrollment/ownership and sealed preferences. Cloud requires
  a cloud entry. Team runners cannot acquire burst work, and labels cannot grant
  eligibility. The sealed here owner retains planning authority independently of
  job placement. Placement holds only consider active personal registrations.
- S2: RenewalRequest.local_secrets re-supplies volatile values through the live,
  fenced here owner. The CLI retains them on RemoteHostStore and sends them on
  renewal, including before resumed host construction. A fresh Store/controller
  over the same SQLite database restores secret release through that channel.
  Worker resupply, changed values and mismatched names fail. Team deletion and
  owner lease expiry purge values using the existing commit cleanup. The expiry
  test covers requeued, unfinished work. Values never enter persistent storage.
- S3: publish-mode success skips pipeline_success_pins. Other completion effects
  still run. Published history cannot replace the team's latest execution pin.
- S4: the scanner compares snapshot blobs with HEAD. It scans changed/uncommitted
  content, excludes short values and PEM armor, and keeps encoded-value and chunk
  boundary detection. Runtime masking is unchanged.
- S5: publication reuses the private controller's existing 45-second request
  timeout. A second interrupt cancels it, prints that the run stayed local-only
  and preserves the local execution verdict. The watcher joins on completion.

Two nits marked fixed in the review were absent from this checkout. Publication
now pins executor_name to local and billing_awards indexes team/requester_account.
Each has a regression that failed before its fix. The personal-machines query is
also filtered to unrevoked, unexpired personal roots. The offline fallback's
existing real CLI/native regression and routed tests retain its one-line notice.
The pending TeamAdmin, cap-admission and default-hold decisions are unchanged.

Red evidence includes hostile config admitting one Cloud run and source upload,
three excluded claimant variants obtaining burst awards, a team claim releasing
requester values, restart resupply rejected as unknown wire input, deletion
retaining values, publication creating a pin, the scan refusing unchanged/short
inputs, publication lacking a deadline, caller executor attribution and a billing
lookup scanning other requesters. All corresponding regressions now pass.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W9 and
GOTMPDIR=/tmp/sw-astra-W9/go, with at most four Go package workers.

- Whole-repository go build -p 4 ./... passes after the final source changes.
- Vet passes for cmd/sparkwing, nodehost, controllerwire, userconfig, wingd/client,
  projectconfig, store, controller and controller/client. Final cmd/store vet passes.
- Burst, LocalSecrets, RequesterLocalSecret, Publication, RunRouting,
  HostileProject and ProjectUntracked test families pass in cmd/sparkwing,
  pkg/controller and pkg/store. The final machine-source/classification, publication
  deadline, provenance, usage-index and owner-expiry regressions pass.
- Complete userconfig, projectconfig, controllerwire and controller/client tests
  pass. Nodehost availability/secret renewal composition tests pass.
- Race checks pass for requester restart/resupply, deletion/expiry cleanup,
  claimant-secret eligibility and publication interruption.
- FuzzHostileProjectCannotAuthorizeBurst and
  FuzzBurstCLIClaimantsStayInsidePreferenceSet each pass four fixed-count
  executions with real CLI/store paths. Initial three-second runs completed no
  baseline cases and are not cited as fuzz evidence.
- Native-tag compile passes for cmd/sparkwing and pkg/controller with
  -tags rewrite_integration -run '^$'. This executes no native acceptance test.
- OpenAPI/contract, embedded-doc synchronization/privacy/link checks,
  commentcheck against main, sleepcheck against HEAD and git diff --check pass.

Broad wingd/client tests and the docs web-client tests cannot run in this sandbox:
Unix listen/dial and httptest TCP listeners return operation not permitted.
The real here-host constructor reached resupply, then met the same admission
socket refusal. No socket restriction was skipped or replaced with a factory.
The lead must run the existing ^TestBurstPublicRun family and
^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$ with
-tags rewrite_integration. A native deploy/reconnect with concurrent remote
workers remains runtime integration evidence beyond the fresh-controller proof.
No foreign implementation stub blocks these fixes. No new acceptance cell was
unskipped and no archived test was ported in this correction.

Contract changes made: C4 records restrictive project settings, machine path
approval, claimant limits, scan semantics and bounded publication. C3 adds private
owner renewal inputs. C1 records resupply authority, volatile cleanup, claimant
checks, publication provenance/pins and the usage index. Contract changes needed:
none. The lead-owned amendments and handoffs were read and left unchanged.
Existing assigned W9 implementations are retained.

Shared files outside W9 ownership:

- internal/userconfig/routing.go; pkg/projectconfig/contract.go and validate.go.
- internal/nodehost/remote_host_store.go and burst_availability_test.go;
  internal/wingd/client/api_http.go exposes its existing request timeout constant.
- pkg/controller/engine_lifecycle.go, local_secrets_test.go and burst_overflow_test.go.
- pkg/store/burst_placement.go, unit_matching.go, secret_reads.go, local_secrets.go,
  owner_reaper.go, deletion.go, run_state.go, publication_attempt.go and schema.sql;
  burst_placement_test.go, local_secrets_test.go, publication_runtime_test.go,
  publication_attempt_test.go and burst_overflow_retention_test.go.
- C1/C3/C4, api/openapi.yaml and .apidiff snapshots. Regeneration also catches
  previously merged retry/publication/profile-data shapes missing from the saved
  baselines. The generated API reference is unchanged.

No removed API requires another production caller migration. Old unsafe routing
fields/merging and the replaced scan/attribution branches account for the removed
lines. Whole legacy files deleted: 0. Production Go across this session is
+307/-84, net +223. W9 versus main is +19,125/-23,815, net -4,690, within the
-2,900 budget. Tests and contract declaration files are excluded.

Mechanisms added are the private resupply field on the existing owner heartbeat
and a publication signal watcher using the existing timeout. A persisted secret
relay, new endpoint or new timeout setting would be larger and is unnecessary.
No new environment variable, daemon, retry loop or compatibility layer was added.
Open product questions remain with the lead as listed in the review. Scratch was
removed after all checks joined. The shared build cache is retained.

STATUS: CONTINUE
