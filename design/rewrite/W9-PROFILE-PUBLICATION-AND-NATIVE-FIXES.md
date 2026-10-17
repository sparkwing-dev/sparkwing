# W9 profile publication and native fixes

Base: `9e15f2d61`. No Git writes. Lead-owned amendments, handoffs and retry-selection DESIGN are unchanged.

## Implemented

- Ordinary account profiles construct temporary repository cache readers through POST /tokens. Read authority narrows to data.read and verified-default records. Publication narrows write authority to the account's own publish intake, repository and manual ref. Issuance and live ancestry share the same grant rule. Revocation, membership, repository scope and expiry remain effective.
- Local run constructs and closes its profile cache before and after hosting. Background hosts construct the reader from sealed facts. Fresh attempt storage preserves it only for the sealed local opt-in.
- Local publication admits non-executing history, imports a terminal local attempt, uploads selected output/artifact bytes through signed storage, and uploads retained logs through the shared log uploader. It preserves reasons, masked errors, exit codes and structured failures. Generated topology is flattened to explicit dependencies. Attached and background host composition call publication after local completion. A publication error warns and preserves the local verdict.
- The existing snapshot route exports completed state and selected records only to the verified operating-system peer. No new route or public flag was added.
- The cross-job artifact refusal is reproduced through the real router and claimed-node client. Node snapshots now include minimal terminal manifest selectors for declared consumers. The client reads those projections without obtaining another node's private state or credential. Typed dependency outputs reuse the existing authorized output API.

## Native failures

The cancellation fixture was wrong against the documented SDK surface: argument fields use `flag:"block"`, while it used `sw:"block"`. Native preparation with --block reproduced the exact unknown-flag failure, then passed after correcting the tag. The test now asserts the resolved boolean argument. The -- separator remains intact.

The personal-machine assertion raced a zero hold and the runner's first poll. The fixture now waits for its first authenticated claim poll and gives the machine a documented preference hold. The contended case still occupies actual local capacity. No placement product setting was added or changed.

The artifact projection property went red on the same local-host refusal as the native consumer. It now passes, including generated undeclared and nonterminal sources and checks that producer declarations and credentials stay absent. Actual signed byte reads retain C1's separate edge checks.

The remaining CLI Output call in the fallback probe now prints stderr on failure. A native public --publish probe was added to the existing TestBurstPublicRun family. Its local fixture removes the cloud-source-only dotenv sentinel.

The lead's W9-burst-native-latest.md still contains FAIL. Sandbox native probes compile and skip at the socket boundary. No native acceptance cell is closed here, and this report is not COMPLETE.

## Contracts and mechanisms

C1/C2/C3/C4 contracts record the changes. New mechanisms are the scoped account data opt-in, verified-peer snapshot alternative, and publish_attempt transition. The smaller alternatives of executing an imported run or exposing producer private rows would grant execution or private state. Undo requires reverting each binding with its consumers. No database schema, grant vocabulary, route, CLI flag, environment variable, service or limit was added.

Publisher result proofs retain the publisher's zero attempt independently of the imported execution attempt. Executable secret preflight stays enforced. Non-executing publication does not require execution secrets. The shared stored-result reader was extracted for publication without changing retry selection or materialization policy. Typed output process services now reuse the existing authorized output endpoint.

## Evidence

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W9 and GOTMPDIR=/tmp/sw-astra-W9/go, with at most -p 4.

Passed: whole-repository build; focused vet; full short capability/controllerwire/client/runspec suites; generated API/schema and route-parser tests; focused controller/store authority, data, publication, result and materialization suites; CLI local/burst and nodehost executor/cache suites; localcontroller artifact/process and native-client output selections; focused race checks; commentcheck against main; sleepcheck against HEAD; full doccheck; mirror and whitespace checks. Native source preparation builds the edited SDK, seals --block=true, uploads its bundle and reaches real intake without sockets.

Acceptance selectors pass for credential policy alternatives, surface contracts, storage-publish-one-way and storage-local-remote-cache-opt-in components. Their old cache fake now supplies the documented verified-default observation and removes private RefFacts. Three API-route expectations were stale against the existing policy alternatives and were corrected. A typed-output fixture now supplies the required capacity observation for simultaneous claims. No archived test was newly ported and no native scenario was unskipped.

The exploratory broad short suites also exposed existing cron fixtures using removed trigger columns and unresolved GitHub IDs, and socket panics in compile/GitHub-check tests. A transient missing shared Go-cache object interrupted a build and native preparation; both passed on rerun. The shared cache was retained.

## Volume and shared files

Production versus HEAD: +817/-80, net +737, excluding tests, testdata and contract declaration files. W9-owned production: +521/-18, net +503. W9 versus main: +19,010/-23,818, net **-4,808**, inside the -2,900 budget. The new volume implements the previously missing profile/publication slice. Burst-named production files are unchanged. Deleted code includes duplicate transition conversion, duplicate data-authority construction, the private-row typed-output read and the pre-intake publication refusal.

Shared production files: internal/apispec/wire.go; internal/capability/{issuance,policy,routes}.go; internal/runspec/publication.go; internal/nodehost/executor.go; internal/wingd/localcontroller/native_client.go; pkg/controller/{credential_issuance,data_authority,data_request_binding,engine_lifecycle,private_host}.go; pkg/store/{authority_binding,authority_delegate,blob_authority_projection,capabilities_contract,local_publication,node_state,publication_attempt,result_proof,secret_preflight,state_contract,state_materialize}.go. Shared tests are the controller projection/publication/output/route fixtures, store publication tests, nodehost/cache and capability properties, API-route expectations, and acceptance policy/cache fixtures. API generation, source docs, changelog and embedded mirrors were updated. No API was removed that needed foreign caller migration.

## Lead integration

Rerun the existing native commands with no skipped children. The first also covers the new publication probe:

```bash
env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  TMPDIR=/var/tmp/sw-lead-tmp GOTMPDIR=/var/tmp/sw-lead-tmp \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./cmd/sparkwing -run '^TestBurstPublicRun' \
  -count=1 -v -timeout=15m

env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  TMPDIR=/var/tmp/sw-lead-tmp GOTMPDIR=/var/tmp/sw-lead-tmp \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./pkg/controller \
  -run '^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$' \
  -count=1 -v -timeout=5m
```

Native detached publication and a real profile-backed cache through process hosting still need evidence. Deployed Cloud/DinD/network/object integration and native Darwin/Windows/PostgreSQL remain the earlier open integration points. No new foreign contract request is blocked. No installation, deployment, release, operational state change or Git mutation ran. Scratch is removed at session end, while the shared cache stays.

STATUS: CONTINUE
