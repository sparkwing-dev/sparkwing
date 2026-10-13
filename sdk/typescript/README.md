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
| `src/plan.ts` | The plan DSL: `definePipeline`, `Plan.job`, `Plan.approval`, the job envelope (`needs`, `retry`, `timeout`, `requires`, `concurrency`, `resources`, `cacheDir`, `memoize`, `onFailure`), its closures (`skipIf`, `beforeRun`, `verify`, `afterRun`, `generate`), and steps (`step`, `Step.result`, `skipIf`, `finally`, `dryRun`) |
| `src/describe.ts` | The describe document (protocol version, SDK and pipelines) and the plan document types |
| `src/protocol.ts` | Framing: requests on the request pipe, replies and log records on the frame pipe, and the reply error codes |
| `src/runner.ts` | The loop that answers `describe`, `plan`, `run_node`, `run_step`, `cancel` and `eval` |
| `src/main.ts` | The `--sw-node-protocol` entry: finds the pipes on descriptors 3 and 4 and takes the bearer out of the environment |
| `src/log.ts` | NDJSON log records with the engine's field names |
| `src/routes.ts` | Clients for the node routes at `SPARKWING_NODE_URL`: a secret by name, and another node's output |
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

The engine starts `node pipeline.ts --sw-node-protocol` with the request pipe
on descriptor 3 and the frame pipe on descriptor 4, and sends one JSON request
per line. stdout and stderr stay with pipeline code; the engine never parses
them. The engine starts one process per node attempt and sends `plan` before
`run_node`:

```text
-> {"id":"1","op":"plan","pipeline":"hello","args":{},"run":{"run_id":"r1"}}
<- {"reply":"1","ok":true,"result":{"protocol":"1","pipeline":"hello","run_id":"r1","nodes":[...]}}
-> {"id":"2","op":"run_node","node":"build","attempt":1}
<- {"ts":"...","level":"info","node":"build","msg":"building"}
<- {"reply":"2","ok":true,"result":{"outcome":"success","output":{"digest":"sha-hello"}}}
```

A frame with `reply` answers a request; every other frame is a log record.
Requests run as they arrive, since the engine may have several `eval` and
`run_step` requests in flight. A failed reply carries the protocol's
`error.code`, such as `no_plan` or `unknown_node`.

The engine's conformance suite (`internal/nodeproto/conformance`) drives
`test/fixtures/conformance.ts`, the same fixture pipeline the Go SDK
implements, through every request and checks each reply against the node
protocol's schemas.

Not supported yet: Windows, where the protocol pipes are inherited handles;
plan-level concurrency groups with `box` scope, whose key needs the engine's
machine id; and the plan document fields the Go SDK has that this package
does not declare (artifacts, pipeline references, tools, `whenRunner`,
`inline`, step groups and risks, plan priority and admission class).

## Running the tests

The package has no runtime dependencies and needs Node.js 22.18 or newer,
which runs TypeScript files directly.

```sh
node --test "test/**/*.test.ts"
```

`npm run typecheck` needs the `typescript` and `@types/node` dev
dependencies, which `npm ci` installs. The repository's `gate` pipeline runs
both.
