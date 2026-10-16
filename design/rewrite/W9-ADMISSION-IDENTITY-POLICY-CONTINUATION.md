# W9 admission identity and shared version policy

Admission holder references and persisted run IDs have distinct Go types.
`AdmissionRequest.RunID` and the participant constructors carry `HolderRef`.
`RunStore.IsRunTerminal` requires `RunID`. Admission and reattachment resolve
node-host, node-semaphore and plan holders through the same conversion before
reading the store. The JSON field names and values are unchanged.

Invariant: a terminal lookup receives the persisted run identity, never its
holder reference. The regression submits node hosts for a real SQLite run
through the daemon admission handler, reattaches them and rejects another host
after the run finishes. Generated node names exercise the reference encoding.
A separate property covers run, node-host, node-semaphore and plan references.

`Resolver.Index` owns the development-build rule for every policy consumer.
Runner admission no longer has its own build identity or notice guard. Every
production resolver constructor supplies its executable's build identity.
Released and unconfigured resolvers retain signed enforcement. Generated
runner version strings cannot disable it.

The production local HTTP regression completes a run and reads both its detail
and list page while policy downloads return HTTP 403. It makes zero policy
requests. The shared build-kind test covers runner admission, notices and
compatibility, including released-build failures and one development notice per
resolver. Engine tests cover development runner updates and cancellation.

A development compatibility response reports the API major and requested
engine identity without inventing release dates. Development runner updates do
not replace the running executable. A development repository upgrade needs an
explicit version because its resolver does not fetch the latest-version index.
Fetched executable verification is unchanged. The development exemption marker
is private to the engine package. The existing one-time notice guard moved to
the resolver. No environment variable or user setting was added.

## Native continuation

A disposable in-process probe used the production local HTTP controller over
`net.Pipe`, admitted a sealed request with captured environment and this
checkout's full commit, claimed its trigger and called `bindLocalRunHost`.
It passed policy, authentication, storage authority and snapshot loading, then
failed at the process API listener:

```
listen tcp 127.0.0.1:0: socket: operation not permitted
```

The probe was removed. No later product failure is established. Two existing
native reattachment tests also fail when the sandbox refuses their Unix socket.
Their in-process counterpart and the new admission/reattachment property pass.

## Verification and scope

Both modules pass `go build -p 4 ./...` and `go vet -p 4 ./...`. The engine,
wingwire, nodehost, localcontroller and runner short suites pass. Focused CLI,
controller and daemon regressions pass, including existing owner-proof and
reattachment membership checks. Commentcheck against main, documentation mirror
drift and diff whitespace checks pass. No acceptance cell was newly unskipped
and no archived test was ported.

Checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W9/go` and
`TMPDIR=/tmp/sw-astra-W9`. Scratch was deleted after checks joined. The shared
build cache was retained. No Git writes, operational Sparkwing commands,
candidate installation, deployment or release ran. The required read-only
Sparkwing info ran. Git has no local identity or core.worktree override.

Shared production files touched outside W9's CLI ownership are
`pkg/wingwire/{messages,wingwire,identity}.go`,
`internal/wingd/{runstore,server}.go`,
`internal/wingd/localcontroller/admission.go`,
`internal/nodehost/local_admission.go`,
`internal/chaos/crashdummy/main.go`,
`internal/cluster/production_runtime.go`,
`internal/engine/{engine,index,policy,upgrade}.go` and
`pkg/controller/{engine_claim,engine_version}.go`. Their tests were migrated to
the typed admission field. Source versioning documentation and its embedded
mirror were updated. The internal Go type changes above implement the requested
boundary repair. No wire, store-schema or foreign contract amendment is needed.

Production changes this session are +160/-114 lines, net +46. No legacy subsystem
was replaced. The removed runner-only exemption is replaced by the shared
resolver rule. The wider diff is the requested holder-type migration through
callers and tests. W9-owned production Go versus main is +16,916/-23,812,
net -6,896, within the -2,900 budget. Open questions: none for this task.
Friction: native sockets are unavailable in the sandbox.

STATUS: COMPLETE
