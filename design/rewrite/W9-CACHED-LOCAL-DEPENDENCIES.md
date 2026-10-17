# W9 cached local dependency preparation

Base: `aece4e39e`. No Git writes. Lead-owned amendments and handoffs are unchanged.
The supplied native report has no FAIL or SKIP for the previous committed state.
These changes still need the lead's review, commit and fresh harness run.

The reported command ran red before changes:
`go test -p 4 ./cmd/sparkwing -run '^TestPipelineToolchainOverrideStaysInTheBuild$' -count=1`.
Its cached child returned `prepare pipeline dependencies: exit status 1`.

History identifies both contributing changes. `5b9fa601a` moved local
`pipelineBinary` into the shared `bincache.MaterializePipeline`, which prepared
dependencies before consulting the cache. Burst commit `f01cc8239` replaced
`go mod download all` with `go list -deps`. The fixture accepted `mod` but
rejected `list`. Its `cached` case is a cold cache-enabled build, not a cache hit.
C3 already documents imported-package preparation to stabilize checksums before
hashing. The real installer tests establish why that cold call remains required.

Invariant: matching cached inputs reuse the leased local executable without
dependency preparation or compilation. Changed inputs still require a build.
The shared materializer now checks for a lease before invoking Go. Misses retain
imported-package preparation and recompute the key after checksum changes.
Invalid cache ceilings and missing captured inputs still fail before reuse.

The new real-cache regression failed before the product fix with
`probe Go toolchain: go executable not found in PATH`. It now passes, rejects
invalid ceilings on a hit, and refuses stale reuse after a source edit. The CLI
reuse regression disables its fake compiler after the first build. An overlay
restoring only HEAD's materializer makes that test fail at dependency preparation
in 0.044 seconds. The fixed code passes with the same fixture.

The original toolchain fixture now models the documented cold `list` call and
checks its `go1.26.8` override. Compilation still receives the same override,
the probe runs once, the parent pin stays unchanged, and uncached compilation
does not add this preparation step. Obsolete fake `mod` handling is removed.

Verification used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W9 and
GOTMPDIR=/tmp/sw-astra-W9/go, with at most four package workers.

- Whole-repository `go build -p 4 ./...` passes. Vet passes for CLI, bincache
  and sdkinstall.
- Complete bincache and sdkinstall suites pass. Missing-checksum, sparks-overlay,
  covering/unrelated workspace, unused unavailable dependency and executable
  ownership tests retain their real-Go coverage. Focused race checks pass.
- CLI toolchain, compiler-reuse and cache-key tests pass. The Surface, Here and
  EngineAmendments acceptance families pass. No acceptance cell was newly
  unskipped and no archived test was newly ported.
- Commentcheck against main, sleepcheck against HEAD, full doccheck, embedded
  mirror/privacy checks and diff whitespace pass. Doccheck initially received
  no required arguments. Its corrected invocation with docs and repo paths passes.
- Complete cmd/sparkwing and bin suites were attempted. CLI stops at
  TestResolveSparks_WithManifest_WritesOverlay's forbidden TCP listener. Bin
  cannot reach proxy.golang.org in TestInstallToGreenBuildUsesPublicScaffoldAndCandidateSDK,
  then stops at TestReleaseManifestCanBePublishedByDigestWithoutATag's forbidden
  listener. These are sandbox restrictions, not successful full-suite evidence.
- Both tagged native burst families compile and skip on socket refusal. Parent
  PASS lines with skipped children are not native execution evidence.

Contract changes made: C3 engine documents lookup before Go and preparation plus
rehashing only on misses. No signature, route, schema, grant or public setting
changed. Contract changes needed in other areas: none. Local execution docs and
the changelog describe binary reuse. Their embedded mirrors are generated with
bin/sync-docs.sh. No removed API needs caller migration.

Shared files touched: internal/bincache/materialize.go, materialize_test.go and
compiler_inputs_unix_test.go; design/rewrite/contracts/c3-engine-version.md;
CHANGELOG.md, pkg/docs/changelog.md and pkg/docs/mirror/local-execution.md.
Other edits are W9's two CLI tests, docs/local-execution.md and area reports.

Production Go is +40/-15, net +25, all in the shared materializer. The unconditional
preparation path is removed. Whole legacy files deleted: zero. W9-owned production
is unchanged this session. W9 versus main is +19,166/-23,815, net -4,649, within
the -2,900 budget. No new service, environment variable, flag, interface, retry
loop or compatibility path was added. Cache lookup uses the existing lease API.

The amendment/handoff audit retains completed A1-09/16/17/38/41/43/49/50/51/52,
here transport, source attempts, step reports/windows, pipeline identity, launcher
grammar, doctor, local composition, owner/eval separation, shared logs, service
handover and decisions 110/111/113/114. Earlier native cancellation and cache fixes
are retained. No foreign stub blocks this correction. Existing product questions
remain with the lead. A1-53 and native Windows/Darwin, deployed Cloud/DinD, signed
fresh-machine distribution and PostgreSQL evidence remain lead integration lanes.

Friction: sandbox socket/DNS refusal and Overwing's source-lag notice. The lead
must rerun the whole CLI and bin packages plus its native burst families after
committing. Review and native completion remain with that harness. Scratch is
removed after all checks join. The shared build cache is retained.

STATUS: CONTINUE
