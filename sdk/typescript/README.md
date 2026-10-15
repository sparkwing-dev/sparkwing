# @sparkwing/sdk

Declare Sparkwing pipelines in TypeScript. The host schedules jobs and owns
placement, retries, risk authorization, storage and credentials. SDK processes
build declarations and execute the bodies and closures the host requests.

```ts
import { definePipeline, main } from "@sparkwing/sdk";

definePipeline({
  name: "check",
  args: [{ name: "target", type: "string", default: "all" }],
  secrets: [{ name: "DEPLOY_TOKEN", optional: true }],
  plan(plan, run) {
    const build = plan.job("build", async ctx => {
      await ctx.exec(["npm", "run", "build"]).run();
      return { target: run.args.target };
    }).artifacts("dist/**");
    plan.job("report", async ctx => {
      const output = await ctx.output(build);
      await ctx.summary(`Built ${output?.target}`);
    }).needs(build).consumes(build, { into: "build-output" });
  },
});

await main();
```

Use an exact package version in `.sparkwing/package.json`. Its version selects
the matching engine. The package requires Node.js 22.18 or later. The public
API is [index.d.ts](index.d.ts).

Durations are milliseconds and memory is bytes. Omitted options remain unset
where the wire supports presence. Explicit zero remains zero, including an
indefinite approval timeout. Argument values stay strings, including `int64`.
String-array arguments use comma splitting, trimming and empty-item removal.

`Context` provides lazy secrets and plain variables, typed outputs, references,
annotations, summaries, acknowledged masking, OIDC, child runs and cleanup.
Command, Git, Docker, service, filesystem and memo helpers use the same context
and cancellation signal. Plan callbacks declare work; helpers that perform I/O
refuse calls during planning. Generated jobs reconstruct from recorded source
output. The host validates the reconstructed declarations against accepted state.

Step output reads stay within the current attempt, including retry hooks.
Cancellation reaches bodies and every closure kind. A callback that finishes
after cancellation still reports a failed invocation.
Service scopes also reject after cancellation when their callback returns normally.
They remove their containers before rejecting. If removal fails, host cleanup
stays registered and the cancellation error is preserved.

The host starts `node <entry> --sw-node-protocol`. Protocol 2 begins with hello,
which supplies granted runner identity and an optional in-memory service binding.
Requests and frames use private pipes. Standard output and error remain author
output. No preload or environment bearer is used. The SDK does not filter debug
logs or assign job identity to log frames.

On Windows, the host supplies decimal handles through
`--sw-protocol-handles <request>,<frame>`. A packaged native bridge converts them
to Node descriptors and clears inheritance. Build the x64 and arm64 bridges in
the matching Windows MSVC environments with:

```sh
node native/build.mjs --headers <node-headers/include/node> --node-lib <node.lib> --arch x64
```

The release package must contain `native/win32-x64/protocol.node` and
`native/win32-arm64/protocol.node`. `npm pack` checks both files and their
architecture and DLL headers. Native tests must also pass on each architecture
because header checks cannot prove that Node can load and use the bridge.
The bridge source is in [native/protocol.c](native/protocol.c). End users need
no compiler.

For development, run `npm ci`, `npm run typecheck`, `npm test` and `npm run build`.
The build emits ESM into `dist/`; package exports expose only the public root.
The release process stamps `package.json` and its lockfile with the engine
release version. Hello reads that package version.

Run `npm run test:native` from this directory with the repository's Go toolchain
installed and `GOWORK=off` when a parent workspace points at another checkout.
The test builds the SDK and launches a public-package fixture with inherited
Unix descriptors or Windows argv handles. It exchanges requests and replies
using the Go contract types and checks that a child tool cannot access the
protocol pipes. Use Node and Go for the same CPU architecture. On Windows,
build the matching native bridge first. Missing Node, TypeScript dependencies
or bridge binaries fail the test.

The Go acceptance suite also runs SDK boundary tests for the protocol scenarios.
These tests use native pipes and fake bound services. Run the native pipe test
on Windows to verify raw handles there. Controller authorization, store
transitions, artifact/memo persistence, real DinD and the six placement paths
require their corresponding integration hosts.
