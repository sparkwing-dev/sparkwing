# W9 burst native-pipe verification

Starting HEAD: `5caecc401`. This continues the preceding W9 reports.
No Git writes, operational Sparkwing commands, installation, deployment or
release ran. The required read-only Sparkwing info ran.

## Implemented

- Resolved the contents of all three conflicted files: C4 and the source and
  embedded migration guide. Both the burst and incoming Cloud-chart amendments
  survive. Incoming deleted files stay deleted. The lead-owned amendments and
  handoffs are identical between HEAD and MERGE_HEAD and were not edited.
- Cloud connection setup now shares the pre-intake local fallback used by the
  identity probe and source upload. Its regression failed before the fix.
  Network/unavailable failures print the notice and run locally. Forbidden and
  cancelled connections stay errors. A failed connection needs no close callback.
- Generated command descriptions preserve level-three headings. The run docs
  now expose the burst-and-overflow section anchor. The regression failed before
  the renderer change. Higher description headings remain escaped. References
  and embedded mirrors were regenerated through their existing owners.
- Here, personal-machine and exchanged Cloud awards now execute the real Go SDK
  operations in a subprocess through NativeProcesses and ProtocolSession.
  Each path retains real intake, source upload/acquisition, SQLite, claims,
  private-state transport, delegation, secret release, attempts and completion.
  Raw and base64 secret logs are masked, terminal claims lose read authority,
  and the native process and test HTTP service join before return.

The native subprocess uses a test-binary entry and sdkserve.Serve on inherited
request/frame pipes. The test HTTP transport serializes requests and responses
through private FIFOs, whose bytes remain in kernel memory. It changes no
runtime transport. The separate rewrite_integration variant uses loopback TCP.
SDK compilation from the uploaded bundle and selected-engine installation are
still outside this fixture. Their source and engine boundaries are fixtures.

## Supported topology and contract changes made

- C1 records the existing Cloud chart boundary: one controller replica and a
  Recreate rollout. Intake, plan validation and secret release therefore use
  one Store registry. Multi-replica secret delivery remains unsupported. No new
  replica limit, relay, persistence, secret value field or restart policy was added.
- C3 records the subprocess verification boundary without changing wire types.
- C4 records connection-stage fallback and generated section-heading behavior.
- Source run docs and changelog describe the fallback and topology. Declared
  requester secrets still disappear on controller restart and fail explicitly.

No new foreign API change is required. The earlier committed retry-selection
DESIGN remains open. It was not patched again. Earlier closed owner/eval and
launcher composition findings retain the status in W9-BURST-FINALIZATION.md.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package workers.

Passed:

- Whole-repository build; vet on cmd/sparkwing and pkg/controller.
- Complete clisurface, controllerwire, controller/client and doccheck suites.
- Focused CLI burst, route, here, overflow, source, registry, reference and
  Markdown tests; controller burst, personal enrollment and requester-secret
  tests; store burst placement, personal authority, overflow and local secrets.
- Three existing in-process SDK placement flows and three native-pipe flows.
  Both sets also pass under the race detector. CLI fallback race checks pass.
- Fallback ownership fuzz campaign: 8,215 executions in three seconds.
- Surface, here resume/no-retry and credential-policy acceptance selections.
  No matrix cell was newly unskipped; no archived test was newly ported.
- Tagged controller test compilation, reference regeneration, embedded docs and
  changelog drift, diff whitespace and commentcheck against HEAD (15 Go files).

Commentcheck against main still fails on the inherited pinnedClock comment at
pkg/store/store.go:108. The original TCP subprocess probe reached its listener
and was refused by the sandbox. The final TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards
variant compiles but still needs a native run. An overbroad CLI docs selection
also reached an unrelated httptest listener refusal; its socket-free focused
replacement and the full doccheck suite pass. No whole-repository test pass is
claimed. The FIFO fixture's initial argument/bootstrap and response-length
mistakes were corrected before its ordinary and race passes.

## Remaining work and lead handoff

- The three conflict contents are resolved. Their index entries remain unmerged
  because Git writes are prohibited and the worktree Git metadata is read-only.
  The lead must stage the resolved files and finish the existing merge.
- Run the TCP subprocess variant with `go test -tags rewrite_integration
  ./pkg/controller -run '^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$'`.
- Full native integration still needs configured public run, SDK build from the
  edited uploaded source, selected engine installation, here scheduler/log
  streaming, registered runner and Cloud Job execution, Ctrl-C, artifact
  retrieval and competing local admission. These are the remaining concrete
  gaps before claiming all five features end to end through the public CLI.
- Existing retry-mode replay DESIGN remains a lead decision, independent of
  these burst tests. Multi-replica support would need a new ephemeral-delivery
  contract. One-controller launch verification needs no relay.

## Size, shared files and friction

Production Go this session: +4/-5, net -1. No legacy subsystem was replaced;
earlier cuts stand. W9 versus main: +18,344/-23,824, net -5,480, within -2,900.
Counts exclude tests, testdata, acceptance and contract declarations. No removed
API leaves callers behind.

Shared files touched: pkg/controller/burst_sdk_execution_test.go and the three
new burst_sdk_{native,pipe,http_integration}_test.go files; C1, C3 controller
wire and C4 contract docs; CHANGELOG.md and generated pkg/docs mirrors. The
incoming W8 chart and launcher changes were retained without further edits.

Mechanisms added: test-only native SDK and FIFO transport fixtures. Runtime
changes reuse the existing fallback and documentation renderer. No environment
variable, service, runtime interface, budget default or secret store was added.

Friction: sandbox listeners, Git metadata restrictions, the inherited main
commentcheck finding and Overwing's reported ten-commit source lag. Local Git
identity/core.worktree overrides are absent. Checks were joined; worker scratch
is removed at the end. The shared build cache is retained.

STATUS: CONTINUE
