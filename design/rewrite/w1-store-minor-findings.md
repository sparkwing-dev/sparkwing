# W1 minor store findings

The five findings in W1-review-1792290802-final are addressed on top of
`76aa6d8f9`. No Git writes ran. The lead-owned amendments and handoffs are
unchanged. Earlier W1 cuts and adapters remain in place.

## Implementation and invariants

Stable fallback discovery must finish independently of the number of object
reads. Memo, depcache and default-record fallback selection now accumulate
pending reads across their authorized candidates before leaving the transaction.
A lower-priority hit or record error waits for every higher-priority pending
candidate. Reads
still run outside transactions, and each attempt rechecks current authority and
records. The existing four-pass limit is unchanged.

The memo regression failed before the fix for both LookupMemo and CopyMemo:
four missing/corrupt manifests returned Conflict, and a valid fourth candidate
also returned Conflict. The property covers every possible winning candidate
and the all-missing case. A second property varies remote-cache reference count
from one to nine, with all manifests missing or only the last candidate valid,
for both memo and depcache. The existing moving-record proof still refuses a
graph that keeps changing. The priority regression also covers a malformed,
unused lower-priority record. The corrected fixture uses distinct digests so
that its malformed fallback is outside the preferred digest's read set. An
overlay removing only the priority checks fails that test. The final code passes.

Rotation response metadata must describe only the committed attempt.
`Tenant.RotateCredentials` now returns `CredentialRotation`, containing rotated
secret count, git count and skipped name/pipeline identities. The secret callback
returns `(value, skip, error)`. The store builds all metadata inside the retry
boundary and resets it for each attempt. A skip preserves the envelope without
writing it. Errors return an empty result. The controller serializes that result
without keeping a request-level skip slice.

The HTTP regression failed before the fix with HTTP 500 after an earlier
attempt skipped a row that the committed attempt removed. Its generated cases
also cover repaired rows. A real SQLite transaction property forces one through
four serialization retries and checks committed counts, skipped identities and
stored envelopes. Existing tenant, metadata, encryption and rollback properties
retain their assertions through the new return type.

Deletion timestamps and comparisons must preserve nanoseconds. Requests,
completion, recheck deadlines, lease writes/read predicates and purge's account
update now use Unix nanoseconds. The regression failed first on request-time
truncation. It checks raw persisted units, reader round trips, expired-lease
refusal, takeover and recheck due/early boundaries one nanosecond apart across
eight generated fractional timestamps. It also checks purge's account timestamp.
The obsolete cache-grant rationale and separate run/trigger lookup comment are
removed. Recheck's field description now names the final SQL sweep.

## Contract changes made

C1 documents the committed rotation result and nanosecond deletion family.
C2's existing CredentialRotator declaration and documentation use that result
and the explicit secret skip flag. The HTTP response fields remain rotated,
skipped and git_credentials. No additional contract request remains.

Shared implementation edits are limited to
`internal/controller/credential_maintenance.go` and `internal/controller/secrets.go`.
The HTTP fake and property live in
`tests/acceptance/credential_maintenance_http_test.go`. Shared documentation and
generated edits are C1, C2, CHANGELOG, the unreleased migration guide, their
generated embedded copies, and the store
and controller API snapshots. Snapshot regeneration also captures already
committed Intent/ReplayRun and source-response/helper declarations. The separate
inherited controller-client API snapshot drift is unchanged.

## Verification

Commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W1`,
`TMPDIR=/tmp/sw-astra-W1`, `GOMAXPROCS=4` and at most `-p 4`.

- Whole-repository build: pass.
- Store/controller/acceptance vet: pass.
- Focused store and HTTP regressions: pass.
- Focused store race selection: pass, 20.081s.
- Remote-cache count property: pass, 0.730s.
- Controller credential/secret selection: pass, 10.012s.
- Commentcheck against main: clean across 2,918 files.
- Doccheck, commentcheck and sleepcheck package tests: pass.
- Embedded-doc symbol/privacy selection after migration regeneration: pass, 0.591s.
- Source-doc mirror, changelog and whitespace checks: pass.
- Store acceptance/property selection (`^TestStore`): pass, 41.704s, with no
  skipped top-level tests. It includes sessions, transitions, plan seals, queue
  order, matching, enrollment, launcher sync, schedules, recovery, cache scope,
  CAS integrity, read edges, retention and billing.
- Final combined race selection: pass, 36.215s.
- Full final SQLite store suite: pass, 299.396s. Fixture packages also pass
  (teststore 0.236s, internal/storetest 0.005s). No Go source changed during
  this run.

The first full store run found an existing team-deletion test that required
second truncation. Its assertion now requires the exact supplied nanosecond
time. The focused owner/last-team guard test passes with that stronger check. An
intermediate full run compiled the first priority fixture before its distinct-
digest correction and failed only that fixture. The final full run above
includes all corrections and passes.

PostgreSQL targeted sw_w1lead on 127.0.0.1:5433 and stopped before schema setup
with `socket: operation not permitted`. The docs web-client test likewise
cannot open its httptest listener. The lead's latest native report has no FAIL
and records a successful prior whole-repository and PostgreSQL store run. It
does not establish native evidence for these uncommitted changes. No native
report was edited. Native PostgreSQL remains the real-integration check for
this session. No new stub boundary or external provider work was introduced.

No acceptance cell was skipped or weakened, and no legacy test port was added.
Existing W1 acceptance cells run in the store suite. Earlier handoff entries
for authority adapters, sealed intake, cancellation/session projections,
ordered placement, Cloud metering, launcher fencing and legacy writer cuts
remain implemented. W1b's separately assigned billing/schedule work is unchanged.

## Deletion, budget and assumptions

This session removes the request-level skip/dedup state, two duplicate HTTP
response structs and obsolete comments. Under the lead's production filter:
84 added, 57 deleted, net +27 Go lines across the repository. Store is +43 this
session and -153 against main, 4,097 above the combined -4,250 budget. No budget
waiver is claimed. The inherited overage remains a lead decision and prevents
an area-complete claim.

The two structural changes were requested in the review assignment. Batching
uses the existing deferred-I/O mechanism because increasing its pass limit
would retain a read-count ceiling on stable fallback. Attempt metadata moves
into the existing transaction result because handler-side deduplication cannot
know which attempt commits. Neither adds a runtime option, environment variable,
retry loop, schema table or service. The two result types replace the deleted
HTTP response types. The behavior changes add 27 net production lines.

Friction: PostgreSQL and listener sockets are denied by the sandbox. The whole
API snapshot comparison has unrelated inherited controller-client drift. The
lead commits and runs native checks. Scratch is removed at finish and the shared
Go cache is retained. Git configuration has no user identity or core.worktree
override.

STATUS: CONTINUE
