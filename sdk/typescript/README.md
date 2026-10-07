# Sparkwing TypeScript SDK (prototype)

This package is a prototype. It is not published, not part of a release, and
its wire shapes change with the node protocol specification without notice.
Nothing in the Sparkwing engine starts it yet.

It shows how small a Sparkwing SDK becomes when the engine hosts execution:
the SDK builds plans and runs bodies, and the engine owns scheduling,
retries, timeouts, caching, secrets and log handling. The design is in
[docs/design/engine-hosted-execution.md](../../docs/design/engine-hosted-execution.md).

## What it contains

| File | Purpose |
|---|---|
| `src/plan.ts` | The plan DSL: `definePipeline`, `Plan.job`, `Job.needs`, `retry`, `timeout`, `step`, `skipIf` |
| `src/describe.ts` | The describe document (protocol version and pipelines) and the plan document types |
| `src/protocol.ts` | Stdio framing: requests on stdin, responses and log records on stdout |
| `src/runner.ts` | The loop that answers `describe`, `plan`, `run_node`, `run_step`, `call` and `shutdown` |
| `src/log.ts` | NDJSON log records with the engine's field names |
| `src/routes.ts` | Clients for the node routes: secrets and outputs |
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
serve` answers engine requests, one JSON object per line:

```text
-> {"id":1,"method":"plan","params":{"pipeline":"hello","run_id":"r1","args":{}}}
<- {"id":1,"result":{"pipeline":"hello","nodes":[...]}}
-> {"id":2,"method":"run_node","params":{"pipeline":"hello","run_id":"r1","args":{},"node":"build"}}
<- {"ts":"...","level":"info","node":"build","msg":"..."}
<- {"id":2,"result":{"outcome":"success","output":{"digest":"sha-hello"}}}
```

A stdout line with a numeric `id` answers a request; every other line is a
log record.

## Running the tests

The package has no runtime dependencies and needs Node.js 22.18 or newer,
which runs TypeScript files directly.

```sh
node --test "test/**/*.test.ts"
```

`npm run typecheck` needs the `typescript` and `@types/node` dev
dependencies installed.
