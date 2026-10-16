# W9 native CLI QA follow-up

Items 1, 2 and 4-10 are handled. Item 3 remains unfinished. This report does not
close the working-tree submission slice or claim native deployment acceptance.
No operator onboarding route was built. No Git writes were performed.

## Completed

| Item | Result and evidence |
| --- | --- |
| 1 | The controller emits `controllerwire.Runners` and `RunnerDetail`, including structured run/job IDs, label arrays and the shared headroom shape. Deleted `Agent` and `AgentHeadroom`. `TestRunnerInventoryRealClient` exercises the real controller route with the actual reader. `TestRunnerInventoryHTTPUsesStoreSnapshot` now also reaches that reader from real SQLite. It exposed an unknown location observation, which the reader now accepts without inventing a placement. No production Go `agents` JSON field remains. |
| 2 | `prepareNativeTrigger` supplies a production GitHub repository API resolver. It requires a positive numeric ID, uses captured `GH_TOKEN` or `GITHUB_TOKEN` for private repositories, refuses redirects and never prints provider bodies. `TestNativeTriggerUsesProductionGitHubLookup` reaches the lookup through native preparation. `TestGitHubLookupSuppliesCanonicalID` carries the actual API adapter through source preparation. `TestGitHubLookupNeverForwardsOrPrintsCredentials` generates 100 credential inputs. |
| 4 | `tokens create --type` accepts only `member`. Runner listing, rotation and revocation remain available. Enrollment stays under `runners add`. The parser regression and generated token documentation cover the cut. |
| 5 | Profile probes retain wrapped canonical controller errors. Configuration fields, invalid controller URLs and the isolated-config write guard use `bad_request`. Operator ceilings use `limit_exceeded`; cancellation/deadlines retain their canonical codes. Tests cover 401/403 codes and details through profile connection and JSON rendering, real malformed config through profiles, and operator ceiling errors. |
| 6 | Profile authentication uses `/api/v1/auth/whoami`, through new `client.CheckIdentity`. Real controller tests accept member, machine and deployment credentials while the deployment credential still receives 403 for team runs. Existing profile probe fixtures now use the identity route. |
| 7 | Controller `help`, `--help` and `-h` return before opening credentials/state. Unexpected positionals print flags and fail before startup. The regression checks the actual `run` entrypoint and an untouched runtime home. |
| 8 | This branch's AGENTS.md and info already both use `--search`. Added `TestAgentSearchExamplesMatchTheRegistry` to compare both documents and parse their command/docs examples through the real registry. The installed CLI has older discovery text and was not treated as this branch's source of truth. |
| 9 | Removed the unauthenticated legacy admin mint from `bin/k8s-e2e.sh`. The provisioning step now fails with `PENDING operator onboarding`, identifying the missing team plus first team-admin route. `bash -n` passes. |
| 10 | Updated self-hosting, auth bootstrap, getting-started, deployment-modes and credentials. They distinguish deployment bootstrap from team authority, state pending onboarding, use identity probing, and require explicit `SPARKWING_CONFIG` alongside isolated `SPARKWING_HOME` for profile writes. Working-tree submission is marked unfinished rather than advertised as operational. Updated embedded mirrors and token help. The setup-doc regression reads the embedded pages; mirror drift passes. |

The credential invariant tested here is: a captured GitHub credential is sent
only to the selected GitHub API request and never appears in lookup diagnostics.
Profile identity success grants no additional team authority. Existing runner
tenant property tests remain green.

## Item 3: remaining implementation

`prepareNativeTrigger` still refuses working-tree bundles. Snapshot capture and
its tests already exist, but no production pre-intake storage binding exists.
This is owned end to end by W9 under the integration assignment, not handed off
as a blocker for another worker.

The inspected constraints are:

- C1/C2 storage accepts job, owner-unit or publication upload identities, each
  tied to a persisted run. `blob_receipts.run_id` references `runs`.
- `ResolveBlobAuthority` requires persisted data scope and resolves its run.
  A requesting member's `runs.write` credential is not an execution/data grant.
- A snapshot must upload before executable intake can award the run. Creating
  a synthetic publication run would change the meaning of publication and add
  an extra visible run.
- Current binary/source-bundle read edges require the exact uploader identity.
  Upload alone therefore cannot make a pre-intake bundle readable by a later
  awarded owner. Signed source URLs also have to work on the supported
  filesystem transport, not just HTTPS S3.

The remaining change must provide a persisted pre-intake source-upload binding
with transactional member/repository authorization, shared upload reservation,
verified commit/accounting and retention. Intake must bind its committed digest
to the sealed repository/source revision. A later claim may read only that
sealed digest, with current ancestry and tenant checks. It must carry no App
trust. Exercise forged repository/digest/tenant and revoked authority with a
property test, then carry a real snapshot through the controller, shared object
backend, intake and awarded source acquisition. This needs minimal C1/C2/C3/C4
contract amendments under the existing integration authority. No placeholder
route, fake publication run or authority bypass was added in this session.

## Contract changes made

- `c3-controller-wire.md`: shared runners response, unknown location observation,
  identity probe, member-only token creation and production numeric-ID lookup.
- `c4-surface-config.md`: token creation cut, profile/error semantics, controller
  argument handling and isolated profile configuration.
- No lead-owned handoffs or amendments file was changed. No foreign store or
  capability signature was changed. Item 3's contract work remains to be done.

## Verification

All checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache` and private scratch under `/tmp/sw-astra-W9`.
`go test` used at most four package workers.

- Repository-wide `go build -p 4 ./...`: pass.
- Focused tests across CLI, controller, client, registry, acceptance and embedded
  docs: pass. This includes all `TestSurface*` component contracts, runner
  HTTP/SQLite/client tests, profile probe/config tests and GitHub regressions.
- `go vet -p 4` for controller, client, both changed commands, registry and docs:
  pass.
- `go run ./internal/commentcheck -base main .`: clean across 2,614 files.
- `bash -n bin/k8s-e2e.sh` and `git diff --check`: pass.
- Setup documentation and embedded mirror drift: pass.

No archived tests were newly ported. No native Kubernetes or full pipeline
acceptance was rerun. The lead still owns native QA and whole-repository gates.
Earlier W9 handoff work remains as recorded in its preceding reports.

## Files, deletion and budget

W9-owned production Go: session +147/-18, net +129. Cumulative W9 delta versus
main is -6,713, within the -2,900 budget. Across this session's production Go in
all areas: +168/-48, net +120. Deletions include the two obsolete runner DTOs and
the old runs-based profile authentication call. The legacy unauthenticated
Kubernetes token-mint recipe is also removed.

Shared implementation/test files outside W9:
`pkg/controller/agents.go`, `pkg/controller/runner_inventory_test.go`,
`cmd/sparkwing-controller/main.go`, `cmd/sparkwing-controller/argument_test.go`,
and `bin/k8s-e2e.sh`. Embedded copies under `pkg/docs/mirror` mirror source docs.
No removed API left a caller unmigrated; the repository build passes.

Friction: the installed CLI differs from the rewrite; docs have an embedded
mirror that must be synchronized; the actual store can report an unknown runner
location. Working-tree storage requires more than CLI wiring, as detailed above.
Scratch is removed at the end of the session. The shared build cache is retained.

STATUS: CONTINUE
