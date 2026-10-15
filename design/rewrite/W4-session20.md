# W4 continuation report

Continues `d52b60257` and `W4-session19.md`.

`nodeproto.ValidatePlan` now refuses empty and unknown admission classes with
`plan_invalid`. It accepts `critical`, `interactive`, `normal` and `batch`,
matching the retained Go builder. Omission leaves inference to the host.
This adds no setting, interface, environment variable or limit.

The acceptance regression exercises the shared validator for all six
placements. The 24 invalid-class cases failed before the fix. All 54 cases
pass after it, including valid classes and omission. These prove validator
parity, not real host integration. The C3 protocol notes now name the
existing class vocabulary explicitly. No other area's contract changed.

Deleted zero additional lines. Earlier hard cuts remain intact. The W4 Go
source scan found no Old-suffixed scaffolding or unimplemented markers.
No additional inventory test needed porting. The existing conformance,
schema snapshots, secret-identifier port and removed-API compile probes pass.

Verification used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-astra-W4/cache` and both `GOTMPDIR` and `TMPDIR` set to
`/tmp/sw-astra-W4/tmp`. From this worktree, the following checks passed:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v
```

The Linux hello, version/hard-cut and fake-host/linkage target cells pass.
The latter still skips its HTTP-route subprocess test because sockets are
denied. Mask's SDK service-contract subtest passes. Its target cell remains
skipped because C2 lacks an acknowledged registration route contract.
No additional complete target cell was unskipped in this session.

The nine package-test exclusions are inherited. C2's `apierr.DecodeInner`
still panics as unimplemented, affecting two tests. Three fileset tests
encounter the sandbox's empty ancestor `/tmp/.git`. Four service tests need
sockets. These excluded cases were not rerun. Native Windows/macOS, W5 plan
parity and other host placements remain unverified here. The lead retains
pipeline and gate execution.

Contract changes needed in other areas remain C2's Mask method, route,
payload, response and authorization. The SDK cannot invent that route.
Existing fake services exercise the declared boundary. Real integration
remains open for W2 error decoding and mask registration, W6 environment
and limit composition, W3 latest-output/child handlers, and W3/C1 persisted
expansion/reconstruction hashes, claims, lifecycle and artifact authority.
This continuation does not substitute those implementations with W4 code.

Open questions remain the lead's approval SkipIf reading versus removed
executable hooks, and newly registered masks matching accepted identifiers.

The only production file changed is `internal/nodeproto/plan_validation.go`.
Shared files touched are `tests/acceptance/protocol_hard_cut_test.go`,
`design/rewrite/contracts/c3-protocol-notes.md` and this report. No other
area's production caller needed migration.

Friction remains denied sockets, empty `/tmp/.git` and Overwing's stale
binary warning. The shell rejected `rm -rf` cleanup. Python removed the
owned scratch directory successfully. No Sparkwing executable or Git write ran. Shared Git config
has no injected identity or worktree setting. All checks finished. Scratch
was deleted after verification. Formatting and diff whitespace checks pass.

STATUS: CONTINUE
