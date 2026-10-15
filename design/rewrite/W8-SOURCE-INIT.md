# W8 Cloud source initialization and entrypoint composition

`SourceInit` implements the fetch-source half of the private Cloud award path.
It captures the source projection and deadlines before binding source access,
calls W3's real `source.PrepareCheckout`, and moves the matching checkout to
the requested source directory. W3 owns the private Git staging directory,
credential files and their cleanup. Existing destinations are refused before
requesting authority. Failure, cancellation and partial binding cleanup remain
errors. SDK installation and bootstrap exchange happen after this phase.

`cluster.BootstrapRuntime` reads the immutable award file and checks argv's run,
job and generation before constructing either source or host services. Both
constructors receive the parsed controller and paths. Source initialization
receives the private bootstrap in memory. Execution uses `BootstrapHost` and
its existing single exchange. Partial host bindings close on failure.

`MainWithRuntime` supplies runner, launcher and bootstrap service bindings to
the executable. Main uses these bindings and one SIGINT/SIGTERM context for
all commands. The launch command's duplicate signal-context wrapper is removed.
The shared bootstrap grammar now permits plan units to omit `--job`. Eval and
work still have to match the job in their private award before binding services.

Invariant: changing argv's run, job or generation cannot reach host construction
for another private award. `FuzzBootstrapRuntimeFence` generates those changes.
Invariant: source access cannot start for a Cloud local-checkout projection or
an engine that differs from the awarded engine. `FuzzSourceInitRejectsChangedEngine`
generates engine values and checkout classes. Real Git fixtures verify exact SHA
publication, cancellation, missing commits, refused access, partial cleanup,
existing-destination preservation and mutation after capture. The fixture
redirects a public literal-IP remote to a local repository. It proves the shared
Git path without DNS or network access. Credential release is a fake.

## Amendment and handoff disposition

All 26 amendment entries naming W8 and the W8 handoffs were checked. The prior
owned-store queue, native store host, local admission and legacy deletions remain
in place. This session adds execution code, with no new native catalog cell.

| Entries | Result and remaining work |
| --- | --- |
| A1-08,09,20,26,49,55 | Ordered/named claims, shared authorization, file credentials, enrollment cleanup and here handling remain implemented. Default runner/launcher factories, local claimant provisioning and production Actions wiring remain open. |
| A1-10,11,12,43,45,46,48,50,51,52 | The source initializer now consumes W3's checkout implementation and captured environment options. Selected-engine installation, signed policy, historical matrices and shared settings remain intact. Native platform evidence and final CLI/host bindings remain open. |
| A1-18,27 | W8 preserves SourceAttempt and Applied.Attempt at its boundaries. W3 holder transitions still need SourceAttempt. W1/W2 own billing transactions and policy. |
| A1-19,36,39 | Embedded queues still construct the real native hosts against the owned store. Remote source/process/policy/log/storage bindings and NativeHostStore transport remain open. Source initialization does not execute SDK code under bootstrap authority. |
| A1-38 | The private-file path now reaches source initialization or exchange after fence validation. Plan argv passes the shared parser. Transactional launcher sync and plan/eval resource resolution remain open. A1-38 is not closed end to end. |
| A1-40,41 | W8's local store owner, queue, reaper, admission, on-demand runtime, four holds, drain and managed units remain implemented. The production State/HTTP/claimant/native-service binder and W9 resolver remain open. |
| A1-42,53 | Deployed Probe/Clear, network enforcement, aggregate scratch, teardown, native platforms and real-repository QA remain open. Native cells stay blocked. |
| Pending-claim designs and lead decisions | Completed outcome/cancellation behavior and launcher flag corrections remain intact. Award correlation and shared LocalHTTP remain implemented. W1/W2 seeded provisioning and transactional sync retain their existing owners. |

## Real integration points and contract requests

- W1/W2 must supply `localcontroller.State` and the shared HTTP handlers over
  the elected store. This includes readiness, owned secret reads, activity and
  kernel-peer authorization. The store's authentication reader currently handles
  bearer and session presentations and refuses other classes. W8 adds no local
  authorization bypass. W9's `resolveLocalServices` still returns unavailable.
- The executable's default `MainWithVersion` still constructs an empty Runtime.
  Its production caller must supply the runner, launcher, source-access and host
  bindings to `MainWithRuntime`. The new entrypoint port does not itself supply
  those services.
- W2/W9 must bind `source.Access` to the final controller source-release and
  signed bundle routes, using the bootstrap presentation. The legacy client's
  SourceCredential return type is not the final SourceCredentialResponse.
  This session does not create another controller client or wire decoder.
- W8/W3 must connect the initialized checkout to native work hosting after
  exchange, revalidate its sealed SHA and source policy, then acquire the selected
  engine and install the SDK. NativeHostStore transport, fresh process/storage
  bindings and controller-owned plan/eval composition remain open. The source
  initializer alone is not a working pod entrypoint.
- W1/W2 still owe `LauncherSyncReader.ReadLauncherSync` with transactional
  ancestry/fence checks and resolved plan/eval resources. W8's correlation is
  implemented. No production reader exists in this tree.
- W1/W2/W6 retain the approved seeded bootstrap-admin path. W8 introduces no
  alternate issuer. W8 still owes deployed boundary Probe/Clear implementations.

No foreign Go interface changes are needed by this slice. The shared registry's
two optional `--job` flags implement the already requested plan-unit correction.
The lead-owned amendment and handoff files are unchanged. No new product decision
or customer-visible restriction is proposed.

## Changes and assumptions

New mechanisms are SourceInit, BootstrapRuntime and the executable Runtime binding.
They connect the existing source, private-award, exchange and host contracts.
Direct cloning into the destination would expose a failed checkout. W3 already
owns staging and cleanup, so W8 reuses it. Removing these files and restoring
the former command dispatch undoes the composition. No extra timeout, cache,
environment variable, issuer or compatibility path is added.

Deleted production surface: nine physical lines for the duplicate launch signal
wrapper and its imports. No whole legacy file is newly deleted. The warm-pool
dispatcher, run-host Kubernetes dispatcher, cluster worker and old pod builders
were removed in prior sessions and remain absent.

Shared files touched: `internal/clisurface/registry.go`. Caller migration:
`internal/cluster/main.go` and `internal/cluster/storage_flags_test.go` now use
RunLaunchCLI directly. Other source and tests stay in W8's area. Source docs
are updated in `internal/runners/launcher/CLOUD-JOBS.md`.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and at most four
package workers. GOCACHE, GOTMPDIR and TMPDIR are under `/tmp/sw-astra-W8`.

| Check | Result |
| --- | --- |
| Whole-repository `go build -p 4 ./...` | Passed. |
| Vet on runners/..., engine/..., localcontroller, cluster and clisurface | Passed. |
| Runners/..., engine/..., localcontroller, internal/acceptance and clisurface tests | Passed. |
| Cluster tests | Passed with the socket-bound metrics listener test excluded. |
| `tests/acceptance -run 'W8\|LauncherCloud\|LauncherController\|LauncherPolling'` | Passed. Includes existing local-store/queue and launcher components. |
| SourceInit and BootstrapRuntime tests with `-race` | Passed. |
| Each new fuzz property, `-fuzztime=1000x -parallel=2` | Passed 1,000 executions. |
| `go run ./internal/commentcheck -base main .` | Clean across 2,124 Go files. |
| `git diff --check` | Passed. |

No new native acceptance cell, archived source-function test port, deployed
Cloud result or full real-repository QA pass is claimed. The lead retains native
checks, independent review and commits. Generated CLI references still belong
to W9's regeneration pass.

The first remote fixture attempted DNS and was refused by the socket sandbox.
The corrected fixture uses a public literal-IP remote redirected to local Git.
A cancellation regression exposed ctx.Err replacing its custom cause. The
initializer now preserves that cause. A test comparison of a non-comparable
source struct was corrected. A concurrent compile overlapped that import edit
and failed. Targeted verification was rerun after the source settled.

Friction: pipeline and socket-bound native checks remain lead-owned. Overwing's
installed binary reports ten commits behind source. Git config has no local
identity or core.worktree override. No Git write, Sparkwing binary, pipeline,
deployment or release ran. Scratch is removed after checks join.

STATUS: CONTINUE
