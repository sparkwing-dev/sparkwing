# Sparkwing TypeScript SDK (prototype)

This package is a prototype. It is not published, not part of a release, and
its wire shapes change with the node protocol specification without notice.
Nothing in the Sparkwing engine starts it yet.

It shows how small a Sparkwing SDK becomes when the engine hosts execution:
the SDK builds plans and runs bodies, and the engine owns scheduling,
retries, timeouts, caching, secrets and log handling. The design is in
[design/engine-hosted-execution.md](../../design/engine-hosted-execution.md);
the wire shapes follow the node protocol specification (`docs/node-protocol.md`
and its JSON Schemas).

## What it contains

| File | Purpose |
|---|---|
| `src/plan.ts` | The plan DSL: `definePipeline`, `Plan.job`, `Job.needs`, `retry`, `timeout`, `step`, `skipIf` |
| `src/describe.ts` | The describe document (protocol version and pipelines) and the plan document types |
| `src/protocol.ts` | Stdio framing: requests on stdin, replies and log records on stdout |
| `src/runner.ts` | The loop that answers `describe`, `plan`, `run_node`, `run_step` and `eval` |
| `src/log.ts` | NDJSON log records with the engine's field names |
| `src/routes.ts` | Clients for the node routes: a secret by name, and another node's output through its signed grant |
| `examples/hello/pipeline.ts` | A two-job pipeline |

## A pipeline

```ts
import { definePipeline, main } from "@sparkwing/sdk";

definePipeline({
  name: "hello",
  plan(plan) {
    const build = plan.job("build", () => ({ digest: "sha-hello" }));
    plan
      .job("publish", async (ctx) => {
        const out = await ctx.output<{ digest: string }>("build");
        if (!out) throw new Error("build recorded no output");
        ctx.log.info(`published ${out.digest}`);
      })
      .needs(build)
      .retry(2)
      .timeout(60_000);
  },
});

process.exitCode = await main();
```

`node pipeline.ts --describe` prints the describe document. `node pipeline.ts
--sw-node-protocol` answers engine requests, one JSON object per line, until
stdin ends. The engine starts one such process per node attempt and sends
`plan` before `run_node`:

```text
-> {"id":"1","op":"plan","pipeline":"hello","args":{},"run":{"run_id":"r1","pipeline":"hello"}}
<- {"reply":"1","ok":true,"result":{"protocol":"1","pipeline":"hello","run_id":"r1","nodes":[...]}}
-> {"id":"2","op":"run_node","node":"build","attempt":1,"options":{"dry_run":false}}
<- {"ts":"...","level":"info","node":"build","msg":"building"}
<- {"reply":"2","ok":true,"result":{"outcome":"success","output":{"digest":"sha-hello"}}}
```

A stdout line with `reply` answers a request; every other line is a log
record. Requests run one at a time, except `eval`, which the SDK answers
while a node body runs.

## Running the tests

The package has no runtime dependencies and needs Node.js 22.18 or newer,
which runs TypeScript files directly.

```sh
node --test "test/**/*.test.ts"
```

`npm run typecheck` needs the `typescript` and `@types/node` dev
dependencies installed.
