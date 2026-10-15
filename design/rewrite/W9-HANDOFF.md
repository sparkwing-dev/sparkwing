# W9 continuation handoff

W9 is incomplete. This session continues commit `e98f5bdeb`. The production
CLI now parses the final grammar, but many execution handlers still use old
runtime APIs. A passing grammar test is not full surface acceptance.

## Completed in this session

- `runSparkwing` uses `clisurface.Parse` before checkout changes, version stamps,
  update cleanup, profile lookup or dispatch. It rejects removed commands and
  flags with exit 2. Help accepts flags before verbs. Checkout applies in either
  position and is restored afterward. Version remains an executable read action
  despite having a `hold` child.
- Help, completion and command discovery derive structural metadata from
  `clisurface.Registry`. The duplicate 3,807-line help registry is replaced by
  prose keyed to the final grammar. Discovery uses `--search`, `--all`, and
  positive page limits. Generated CLI references and embedded mirrors match.
- Run, trigger, plan and lint parsing consume the shared grammar. Pipeline args
  remain behind `--`. Run accepts `--sw-no-memo` and `--sw-allow-risk`.
  Explicit output wins in order: `--sw-output`, `-o`, environment, terminal.
  Foreground pause flags reach the host, including explicit false.
- Shell completion uses flag-based `__complete` requests. Bash, zsh, fish and
  PowerShell artifacts exist. Completion output follows the output contract.
- Profiles use `add/list/show/set/remove`, kind-named selectors and consistent
  output. Tokens are redacted. Only machine configuration supplies profiles.
  Failed revocation leaves the connection saved. Cloud dispatch/status,
  admin-token enrollment and project credential fallback are deleted.
- `runs cancel` uses `controllerwire.Controller.Cancel` for both connection
  choices. It retains the authoritative applied result, replay marker and API
  error. The CLI no longer opens SQLite or guesses that a 404 means a terminal
  run. Stdin IDs remain supported. Local connection setup still uses the existing
  wingd startup API, pending W8's lifecycle cutover.
- Diagnostics carry the parsed command path and exit code. JSON delegates to
  `apierr.WriteJSON`; tests supply a fake because W2's writer remains a stub.
  Structured codes, hints and details survive the CLI boundary.

The parser, engine HTTP client, response validator, retry policy and calendar
compatibility window from the previous session remain implemented. The earlier
bounce/rerun deletion remains in the committed base.

## Runtime work still required

1. Replace the remaining legacy handlers and client calls, then delete their
   dead implementations and tests. Reaching a final command path does not prove
   its handler meets the final contract. Several pflag handlers still have old
   defaults, output behavior or incomplete flag support.
2. Wire W3's shared run host and W6's sealed request/settings. Publication,
   cache-from, target selection, here-resume and trigger pipeline-ref selection
   return explicit unavailable errors. Those are unfinished integrations, not
   final product restrictions. Detached pause flags also refuse rather than
   disappearing. Existing detached code still launches the removed consumer
   entrypoint and must be replaced by the local controller queue.
3. Wire W8 local lifecycle. Dashboard still uses the separate supervisor and
   launches the removed `__dashboard-supervise` entrypoint. Start `--stay`,
   install/uninstall, managed-unit refresh, timer startup and the final wingd
   process arguments remain incomplete. Schedules need their final selectors,
   repeatability and pause/resume handlers. `__tick` is routed but still calls
   the old timer implementation.
4. Implement pools, `upgrade`, cache warm, and the deployment token path.
   `operator tokens` still reaches the legacy token handler. Team member tokens,
   reserved runner pools, final secret writes and final management routes need
   typed controller clients. Restricted `secrets set --only-on` refuses before
   storing an unrestricted secret. `profiles add --scope` refuses before writing.
5. Replace the remaining run detail/render/retry/prune/log clients. Their old
   store/detail models survive. Retry's final wire request has no failed/all-jobs
   mode, so agree on that contract before wiring those CLI flags.
6. Replace profile.Load/Save with W6's final machine decoder/persistence and use
   W2 final credential metadata for probing and revocation. Existing profile
   resolution is machine-only, but the loader still accepts the old schema.
7. Finish the source-guide and executable caller migration. Only generated CLI
   references and the profile section of getting-started were updated here.
   Other guides, snippets, scripts, info suggestions and older runtime tests
   still contain removed vocabulary. The full docs-honesty and full CLI suites
   have not been established green.

## Contract changes needed

- W2: the prior mixed trigger/job queue-claim route gap remains. One ordered
  claim must not be simulated by two independent scans.
- W2: agree on credential enrollment for `profiles add --scope` and the request
  for restricted secret writes. Neither is implemented by reusing an admin path.
- W1/W2: final retry transport needs the CLI's failed/all-jobs selection, or the
  owning contract must specify how those selections derive from intake.

Local W9 repair: `__complete --word` permits an empty value for root completion.
The C4 surface contract records it. No other area's contract was changed.
The prior local `EngineOptions.Launcher` repair is unchanged.

## Verification

Every Go invocation uses `GOWORK=off GOFLAGS=-buildvcs=false`, `-p 4`, and
GOCACHE/GOTMPDIR/TMPDIR beneath `/tmp/sw-astra-W9`.

- Whole-repository `go build -p 4 ./...` passed.
- `go vet -p 4` passed for cmd/sparkwing, clisurface, controllerwire,
  controller/client and tests/acceptance.
- Full clisurface and controllerwire tests passed.
- Engine client and acceptance grammar/transport slices passed:
  `TestEngineClient`, `TestSurfaceGrammarContract`,
  `TestSurfaceWireDetailTransport`.
- Focused CLI tests passed for production validation/help, checkout handling,
  error formatting and exits, run/trigger parsing, profiles, completion,
  help/discovery, generated-reference drift and cancellation.
- The store default-team caller check found a stale bounce entry from the prior
  session. Both that entry and the removed queued-cancel entry are deleted.
- `git diff --check` and `bin/sync-docs.sh --check` passed.

No full surface acceptance cell was unskipped. `surface-cli-vocabulary-and-flags`
and `surface-api-and-detail-parity` still require complete handler/runtime
coverage. The passing supplemental tests prove the stated slices only.
No Sparkwing executable, daemon, socket-based suite or pipeline was run.
The lead must run the real HTTP/api.sock integrations and gates.

The new cancellation tests port terminal replay, live cancellation, unknown-run
failure, batch continuation and retained results onto a controller fake.
Profile tests port credential storage, replacement refusal, revocation,
machine-token protection and config isolation. No W9-owned source test appears
directly in the T1 port inventory.

## Shared files and review

Outside W9 ownership, the changes are `pkg/store/default_team_callers_test.go`,
`CHANGELOG.md`, generated `pkg/docs/changelog.md` and `pkg/docs/mirror/cli-*.md`
plus `pkg/docs/mirror/getting-started.md`, the C4 surface contract note and this
handoff. No other area's runtime implementation changed.

Read-only review caught version being mistaken for a help-only group, cleanup
before validation, absent wingd routing, dropped run flags and trigger output
leaking into pipeline args. Those defects were corrected. The remaining runtime
holes are listed above. This is continuation work, not a landing verdict.

Mechanisms added: final grammar dispatch and metadata projection, a CLI error
formatter using the shared API type, and a controller-client resolver used by
cancellation. No new environment variable, credential grant or compatibility
alias was added. No git writes were made. The default Git identity remained
Korey Gambill and core.worktree stayed unset.

Deletion count for this session: 16,074 tracked lines removed,
including 7,021 Go lines. The total includes regenerated references and mirrors.
