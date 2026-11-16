## Queue budget reports applied enforcement

**Before:** `sparkwing queue list -o json` carried `budget.enforce: true` whenever
`admission.budget` included `enforce`, and the plain `budget` row printed `true` or
`false` in its sixth column. Neither said whether the OS applied a limit.

```json
{"budget": {"cores": 2, "enforce": true}}
```

**After:** `budget.enforcement` is present only when the budget asks for
enforcement. It reports what the kernel applies to the local runner right now:

```json
{"budget": {"cores": 2, "enforcement": {"applied": true, "cpu_cores": 2, "nice": 10}}}
{"budget": {"cores": 2, "enforcement": {"applied": false, "reason": "the local runner's cgroup has no CPU limit"}}}
```

The plain row's sixth column prints `enforced`, `not-enforced` or `-` (not
requested). Go callers of `pkg/wingwire` replace `BudgetState.Enforce` with
`BudgetState.Enforcement`, a `*BudgetEnforcement`.

- Treat `budget.enforcement.applied` as the answer to "is Sparkwing capped by the
  OS?" A missing object means enforcement was not requested.
- To ask whether enforcement was requested, read `budget.raw` for the `enforce`
  token.
