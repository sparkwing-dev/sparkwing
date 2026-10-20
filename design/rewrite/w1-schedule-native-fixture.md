# W1 schedule native fixture

The five minor findings remain fixed in committed `587cbe9f2`. This continuation
addresses the only failure in the lead's latest native report:
`TestPostgresScheduleConcurrentFiresCommitOneRunWithoutDeadlock` failed while
fetching its source, before reaching its concurrency assertions.

The schedule test must reach the real intake transactions using a retrievable,
pinned commit without depending on an external repository. Its old setup
installed the real controller source resolver but supplied an unbacked GitLab
URL and a fabricated SHA. The setup now uses `sourcetest.FixedRemote` and the
commit it creates. A public literal host avoids a DNS dependency. The existing
Git fixture redirects fetches to the local repository.

The extracted setup serves both the existing PostgreSQL concurrency test and
`TestScheduledRunSealsPinnedRemoteSource`. The new test admits a schedule through
the concrete controller policy and checks its sealed checkout SHA, pipeline SHA
and remote location. It follows the suite's selected database dialect. The
PostgreSQL advisory lock, blocking-pid barrier, two concurrent admissions and
single committed run/history assertions remain intact.

## Tests first and verification

The new SQLite probe first failed on DNS socket refusal. Using a public literal
host removed that dependency and reproduced the native symptom in 0.117s:
`source git fetch failed: exit status 128`. Supplying the local fixed commit then
passed in 0.670s. No production behavior changed.

Checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W1`,
`TMPDIR=/tmp/sw-astra-W1`, `GOMAXPROCS=4` and `-p 4` for tests.

- Whole-repository build: pass.
- Store and fixture-package vet: pass.
- Schedule, fallback, rotation and deletion regression selection: pass, 6.986s.
- Focused fallback, rotation, deletion and schedule race selection: pass, 28.518s.
- Final named pinned-source test under the race detector: pass, 2.537s.
- Commentcheck against main: clean across 2,918 files.
- Whitespace check: pass.

The targeted PostgreSQL invocation used `sw_w1lead` at 127.0.0.1:5433. Both
schedule tests failed before schema creation with `socket: operation not
permitted`. This is unavailable native evidence. The lead's latest native file
still contains its original PostgreSQL FAIL and was left unchanged. A lead
rerun must verify that the corrected fixture reaches and passes the PostgreSQL
contention assertions. The prior whole-repository SQLite native run passed.

## Scope, deletion and remaining work

Only `internal/store/schedule_lock_postgres_test.go` and this report changed. The test
diff adds 23 lines and deletes 3. Production additions and deletions are both
zero. Under the lead's production filter, store remains 27,230 added and 27,383
deleted against main, net -153, or 4,097 above the combined -4,250 budget. No
budget waiver is claimed.

No acceptance cell was skipped or weakened, and no legacy test port was added.
No caller outside W1 changed. There is no contract amendment, new runtime
mechanism, setting or environment variable. Lead-owned handoffs and amendments
remain unchanged. Earlier W1 adapters, ordered placement, Cloud metering,
rotation grace, sealed App/scheduled intake and legacy writer cuts remain in
place. Retired `StateStoreOld`, `triggerIntake`, `triggerRows` and the named dead
contract declarations remain absent.

The remaining real-integration point is the native PostgreSQL rerun. No new stub
boundary was introduced. The inherited production budget still requires a lead
decision or further cuts. No Git write ran. Local Git config has no user identity
or core.worktree override. Session scratch is removed at finish, and the shared
Go cache is retained.

STATUS: CONTINUE
