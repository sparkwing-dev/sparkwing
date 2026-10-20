# W2 continuation after the host merge

HEAD is 1d8a5e89e. The previous W2 implementation and merge resolutions are
committed. This continuation changes two controller test files and the port
inventory. It makes no production or internal/store edit.

Two console-settings tests failed because their fake session had no committed
ancestry. They now create a real account session, seal its CSRF key and use the
default authentication, console gate and SQLite committing transaction. They
retain scalar and installed-table writes, explicit zero, omitted fields,
validation failures and unchanged settings after refusal.

The old runner-error test called two deleted routes. Its replacement calls the
final runner inventory and mixed-claim routes with real persisted credentials.
The invariant is that an untyped dependency failure exposes no private message
in an HTTP response. Generated private messages reach fake inventory and runner
policy dependencies through the real router. Both routes return the canonical
internal error and no-store. No production authorization check was weakened.

The test diff removes 49 lines and adds 63, net +14. It removes the fake console
session/CSRF setup and the old route assertions. Production deletion in this
continuation is zero because the credential route cut is already committed.
W2 production Go versus main is +14,353/-17,191, net -2,838, within the -250
budget under the lead's ownership exclusions. The incoming host merge accounts
for the three-line difference from the preceding report.

| Lead item | Current state |
| --- | --- |
| (a) Committing origin | Complete. The concrete root-issuance and owner-consent transactions share DirectAccountSession. TestCommittingAccountOriginProperty passes. |
| (b) Production composition | Bootstrap, console, local peer, launcher snapshot and data authority/access are implemented. Non-checkout events reach W1b SettlePayment. The final payment receipt/correlation schema and trusted billing-page writer remain absent. |
| (c) Declared references | Complete in the earlier commit. No change in this continuation. |
| (d) Dashboard source and resume | Complete in the earlier commit. Native resume tests pass. |
| (e) Registrations | Nineteen of 23 are implemented. The four below still need foreign contracts. Queue-priority HTTP uses its declared writer with fakes, awaiting C1 persistence and ordering. |

The four missing registrations still fail TestFinalCredentialServingRegistrations
in both metrics configurations:

- GET /api/v1/runs/{run}/jobs/{job}/output
- GET /api/v1/runs/{run}/children/{child}/jobs/{job}/output
- POST /api/v1/runs/{run}/jobs/{job}/claim/input
- POST /api/v1/operator/teams/{team}/refunds

Contract changes needed in other areas remain the same as the prior report.
C1/W7 must supply a member-session blob read edge and a child-output edge.
NodeRow.OutputRecord supplies the exact selector, but ResolveBlobAuthority only
accepts capability data authority. C1/W9 must define the native claim-input
request and record projection. The remaining legacy ClaimInput/ClaimToken path
is not a final credential adapter. C1/W1b/W9 must define the human refund request
and its durable provider/ledger join. ReversePayment is removed and verified
provider settlement cannot stand for a human refund request. No policy row was
removed, and no placeholder handler was registered.

C1 still needs the integer queue-priority persistence/ordering contract and the
payment schema/writer described in c1-data.md. The W2 interfaces and fake-backed
HTTP tests remain in place. There is no contract amendment in this continuation.
Other unchanged integration points remain authority-bearing local and
credential/account/team mutations, roleless sessions, trusted schedule/App
intake, local-first matching, runner snapshots and final remote tagged-log
transport. The prior report contains their interface-level evidence. A1-01,
03, 05, 08, 14-16, 19-28, 31, 34, 38-40, 49, 53 and 55 retain their implemented
W2 halves. Their other owners' runtime, persistence and topology obligations are
not claimed as complete.

W1's separate cancellation snapshot is integrated. The concrete generated
precedence test passes, including an empty job reason overriding run intent.
Holder result rights remain separate from sensitive-read cancellation.

No shared production file or internal/store file was touched in this continuation.
The earlier vertical changes remain in authority_provision.go, source_release.go,
authority_bootstrap.go, authentication_binding.go, blob_authority.go,
blob_authority_projection.go, blob_data_access.go, launcher_snapshot.go,
binding_execution.go, latest_run.go, debug_pause_resume.go, console.go and their tests. The prior
merge report lists the shared finance resolutions. Lead-owned handoffs.md and
amendments-*.md remain unchanged.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2. Tests use -p 4.

- Whole-repository go build ./... passes.
- go vet ./internal/controller and commentcheck -base main pass.
- Full capability, apierr, apiroutes and controller/client suites pass.
- Focused console settings, console commits, runner error properties, bootstrap,
  launcher sync, resume, credential ports and native log tests pass.
- The seven socket-free metrics tests, trigger validation and receipt-not-found
  pass. Dedicated metrics listener tests still require the lead's socket-enabled
  environment. The two obsolete tests remain deleted by the earlier commit.
- Store origin and cancellation-intent precedence properties pass.
- Credential, root, payment, source and session executable acceptance selections
  pass. The two existing SQLite credential proofs pass. No new matrix cell is
  claimed and pending native-placement scenarios remain pending.
- Native TypeScript passes with go -C sdk/typescript test -p 4 ./test/nativehost
  -run TestNative -count=1 -timeout=90s. No setup failure occurred.

The initial broad controller run found the fixed settings/runner test failures,
four known schedule/intake failures and then stopped at
TestDrainGitHubChecks_DoesNotInheritASpentBudget with
listen tcp6 [::1]:0: socket: operation not permitted. Three schedule tests need a
resolved GitHub repository ID. The fourth calls the legacy CreateTrigger path
and gets triggers has no column named pipeline. Trusted intake is W1 item (g).
No whole controller suite, PostgreSQL, provider network, native placement matrix
or runtime launcher integration pass is claimed.

Changed files are internal/controller/credit_settings_http_test.go,
internal/controller/error_response_test.go, design/rewrite/contracts/t1-port-inventory.md
and this report. There are no new production mechanisms, limits or settings.
No Git write or Sparkwing runtime was run. Git identity remains Korey Gambill and
core.worktree is unset. Overwing reports its installed binary is ten commits
behind source. The task scratch directory is removed after verification and
the shared build cache is retained.

STATUS: CONTINUE
