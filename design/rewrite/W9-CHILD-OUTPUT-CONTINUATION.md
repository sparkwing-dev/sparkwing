# W9 child output and missing-route diagnostics

Base: `bc5736b07`. The assigned child-output and 404 diagnostics fixes are complete.

The failing request was `GET /api/v1/runs/<child>/jobs/<job>/output`, sent by
`NativeProcessClient.Output` from the local host's `AwaitChild` service after its
successful child-status read. That generic output handler was absent. The host
now calls `ChildOutput`, which carries the parent in the declared final route
`GET /api/v1/runs/{run}/children/{child}/jobs/{job}/output`.

The controller registers that route under its existing awarded/delegated claim,
`refs.read`, `childOfBoundNode`, sensitive-liveness policy. `Tenant.ReadChildOutput`
rechecks current ancestry, claim liveness and the child relation in the same
snapshot as the selected terminal job's output record and blob metadata. The
filesystem and S3 adapters reuse the existing verified object reader and reject
missing bytes, bad digests, wrong sizes and invalid JSON before returning output.
No authority grant or generic cross-run data edge was added.

Unmatched 404 records retain `route=other` and add the request method and escaped,
sanitized path. The path excludes query parameters, masks registered secrets,
redacts capability bearers and hides secret/token/session selectors. Escaped
slashes stay inside their original segment. Metrics retain bounded route labels.
The source guides and embedded mirrors describe both behaviors. The performance
manifest records the route's missing dashboard corpus and unmeasured latency.

## Verification

Commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W9/go` and
`TMPDIR=/tmp/sw-astra-W9`. Go package workers were limited to four.

- Both modules pass `go build -p 4 ./...` and `go vet -p 4 ./...`.
- `TestRunAndAwaitChildOutputThroughLocalController` first reproduced the exact
  status-200, output-404 failure. It now executes a typed SDK child job, publishes
  its output through the real controller and filesystem store, and returns that
  value through real `RunAndAwait` and local-controller process services.
  Transport is in-process HTTP. Child scheduling is driven by the fixture.
- Its generated cases refuse changed team, parent, child, job, generation,
  holder, cancellation and revocation facts, including stale supplied authority.
  Corrupt output returns `integrity_failure`; missing output bytes return 404.
- The unmatched-route logging property first failed for the absent path and now
  passes for reads and writes, queries, escaped separators, control characters,
  registered secrets, credential selectors and capability bearers.
- Focused controller await/audit tests pass with the race detector. Native host
  storage, store child-binding/intake and SDK await regressions pass.
- Full short suites pass for controller/client and localcontroller.
- `FuzzObjectOutputRequiresVerifiedJSON` passed 33,941 generated executions with
  four workers, including explicit JSON null and corrupt/wrong-size cases.
- Commentcheck against main is clean across 2,560 files. Embedded-doc sync,
  OpenAPI drift and whitespace checks pass.

The broader controller child-test selection cannot run its older OAuth fixture:
`httptest.NewServer` is refused with `socket: operation not permitted`. The new
integration test uses no listening socket. The native `sparkwing run example`
rerun remains with the lead. No native acceptance cell is newly claimed and no
archived test was ported.

## Scope and handoff

Shared production files touched: `internal/controller/child_routes.go`,
`internal/controller/audit.go`, `internal/controller/server.go`, `internal/store/child_output.go`,
`internal/blob/object_output.go` and
`internal/wingd/localcontroller/native_client.go`. W9's client change is in
`internal/controller/client/native_services.go`. New tests are
`internal/controller/child_output_sdk_test.go` and `internal/blob/object_output_test.go`;
the existing audit property file is extended.

Additional generated cleanup: OpenAPI was already missing the current
`owner_capability_prefix` snapshot field. Regeneration adds only those two lines.
Source docs, mirrors and `perf/budgets.json` are updated. Lead-owned contract
handoffs and amendments are unchanged. No foreign contract change is needed.

Mechanisms added: one authorized result selector and two concrete physical
output-read methods. They complete the already declared child-output route.
The generic route cannot enforce the creating-parent relation. There is no new
setting, interface declaration, timeout, retry, grant or compatibility path.

This continuation adds 183 production Go lines and deletes 5, net **+178**.
W9-owned production paths add 9 lines. W9 versus main is +16,931/-23,812,
net **-6,881**, within the -2,900 budget. Tests, testdata and contract declarations
are excluded. No whole legacy file was deleted.

No Git write, candidate install, operational Sparkwing command, daemon or release
ran. Scratch under `/tmp/sw-astra-W9` is removed after checks finish; the shared
build cache is retained. Friction is limited to the blocked older socket fixture
and Overwing's installed-source lag. Native replay remains unverified here.

STATUS: COMPLETE
