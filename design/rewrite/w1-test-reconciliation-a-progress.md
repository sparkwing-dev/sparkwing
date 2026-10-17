# W1 test reconciliation-a progress

The final assigned controller selection passes **96/96 groups**, with no failures
or skips (22.796s). All 72 App groups still pass. This closes the ten remaining
runner/source groups and adds one passing controller property. The native report
still contains FAIL and reflects the last committed state. Completion is not
claimed.

## Implemented

The default source controller now connects its configured GitHub App client to
an audited store transaction. It checks persisted live claim authority, exact
sealed numeric repository identity, current tenant installation and subscription
consent, and current provider coverage. Only the source ID and owner-approved
extra IDs receive contents:read. An App request uses its sealed installation.
A typed repository needs a live subscription. The transaction rechecks ancestry
and the claim fence after provider work and commits an audit before returning
the token. Audit, provider or authority failure returns no token.

App tokens require live source authority. Stored team credentials retain their
Cloud enrollment or machine-owner consent requirement. Both paths use the same
source fence and audit writer. Missing credentials retain useful remedies in
the shared error envelope. The response carries the App token in secret and
uses the checkout facts already in the seal.

Owner approval now binds the source repository's numeric ID as well as each
extra ID. A generated HTTP property reproduced approval transfer to a new
repository at the source's old name. The corrected lookup follows the original
ID through rename and excludes the replacement.

Runner tests use current sealed requests, runner claim polling, named unit
selection and awarded capabilities. All named/queued tenant, repository, branch
and SHA exclusions remain. The SHA and checkout URL assertions now read the awarded request seal.
The source tests retain owner-only approval, exact
content-token scope, current coverage and claim-only release assertions.
The route rejects runner-selected extras under its existing no-body contract.

## Deleted and budget

Deleted 55 production lines of retired surface: the 32-line SourceCredential /
SourceSpec response file and the 23-line unused GitHubAppExtraRepoRefs reader.
Its 25-line projection test is replaced by assertions through real release and
rename properties. No security assertion was dropped or disabled.

Session production delta under the lead's exclusions is **+149** (222 added,
73 deleted), including **+114** in store and +35 in shared controller/credential
code. Store versus main is **-2,369**, which is 1,881 over the -4,250 W1+W1b
budget. That budget remains open. The added path restores the App source release
that the route cut left absent. Its committing authorization and audit cannot
be replaced by an HTTP-only check or by opt-in to a stored team credential.
The existing scheduled-origin cut remains the main legacy intake work left.

## Contract changes made

C1 records ReleaseGitHubSourceCredential and the two-method GitHubSourceProvider
interface used by the existing client and store fake. It records the positive
source repository_id in github_app_extra_repos, numeric replacement/release,
shared fences/audit and removal of the old response and reader. This changes
the disposable baseline. No migration is added.

C2 records trusted SourceReleaseRequest.GitHubApp provenance. Only the verified
C1 App path sets it. It exempts App tokens from the additional stored-credential
machine consent rule without supplying grants or relaxing source liveness,
repository scope, audience or binding. SourceController.GitHub supplies the
existing provider adapter. The wire response stays on kind/host/secret.

Assumptions / mechanisms added: one provider interface is needed because the
store must recheck authority after provider I/O before committing its audit.
The smaller HTTP-only mint would lose that boundary. One provenance bit keeps
the shared source policy's existing App/stored distinction. The source ID column
makes name-reuse approval transfer impossible. Undo is to remove App release and
these additions together, reopening the restored source tests. No new package,
flag, setting, environment variable, quota, cache or compatibility path is added.
Lead-owned amendments and handoffs are unchanged. The lead reviews the C1/C2
amendments. No further contract change is requested.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and -p 4. Scratch and GOTMPDIR are under
/tmp/sw-astra-W1.

- Whole-repository go build ./... passes after the production edits.
- Scoped vet passes for store, controller, controller/client, capability and source.
- Final assigned controller selection passes 96/96 groups, zero skips, 22.796s.
- Focused store source, consent, App mutation, push-boundary and two native-report
  regressions pass in 7.292s. The source/push race selection passes in 102.271s.
- Final strengthened source SHA/ID assertions pass in 3.798s.
- Controller source and HTTP source acceptance checks pass after the last
  controller simplification (5.088s and 0.046s).
- Complete capability, controller/client and docs suites pass in 0.098s,
  0.079s and 1.400s. Embedded docs match source docs.
- cmd/sparkwing-controller and internal/web pass in 0.916s and 1.425s.
- Baseline dialect-shape and rewrite-schema checks pass in 0.224s.
- Commentcheck is clean. git diff --check passes.
- The rename property fails before the source-ID change. A temporary source
  overlay removing post-mint authority/lease rechecks makes the generated store
  property fail at input mode 9. The real source passes. Overlays do not edit
  production source and are removed with scratch.

The controller/provider component checks use a temporary standard-library
httptest overlay with net.Pipe and the default HTTP transport routed to those
listeners. Real routers, provider fakes, authorization, issuance and SQLite
transactions run. This is component evidence, not native socket proof.
The lead owns native testing. No new acceptance-matrix native cell is claimed.
The existing SourceReleaseHTTPUsesCommittedAuditBeforeOpening,
SourceReleaseHTTPRefusalsNeverOpenCredential and SourceMachineConsentHTTPUsesAccountAuthority
checks still pass with their full assertions.

## Remaining work and real integration points

No assigned controller failure remains in the local selection. The supplied
native report has FAIL and must be refreshed after the lead commits. Other
controller groups belong to the other worker and were not changed. Unchanged
bin, cmd/sparkwing and internal/wingd suites were not repeated. Their native
results remain with the harness.

Runner claim tests fake only enrolled resource observations and software policy.
Real enrollment, signed runner-policy composition and native runtime execution
remain separate integration evidence. Provider behavior uses the existing fake
GitHub API, not a live installation. Native sockets, PostgreSQL and real-repo
QA remain lead evidence.

The earlier W1 scheduled-origin handoff stays open: schedules must enter the
single sealed intake before the remaining generic trigger insertion APIs can
be deleted. This session does not alter the other worker's failing cron groups.
Completed enrollment, rotation, placement, metering and connection work is not
repeated.

## Shared files touched

- design/rewrite/contracts/c1-data.md
- design/rewrite/contracts/c2-credentials.md
- design/rewrite/test-reconciliation.md
- docs/git-credentials.md
- docs/github-app.md
- internal/capability/source.go
- pkg/controller/claim_source_test.go
- pkg/controller/github_app_extra_repos.go
- pkg/controller/github_app_test.go
- pkg/controller/github_claim_fixture_test.go
- pkg/controller/github_runners_test.go
- pkg/controller/source_controller.go
- pkg/controller/source_credential_test.go
- pkg/docs/mirror/git-credentials.md
- pkg/docs/mirror/github-app.md

Store files changed: github_source_release.go and its new tests, source_release.go,
github_app_extra_repos.go, schema.sql, github_app_authorization_fixture_test.go,
github_app_mutations_test.go, github_app_automation_test.go and
declared_repo_guard_test.go. source_credential.go and claim_source_test.go are
removed. These are the release implementation, obsolete surface deletion,
source-ID schema/approval change and minimal caller/fixture migrations.

Friction: socket restrictions require component transport substitution and
lead-owned native evidence. The supplied native report still contains FAIL.
No Git writes, external messages, Sparkwing candidate execution or harness
memory writes occurred. The shared Git config has no identity or core.worktree
overrides. Scratch is removed at the end; the shared Go cache is retained.

STATUS: CONTINUE
