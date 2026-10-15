W2's five assigned Opus findings are fixed. Finding 3 remains a lead decision
and was not changed.

| Finding | Status | Evidence |
| --- | --- | --- |
| 1 | FIXED | Only a direct account session or deployment authority may provision a machine root, and its grants must be nodes.claim or triggers.claim. The real-store derived-session regression failed first by returning a bearer with no error. The transaction now uses DirectAccountSession and checks the grant allow-list. |
| 2 | FIXED | The committing-origin property creates derived sessions through CreateCapabilitySession and requires Forbidden for machine roots, member roots and owner consent. It also refuses account bearers as machine origins. |
| 3 | LEAD DECISION | Untouched. |
| 4 | FIXED | Bootstrap refuses a second seed whenever a live administrator exists, regardless of the once-only marker. TestBootstrapSeedStoreOnceProperty now removes the marker while keeping the administrator live and checks refusal. |
| 5 | FIXED | Sign-in cannot discard data scope. CreateCapabilitySession and verifyLoginSubmit refuse every non-nil data scope. Generated store and HTTP properties pass. Removing only the HTTP guard through an overlay reproduces HTTP 303 and an added session. |
| 6 | FIXED | Local authority comes from the accepted transport identity. The controller formats that identity without comparing it with itself. The connection-accept UID/SID check remains the authority boundary. Existing native and injected local HTTP properties pass. |

The grant property exercises every declared grant through both direct-account
and deployment origins, with generated grant combinations. Only the two claim
grants succeed. The origin property generates 64 account capabilities and
creates their sessions through the real SQLite store. The data-scope store
property generates 32 repository/ref scopes and checks that no session persists.
The HTTP property covers remote-cache and publish scopes using authentication
snapshots over a real session store. It tests the verifier separately from the
store's committing check.

Production delta for this fix is **+12 lines**: 23 added and 11 replaced lines
removed. W2 accounts for +7 and shared store code for +5. No production files or
legacy subsystems were deleted in this bounded fix. W2's total production delta
against main is **-2,829**, using the lead's exclusions for tests, testdata,
acceptance harnesses and contract-named files. Its budget is -250.

Shared store files touched:

- pkg/store/authority_provision.go
- pkg/store/auth.go
- pkg/store/authority_origin_test.go
- pkg/store/auth_scope_test.go (new)

Other files touched:

- pkg/controller/browser_login_routes.go
- pkg/controller/local_http.go
- pkg/controller/bootstrap_store_test.go
- tests/acceptance/login_http_test.go
- docs/auth.md and pkg/docs/mirror/auth.md
- design/rewrite/contracts/c2-credentials.md
- design/rewrite/W2-opus-review-fixes.md (this report)

The C2 prose now describes the enforced machine-origin and sign-in rules. No Go
contract, schema, cross-area interface or lead-owned handoff file changed. No
new mechanism, contract request or open decision was added. Existing tests were
strengthened; no legacy test was ported or deleted. No skipped native acceptance
cell was promoted.

All Go checks used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp. Tests used -p 4.
Run the following with go -C /home/bombuntu/.xwing/envs/1080/sparkwing:

- build ./... and vet ./... pass.
- test -p 4 ./pkg/store -run 'Test(CommittingAccountOriginProperty|MachineProvisionGrantsProperty|CapabilitySession|Session)' -count=1 passes.
- test -p 4 ./pkg/controller -run 'Test(Bootstrap|LocalControllerHTTP|PolicyPeer|Peer)' -count=1 passes.
- test -p 4 ./tests/acceptance -run 'Test(LoginHTTP|RootProvisioning|Session)' -count=1 passes.
- test -p 4 ./internal/capability ./internal/apierr -count=1 passes.
- test -p 4 ./pkg/docs ./cmd/sparkwing -run 'Test(PkgDocsContentMatchesDocsRoot|EmbeddedChangelogMatchesRoot|AuthGuide)' -count=1 passes.
- run ./internal/commentcheck -base main . passes across 2,195 files.
- git diff --check passes.

The full pkg/store suite fails on legacy schema/token tests, Cloud metering,
tenant table classification and SQL guards. It ends with a nil-pointer panic in
TestListTokens_FiltersKindAndRevoked. An overlay restoring both changed store
production files to HEAD reproduces the heartbeat acquire failure, missing
args_json and tokens schema errors, and Cloud metering failures. These belong
to the existing store rewrite work and were left outside this fix.

No git writes were made. Scratch overlays and GOTMPDIR are removed after checks;
the shared GOCACHE is retained. Friction: one mistaken installed sparkwing info
invocation ran during bootstrap before the task's no-binaries rule was applied.
No candidate, daemon, install or pipeline command ran.

STATUS: COMPLETE
