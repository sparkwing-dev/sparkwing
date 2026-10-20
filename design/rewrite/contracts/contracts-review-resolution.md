# Contract repair report

<!-- design:begin -->

Findings 1-26 are applied as contract declarations, tables, schema and stub
signatures. Groups A, B and C were completed in the requested order. Runtime
implementation remains the next phase. Findings 27-36 and both acceptance
source directories were left to the separate acceptance session.

| Finding | Status | Result |
| --- | --- | --- |
| 1 | Applied | C1 `ReconcileNode(ReconcileRequest)` returns `ReconciledNode`. Competing declarations are removed; source outcome comes from persistence. |
| 2 | Applied | C1 `AcceptExpansion(ExpansionSubmission)` handles success XOR structured failure. `ExpansionForStore` combines wire inputs with C2 authority. |
| 3 | Applied | `RunSnapshot.Scheduling` projects consistent accepted declarations and persisted expansions, including empty groups, into the shared decider. |
| 12 | Applied | Typed request and ceiling projections preserve presence/origin and separate run selection from machine runner preferences. Restricted checkout declarations retain both project layers through wire, acceptance and sealing. |
| 13 | Applied | Resolved per-job settings persist independently of declaration specs and reconstruction hashes, including generated jobs. |
| 14 | Applied | C3 `SourceSpec` owns checkout policy; C4 resolves it and source execution consumes the sealed winner. Acquisition identity stays separate. |
| 15 | Applied | Fractional cores and complete origins survive sealing. Origin conversion is explicit and fixed scratch is not an author disk override. |
| 16 | Applied | The shared matcher contract covers granted reservations, allowed pools and resolved requirements/preferences for named, queued and self claims. |
| 17 | Applied | `blob.LogSubject` tags job attempts versus trigger/plan/eval units across signing, commits, reads and schema, with independent eval/source fences and the bounded final-job flush exception. |
| 4 | Applied | Tagged capability/session ancestry persists and resolves against the appropriate authority store. |
| 5 | Applied | Intake, actors and sealed authority retain `AuthorityRef`; capability prefixes remain separate claim fences. |
| 6 | Applied | Operational kinds are trigger/plan/eval/node. Work/here are award purposes and bootstrap is delivery, preserving the underlying unit. |
| 7 | Applied | Versioned capability envelopes retain the full binding/data/authority shape with checked indexed projections and schema round-trip tests. |
| 8 | Applied | Operational and billing units persist separately with trigger→host, node→job, plan→plan and eval→eval mappings. |
| 9 | Applied | C4 owns job/host billing constants; C2 consumes them. Plan/eval minima remain zero. |
| 10 | Applied | Protocol consumers use typed canonical error codes and explicit reasons, with a registry-membership test. |
| 11 | Applied | `apierr.DecodeInner` validates every inner protocol error and rejects malformed or double envelopes. |
| 18 | Applied | Eval keeps expansion.submit and data.read. Its own-unit log alternatives use expansion authority; failures use the single expansion transaction. |
| 19 | Applied | C2 verifies Stripe events; C1 settles committed checkouts idempotently with server-derived teams/credits. Test/live uses one implementation contract. |
| 20 | Applied | `internal/controllerwire` owns lifecycle/claim/renewal/expansion/status types and the twelve-month fetched-engine API boundary. Intake retains idempotency keys on runs, including publication. |
| 25 | Applied | C4 owns a five-second auth-cache TTL. Cache reuse also requires fresh ancestry/membership generations; C1 mutations advance them atomically. |
| 21 | Applied | `internal/engine` owns exact Go/TS pin resolution, GitHub-only release fetching, version-aware closed-set verification and machine caching. |
| 22 | Applied | Signed version index, short caching, verified fallback and rollback refusal are declared. First use without a verified copy fails with an actionable unavailable error. |
| 23 | Applied | `sparkwing upgrade`, revocation/minimum errors, shared run/dashboard notices and verified runner self-update are declared. Age alone never revokes a release. |
| 24 | Applied | C3 owns default-enabled plan caching, complete identity, eligibility, invalidation and validated hits. Hits still pass seal-point-two validation and incur no planning charge. |
| 26 | Applied | Decision 104 governs: TypeScript on Windows is supported; C3 notes and the SDK platform table agree. |

No requested contract fix remains unapplied. All competing C3-suffixed target
declarations were removed. Old-suffixed runtime scaffolding remains only where
the original contracts brief requires the existing code to compile.

Verification uses `GOWORK=off` and `GOFLAGS=-buildvcs=false`. The requested cache
location was read-only in this sandbox. Creating its temporary directory failed,
and the requested build stopped before compilation. The verification substitute
uses `GOCACHE=/tmp/sparkwing-rewrite-check-cache` and its `tmp` child as
`GOTMPDIR`.

| Check | Result |
| --- | --- |
| Full `go build ./...` | Passed after all corrections. |
| `go vet -p 4` on all 19 touched contract packages | Passed. |
| Full tests for apierr, blob, capability, clisurface, controllerwire, engine, limits, nodeproto, releaseasset, runners, runspec, settings, source and match | Passed; declaration-only packages report no test files. |
| Focused `go test -p 4 -run '^(TestRewriteSchema\|TestProtocolErrorUsesCanonicalRegistry)'` on store, nodehost, launcher, controller and controller/client | Passed. Store schema tests ran; the other four packages compiled with no matching runtime tests. |
| `git diff --check` | Passed. |
| Competing C3 reconciliation/expansion symbol scan | No remaining declarations or consumers. |
| Acceptance directory diff | Empty. |
| Independent snapshot review | Three integration seams found and corrected; focused re-review found no remaining defect in those corrections. |

The vet package list is `internal/apierr`, `internal/blob`, `internal/capability`,
`internal/clisurface`, `internal/controllerwire`, `internal/engine`,
`internal/limits`, `internal/nodehost`, `internal/nodeproto`,
`internal/releaseasset`, `internal/runners`, `internal/runners/launcher`,
`internal/runspec`, `internal/settings`, `internal/source`, `internal/controller`,
`internal/controller/client`, `pkg/match` and `internal/store`. Test commands use the same
cache/environment values as the build and vet. The requested-cache check still
needs a shell that can write that location; the substitute is not an exact-path pass.

The focused test boundary covers contract data, schema round-trips and package
compilation. No Sparkwing executable, installed daemon, release or runtime
acceptance scenario was run. No git command wrote repository state; the lead
retains committing and landing.

The added mechanisms are the requested shared controller wire package, the
single engine/version owner, version-aware declarations in the existing release
asset owner, and the planning-cache boundary. Existing stores own authority,
settlement and logging. There are no new trust roots, environment variables,
compatibility aliases or runtime services.

Choices recorded for implementation:

- First use without a verified version index fails explicitly. A previous
  verified index has no fallback age cutoff. Refresh is five minutes.
- The engine cache defaults to the OS user cache directory under
  `sparkwing/engines`. An ephemeral Cloud runtime has an ephemeral machine cache;
  this adds no hostPath or controller mirror.
- Allowed checkout policies are complete typed policy tuples; an unset policy
  leaves acquisition unrestricted. Resource conversion occurs at platform edges.
- New runs plan afresh. Accepted resumes retain their sealed plan and complete
  engine identity. Persistent plan caching is removed under decision 28.
- The host's separate sixty-second billing minimum is retained and named.

Existing questions outside these repairs remain in the area notes: root
provisioning as the root-only Delegate case, approval SkipIf versus removed
before/after hooks, startup-env wording, local external credentials versus the
credential-filter rule, and the selected CLI/config spellings. Golden hash
vectors, concurrency stress evidence and deployment network evidence remain
implementation/acceptance obligations. None requires a competing contract shape.

<!-- design:end -->
