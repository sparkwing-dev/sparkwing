# W9 full-area review fixes

Session base: `0a7f86f2e`. No Git writes. Lead-owned amendments and handoffs
are unchanged.

Ruling 4 and the Invocation simplification are implemented. Rulings 5 and 7
remain open. Earlier sessions closed 1, 2, 3, 6, 8, 9, 10, 11 and 12. The
latest native report still contains FAIL for `./internal/projectconfig`, a
removed package. The harness should target `./pkg/projectconfig`. This session
cannot replace the lead's native evidence.

Every Sparkwing handler now consumes the original parsed Invocation. The
production CLI has one clisurface.Parse call. Reconstructed argv, repeated
parsing, pflag declarations, old parent dispatchers, binding helpers and unused
binding metadata are deleted. The registry flag sweep covers public and hidden
Sparkwing leaves, including short spellings. Pipeline help still inspects the
selected pipeline. Generic flag sweeps request engine help without a pipeline.

Repository add, remove and info use the global --checkout. Removal now reaches
the registered checkout instead of requiring a positional argument that the
public parser refuses. Add/remove emit JSON receipts containing action, absolute
path and affected count. Prune and checkout listings honor plain output.
Version holds, state recovery and sparks lint/update honor selected output,
including JSON when piped. Docs listing filters ignore global help=false flags.
Invalid docs selectors return usage errors. Required-flag errors retain the
registry's explanation. Local run's --profile refusal retains trigger guidance.

Engine handoff replays the original argv from the original directory. This
preserves relative --checkout resolution and opaque pipeline arguments.
The root command owns checkout selection. Upgrade uses the selected directory
rather than resolving --checkout again. Its real-parser regression passes. A real
Unix self-exec test parses the invocation and checks the replacement process's
directory and arguments, without replacing preparation or the OS adapter.

Invariant: execution receives the command, explicit flag values and pipeline
arguments accepted by the public parser unchanged. Mutation stdout obeys the
selected format. The docs/global-flag, repository receipt and utility receipt
regressions ran red before their fixes. Existing fixture tests enter through
public parsing or test-only adapters into the real Invocation handlers. Their
existing injected dependencies remain component evidence. No new regression
uses injected preparation or a fake product adapter.

All five requested simplifications are now implemented. Earlier sessions
removed claim_paths.go, runSparksResolve, redundant client-constructor wrapping,
unsupported profile scope, unused completion bookkeeping and deferred onboarding
prose. This session removes the remaining second grammar. No whole production
file was deleted this session. The obsolete code was deleted in place.

Remaining work:

1. Ruling 5: capture present, allow-listed run.submit_env_allow values, retain
   and transport them privately, consume them in hosts and mask them. Trigger's
   unconditional refusal remains. Values must stay out of the public names/digest
   snapshot. The existing private local environment owner is available.
2. Ruling 7: implement the retained options below. Their integration refusals
   remain. No retained run option was cut this session.
3. Lead native verification and review. Socket-dependent tests, full CLI/bin
   suites and native Windows/Darwin execution remain with the harness.

| Option | Kept or cut | Source and state |
| --- | --- | --- |
| Relative --sw-priority | Kept, pending | C4 Registry/run and settings priority. Resolve queue position before sealing. |
| --sw-no-bincache | Kept, pending | C4 Registry/run. Bind the uncached compiler to retained preparation. |
| --sw-hash-all-files | Kept, pending | C4 Registry/run. Bind the retained source/hash policy. |
| --sw-allow-unadmitted | Kept, pending | C4 Registry/run and decision 91's local admission model. |
| --sw-index | Kept, pending | C4 Registry/run and procenv.Runtime.GateIndex. Retain a verified private snapshot. |
| --target | Kept, pending | C4 Registry/run. Bind the selected target through preparation. |
| --sw-request-id | Kept, implemented previously | C4 Registry/run and local-execution's detached tracing contract. |
| Hooks --fleet and --all | Cut previously | Lead ruling 4 and C4's --all-repos grammar. No aliases. |
| profiles set --token | Cut previously | Lead ruling 12. Use --token-stdin. |
| profiles add --scope | Cut previously | Lead simplification 5. Use member credentials. |

Contract changes made: C4 records direct Invocation consumption, original engine
replay and utility output. These clarify existing grammar and output contracts.
There is no new wire/schema field, grant, route, configuration key, environment
variable, execution limit or compatibility adapter. The replay directory stays
private to the invoking process. The private CLI signature cuts needed no
production caller migration outside W9. No other-area contract change is needed
for the work completed here.

Shared files touched: CHANGELOG.md, pkg/docs/changelog.md, docs/cli.md,
docs/hooks.md, their embedded mirrors, and C4. All production Go edits are under
cmd/sparkwing or internal/clisurface. Other changed files adapt existing CLI
fixture tests to Invocation. Four tests of removed binding/help-scanning helpers
were retired. Old positional acceptance tests now assert the public usage error.
No archived inventory test was newly ported or acceptance cell newly unskipped.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
TMPDIR=/tmp/sw-astra-W9 and GOTMPDIR=/tmp/sw-astra-W9/go, with at most four package
workers per command.

- Whole-repository go build -p 4 ./... passes.
- CLI and clisurface vet passes.
- 463 selected tests from changed CLI test files pass under -short, excluding
  socket fixtures. The new regression and self-exec tests also pass separately.
- Complete clisurface, controllerwire and controller/client suites pass.
- pkg/docs passes with TestWebClient excluded. Its HTTP fixture cannot listen
  inside this sandbox. The lead's prior native pkg/docs suite passes.
- Surface and EngineAmendments acceptance families pass.
- Doccheck, registry reference drift, embedded mirror checks, commentcheck
  against main, sleepcheck against HEAD and whitespace checks pass.

The prior amendment/handoff audit still applies: A1-09/16/17/38/41/43/49/50/51/52,
source attempts, step reports/windows, pipeline identity, launcher grammar,
doctor, local composition, owner/eval separation, shared logs, service handover
and decisions 110/111/113/114 retain their implementations. A1-53 and deployed
Cloud/DinD, native platforms, fresh-machine distribution and PostgreSQL remain
lead integration lanes. No foreign stub blocked this session.

Production accounting: +632/-1532, net -900 this session. W9 versus main is
+19,438/-25,159, net -5,721, within the -2,900 budget. Counts exclude tests,
testdata and contract declaration files. Scratch is removed after checks join.
The shared build cache is kept.

Friction: the native harness still names the removed internal/projectconfig
package. Initial broader local selections hit the expected socket refusal.
Focused selections exclude those fixtures and leave their evidence to the lead.
The installed Overwing binary reports source lag. No tool availability issue
blocked the implementation. Remaining implementation is rulings 5 and 7.

STATUS: CONTINUE
