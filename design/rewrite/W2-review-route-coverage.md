# W2 route coverage handoff

Finding 4 is partial. The coverage mechanism is replaced and now detects 23 missing production registrations. The missing operations remain incomplete. No dummy handlers or availability adapters were added.

Invariant: every controller or node-host operation registered by its serving code has exactly one independently expected policy, and every expected operation has a serving registration.

`registeredMux` records the actual `Handle` calls used to build the controller API, browser authentication and metrics routers. `Server.Registrations` collects the configured serving surfaces. The test also invokes nodehost's production `RegisterProcessServices` into its policy router. The combined inventory is compared with the committed literal expectations, independently of `capability.Routes`, in both metrics-listener configurations. A generated-input property verifies that missing, extra and duplicate registrations are each diagnosed specifically.

The obsolete AST scope guard and its exported source-parsing helper are deleted. Their two team-boundary enumeration callers now use actual registrations and final policy metadata. The unrelated approval-vocabulary assertion moved into its own file. Deleted guard: 201 lines. Retained approval test: 21 lines.

The separate metrics listener bypassed its declared policy. It now uses the same endpoint and PolicyRouter as the API listener. Its property test exercises the actual metrics handler with anonymous, insufficient and sufficient authority. The source documentation and embedded mirror describe the same policy. The stale future-work comment on `capability.Routes` is removed.

## Missing registrations

`TestFinalCredentialServingRegistrations` fails with these same 23 omissions for `separate_metrics=false` and `separate_metrics=true`:

```text
DELETE /api/v1/pools/{pool}/rules/{rule}
DELETE /api/v1/runners/{runner}
GET /api/v1/pools
GET /api/v1/runs/{run}/children/{child}
GET /api/v1/runs/{run}/children/{child}/jobs/{job}/output
GET /api/v1/runs/{run}/jobs/{job}/attempts
GET /api/v1/runs/{run}/jobs/{job}/debug-pause
GET /api/v1/runs/{run}/jobs/{job}/output
GET /api/v1/runs/{run}/jobs/{job}/steps
POST /api/v1/claims/{claim}/log-manifest
POST /api/v1/claims/{claim}/log-segments
POST /api/v1/claims/{claim}/logs
POST /api/v1/operator/teams/{team}/refunds
POST /api/v1/pools/{pool}/rules
POST /api/v1/runs/{run}/children
POST /api/v1/runs/{run}/jobs/{job}/activity
POST /api/v1/runs/{run}/jobs/{job}/claim
POST /api/v1/runs/{run}/jobs/{job}/claim/input
POST /api/v1/runs/{run}/jobs/{job}/log-manifest
POST /api/v1/runs/{run}/jobs/{job}/log-segments
POST /api/v1/runs/{run}/jobs/{job}/logs
POST /api/v1/runs/{run}/jobs/{job}/resume
PUT /api/v1/queue/groups/{group}/priority
```

`TestControllerRegistersDeclaredLogSubmissions` independently reproduces the review's claim logs, claim segments, claim manifest and job segments route misses. Both completeness tests remain red.

## DESIGN proposals for the lead

- DESIGN: log transport composition. W2 needs one authority-bearing C1 transaction that accepts the authorized log subject and operation, checks the current writer and fence, and returns the persisted retention class. Tenant exposes `RecordSegments` and `ReadLogSegments`, but neither exposes the writer check and retention read required before signing. `Run` does not project `runs.log_retention_class`. W7's `blob.LogTransport` is an interface with no production implementation. Filesystem and S3 expose physical `SignLogPut` and `Get` operations, which explicitly require prior authority checks. Route the C1/W7 contract work through the lead before connecting C2 handlers. Final signing, segment-commit, manifest and live-push HTTP shapes also need one declared owner.
- DESIGN: required serving composition. Route the remaining missing families to their transaction owners: W1/W2 for job reads, debug resume, activity and administrative operations; W1/W3/W4 for child intake, child output and named job claim/input. Keep the real completeness test as the acceptance gate. Remove policy rows only with an approved product cut.

## Verification

Passed with `GOWORK=off`, `GOFLAGS=-buildvcs=false`, at most four package workers and caches under `/tmp/sw-astra-W2/`:

- `go build -p 4 ./...`.
- `go vet -p 4 ./internal/controller ./internal/capability`.
- Route metadata, policy-router, route-drift, metrics-authority, retired-route/method and approval-vocabulary focused tests.
- Full `go test -p 4 ./internal/apiroutes` after preserving the metrics literal in `team_read_routes.go`.
- `go test -p 4 ./pkg/docs -run '^TestPkgDocsContentMatchesDocsRoot$'` and `bin/sync-docs.sh --check`.
- `go run ./internal/commentcheck -base main .` and `git diff --check`.

No new native acceptance cell is claimed. The socket-based metrics-listener test was updated to present operator authority but was not run in the socket-restricted sandbox. The migrated legacy team-boundary fixtures were not claimed as final-schema integration proofs.

## Files, budget and friction

Production files: `internal/controller/{browser.go,operator_routes.go,policy_router.go,server.go,prom.go,team_read_routes.go}` and `internal/capability/routes.go`. Tests: credential route expectations/coverage, route registration, approval policy, capability policy, metrics, team boundary and the exported test helpers. Documentation: observability source and mirror.

The six shared controller production files have a combined working diff of +20 lines at handoff, including other W2 workers' deletions. The lead owns the complete W2 delta against main and the area budget decision.

Mechanism added: the registration recorder replaces source parsing because declarations alone cannot detect a handler omitted from a serving router. It adds no runtime setting or optional production dependency.

Friction: changing the private router return type initially broke tests that require `*http.ServeMux`; retaining that signature and recording inside its construction resolved it. Deleting the AST guard also exposed its exported helper's two callers, which were migrated. Moving the shared metrics endpoint temporarily hid it from the static API parser; keeping its literal in the existing endpoint-group source restored the full parser suite. No Sparkwing binaries or Git write commands were run.
