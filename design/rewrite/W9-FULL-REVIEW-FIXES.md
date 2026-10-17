# W9 full-area review fixes

Base: `195cf2156`. No Git writes. Lead-owned amendments and handoffs are unchanged.
The latest native report still contains FAIL. The lead must commit, review and
rerun its native harness. Its `./internal/projectconfig` target is stale. The
package is `./pkg/projectconfig`.

Closed rulings: 1, 2, 3, 6, 8, 9, 10, 11 and 12. Ruling 4 is partly implemented.
Rulings 5 and 7 remain open.

This session implements:

- Hooks install, uninstall and status consume the parsed Invocation. Their
  duplicate pflag declarations and the old hooks dispatcher are deleted.
  `--all-repos` works. `--fleet` and hooks `--all` are refused. Mutations emit
  resulting hook status as JSON by default when piped. Progress and warnings go
  to stderr. Existing chain/proof tests now enter through the real public parser.
- Pipeline scaffolding consumes Invocation and emits a JSON pipeline/path
  receipt or a plain pipeline name after dependency resolution succeeds. Pretty
  output keeps the guidance. The duplicate parser and bootstrap prose renderer
  are deleted. Real SDK fixtures exercise JSON, plain and piped defaults.
- A flag sweep sends every advertised sparkwing leaf-command flag through its
  execution parser. It also exposed cache handlers binding the removed `--all`
  spelling. They now bind `--all-entries`. Legacy parser helpers accept the
  globally registered `--help-all` and stop rejecting registered `-o` selections.
  Removing those remaining parser helpers belongs to the open Invocation work.
- Detached `--sw-request-id` becomes optional Request.RequestID. Native SDK
  preparation, the HTTP client, local controller, store intake, retry and the
  detached JSON receipt retain it. Separate submissions with the same trace ID
  receive distinct run IDs. Idempotency remains a separate contract.
- A released-engine fixture has an exact SDK version and no replacement. It
  builds a real release-version CLI, verifies signed policy and all six release
  downloads, then installs and describes the SDK through the selected engine.
  A fresh resolver repeats installation and inspection offline from the verified
  cache with no network requests. Cached policy is read and corrupted engine
  bytes are refused. SDK module bytes come from a generated local module proxy.
  Preparation, installers, SDK processes and environment snapshots are real.
  Only the external release HTTP transport serves fixture bytes.
- Generated CLI documentation now matches the registry, including the prior
  profiles scope cut. OpenAPI and embedded docs are regenerated.

Invariants: registered grammar reaches execution unchanged. Mutation stdout
obeys the selected format. A trace ID survives the run without becoming a replay
key. Released offline execution uses authenticated policy and executable bytes.
The parser/output and request-ID regressions ran red before product changes.
The released-engine addition is integration evidence for existing behavior.

Previous sessions completed native here intake and reason sealing, accepted hook
output, fail-closed repository baseline comparison, masked Git proxy environments,
alias metadata joins, decision-114 launch guides, and the profile token/scope cuts.
Four simplifications remain complete: claim_paths.go and runSparksResolve are
removed, client constructors return newEngineClient directly, and unsupported
profile scope, unused completion bookkeeping and deferred onboarding prose are
removed. The Invocation simplification now covers hooks and scaffolding only.

Remaining work:

1. Finish ruling 4 and simplification 1. Pass Invocation through every remaining
   handler and remove argv reconstruction, repeated parsing and duplicate flag
   declarations. The public flag sweep passes, but that does not finish the
   structural cut or prove every handler's non-help output behavior.
2. Ruling 5. Capture filtered run.submit_env_allow values, transport and retain
   them privately, consume them in hosts and mask them. The unconditional trigger
   refusal remains. Values must not enter the public names/digest snapshot.
3. Ruling 7. Implement the retained switches below. Their integration refusals
   remain. No run option was cut this session.

| Option | Kept or cut | Source and state |
| --- | --- | --- |
| Relative --sw-priority | Kept, pending | C4 Registry/run and settings priority. Resolve queue position before sealing. |
| --sw-no-bincache | Kept, pending | C4 Registry/run. Bind the existing uncached compiler to retained preparation. |
| --sw-hash-all-files | Kept, pending | C4 Registry/run. Bind the retained source/hash policy. |
| --sw-allow-unadmitted | Kept, pending | C4 Registry/run and decision 91's retained local admission model. |
| --sw-index | Kept, pending | C4 Registry/run and procenv.Runtime.GateIndex. Retain a verified private snapshot. |
| --target | Kept, pending | C4 Registry/run. Bind the selected target through preparation. |
| --sw-request-id | Kept, implemented | C4 Registry/run, detached tracing contract in local-execution. |
| Hooks --fleet and --all | Cut | Lead ruling 4 and C4's --all-repos grammar. No aliases. |
| profiles set --token | Cut previously | Lead ruling 12. Use --token-stdin. |
| profiles add --scope | Cut previously | Lead simplification 5. Use current member credentials. |

Contract changes made: Request.RequestID adds optional trace metadata to existing
request JSON. C1 data, C3 controller-wire and C4 surface contracts record sealing,
retry and receipt behavior. C4 also records hook and scaffolding output. There
is no new schema column, route, grant, configuration key, environment variable,
timeout, limit or compatibility path. No contract change is needed from another
area for the work completed here.

Shared files touched: internal/runspec/contracts.go, api/openapi.yaml,
CHANGELOG.md, pkg/docs/changelog.md and embedded mirrors, plus C1/C3/C4 contracts.
No API removal required a caller migration outside W9. Test-only parser adapters
replace the old []string entry points in existing hook and scaffold tests. They
call runSparkwing and do not inject preparation or product implementations.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
TMPDIR=/tmp/sw-astra-W9 and GOTMPDIR=/tmp/sw-astra-W9/go, with at most four package
workers per test command.

- Whole-repository go build -p 4 ./... passes.
- Focused vet passes for CLI, clisurface, controllerwire, runspec and client.
- Complete clisurface, controllerwire, runspec and controller/client suites pass.
- The changed-test selection covers 93 CLI tests, including the registry flag
  sweep, native request tracing, released-engine offline inspection, scaffolding,
  hook chains, Git proofs and generated reference drift. Its first run found one
  test still capturing status output from stderr. After correcting that capture,
  all 93 focused tests pass together (15.482s).
- Surface and EngineAmendments acceptance families pass. No acceptance cell was
  newly unskipped and no archived inventory test was newly ported.
- Doccheck, OpenAPI generator comparison, embedded mirror check, commentcheck
  against main, sleepcheck against HEAD and whitespace checks pass.
- Native sockets, full CLI/bin suites and Windows/Darwin distribution remain
  with the lead harness. The latest native report is still failing and is not
  replaced by these focused results.

The previous amendment/handoff audit remains: A1-09/16/17/38/41/43/49/50/51/52,
source attempts, step reports/windows, pipeline identity, launcher grammar,
doctor, local composition, owner/eval separation, shared logs, service handover
and decisions 110/111/113/114 retain their completed implementations. A1-53 and
deployed Cloud/DinD, native platforms, fresh-machine distribution and PostgreSQL
remain lead integration lanes. No foreign stub blocked this session.

Production budget accounting this session: +97/-170, net -73. RequestID adds one
line in the excluded declaration file. W9 versus main: +19,193/-24,015, net
-4,822, within the -2,900 budget. No whole production file was deleted this
session. Obsolete parsers, the dispatcher and bootstrap renderer were deleted
in place. Scratch is removed after checks join. The shared build cache is kept.

Friction: the native harness still names a removed package, and the installed
Overwing binary reports source lag. Remaining work is rulings 4/5/7 and the lead's
native/review evidence. Rulings 8 and 11 no longer need implementation.

STATUS: CONTINUE
