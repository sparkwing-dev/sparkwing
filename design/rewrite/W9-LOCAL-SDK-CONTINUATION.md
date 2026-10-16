# W9 local SDK continuation

Base: `45ce5ff5f`. Priorities (1)-(3) remain implemented, and their focused
socket-free regressions pass. The added assignments remain open at the foreign
contracts listed below.

## Implemented

Foreground and queued local hosts now select the SDK from the pipeline directory
in the authenticated persisted request before acceptance. They use the existing
source projection and package detector. Previously both constructors supplied
Go, so a TypeScript pipeline that passed preparation failed when its owner tried
to read `.sparkwing/go.mod`. Accepted runs retain their sealed engine's SDK.
Malformed manifests and invalid source locations fail before process transport
opens.

The regression ran red with `resolved=false` and a missing `.sparkwing/go.mod`.
It now acquires a real committed local checkout, reads its actual TypeScript pin
and reaches the engine resolver. Only the listener and external engine resolver
are fakes. The other cases cover Go, a custom TypeScript pipeline directory,
malformed manifests, an escaping pipeline directory and accepted-engine
precedence. Every constructed service closes in `t.Cleanup`.

The local-execution guide describes the selection. Its embedded mirror was
regenerated. This closes local SDK discovery before acceptance. Remote SDK
discovery and complete TypeScript schedule pinning remain open.

## Verification

Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9. Package workers were limited to four.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Vet for localcontroller, cluster, runners and its subpackages, and the CLI.
- The full localcontroller suite and native construction/SDK regressions under
  the race detector.
- Full short suites for cluster, runners, controller/client and doccheck.
- Readiness, runtime-fixture cleanup, detached entrypoint, public doctor,
  authenticated queue construction, controller sweep and peer-intake tests.
- Surface and engine-amendment acceptance components, native store host modes
  and cancelled owner log-finalization regression.
- Installed-runner rewrite_integration probes for start, warm, fetch-source and
  run-node. These prove their first production HTTP operation.
- CLI rewrite_integration compilation, commentcheck across 2,345 files, embedded
  documentation drift check and `git diff --check`.

The launch probe was also run and remains red at the missing production launcher
factory. Its assertion and error are unchanged. No native acceptance cell is
newly claimed, and no archived test was newly ported. Native command execution,
platform service managers, Windows, Cloud isolation and PostgreSQL evidence
remain with the lead. Independent landing review remains with the lead.

## Contract changes needed and real integrations left open

No foreign contract changed. The outstanding requests in
[W9-RUNNER-TRANSPORT-CONTINUATION.md](W9-RUNNER-TRANSPORT-CONTINUATION.md) remain,
with local SDK discovery closed as described above.

1. C2/W3 must supply an owner/eval process credential and source-job relation
   independent of a positive node attempt. The separate evaluation binder has
   fake contract evidence, but no production credential can implement it.
2. W8 must implement KubernetesBoundary.Probe and Clear with actual network
   enforcement evidence and reservation-scoped teardown. Production launch stays
   unbound. Cloud plan/eval hosting and the previously reported controller-to-
   launcher resource integration also remain open.
3. Canonical remote placement eligibility, cache/cross-run read-set proofs,
   bootstrap checkout consumption, sealed scheduled intake, initial here reason
   persistence and the earlier debug/publication bindings remain open. Public
   steps and receipts still need the C2/W1 read projections.

The lead should rerun TestReviewProductionExecutionBindings,
TestReviewPublicDoctorSweepsAndWarnsForPrunedRun and
TestLocalClaimantW8UsesPersistedPeerAuthority natively with isolated state and a
release-version stamp. Launch remains a required failing done test until its
deployment boundary exists.

## Scope, size and friction

Shared files touched: internal/wingd/localcontroller/native.go and
native_config_test.go. Documentation files: docs/local-execution.md and its
generated mirror. No caller or API was removed. Lead-owned handoffs and
amendments are unchanged.

This continuation adds 15 production lines and deletes zero. No whole legacy
file was deleted. W9-owned production paths versus main remain +16,876/-23,797,
net **-6,921**, within the -2,900 budget. Tests, testdata and contract declarations
are excluded. The shared localcontroller addition is reported separately above.

No option, environment variable, interface, timeout, retry, authority grant or
compatibility path was added. No repository Git write, operational Sparkwing command,
candidate install, pipeline, daemon startup or release ran. All temporary worker
files are removed after checks finish. The shared build cache is retained.
Friction: sockets and platform service managers are unavailable, and Overwing's
installed binary reports source lag. The remaining foreign contracts prevent a
complete area claim.

STATUS: CONTINUE
