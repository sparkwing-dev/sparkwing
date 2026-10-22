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
    const build = plan
      .job("build", async (ctx) => {
        await ctx.exec(["npm", "run", "build"]).run();
        return { target: run.args.target };
      })
      .artifacts("dist/**");
    plan
      .job("report", async (ctx) => {
        const output = await ctx.output(build);
        await ctx.summary(`Built ${output?.target}`);
      })
      .needs(build)
      .consumes(build, { into: "build-output" });
  },
});

await main();
```

Declare allowed child runs with `job.startsChildren("deploy")` for the parent
repository or `job.startsChildrenIn("gh:123", "publish")` for an explicit canonical
repository key. Calls append targets and can be chained.

Use an exact package version in `.sparkwing/package.json`. Its version selects
the matching engine. The package requires Node.js 22.18 or later. The public
API is [index.d.ts](index.d.ts).

Numeric duration options use milliseconds. Memory uses bytes. Omitted options
remain unset. Explicit zero remains zero, including an
indefinite approval timeout. Argument values stay strings, including `int64`.
The host validates arguments after describe and before planning. Empty defaults
mean no default. Required arguments with non-empty defaults are refused. NaN and
infinity are refused. Errors come from host preparation as `invalid_args`.
Duration arguments use Go syntax such as `1h30m`, including `0`, `+0` and `-0`.
String-array arguments use comma splitting, trimming and empty-item removal in
both the raw argument map and typed values. Enum checks apply to each list item.
SDK declarations and direct `toDoc()` calls rely on host validation of argument
schemas, plan graphs and secret declarations. Annotation locations are validated
by the host service; TypeScript retains transport type checks.

`Context` provides lazy secrets and plain variables, typed outputs, references,
annotations, summaries, acknowledged masking, OIDC, child runs and cleanup.
Command, Git, Docker, service, filesystem and memo helpers use the same context
and cancellation signal. Plan callbacks declare work; helpers that perform I/O
refuse calls during planning. Generated jobs reconstruct from recorded source
output. The host validates the reconstructed declarations against accepted state.

Step output reads stay within the current attempt. Before a step condition or
body runs, the host supplies the terminal predecessor state. A skipped predecessor
has no local output. A terminal step cannot run again in the same attempt.
Run facts stay read-only across planning and retries, including trigger details
and nested pull-request facts.
Cancellation reaches bodies and every closure kind. A callback that finishes
after cancellation still reports a failed invocation. The SDK acknowledges
cancellation after the selected operations finish and receive their own replies.
Closure evaluations run in request order. Canceling a queued evaluation returns
without calling its author code or waiting for the running closure. Overlapping
generator replays return the recorded declaration after the first evaluation.
Service scopes also reject after cancellation when their callback returns normally.
They remove their containers before rejecting. If removal fails, host cleanup
stays registered and the cancellation error is preserved.

Git fileset hashes omit files beneath literal directory paths in `.dockerignore`,
including paths written without a trailing slash. Included file edits still
change the hash.

The host starts `node <entry> --sw-node-protocol`. Protocol 2 begins with hello,
which supplies granted runner identity and an optional in-memory service binding.
Requests and frames use private pipes. Standard output and error remain author
output. No preload or environment bearer is used. The SDK does not filter debug
logs or assign job identity to log frames.

Every encoded frame must fit the 17 MiB wire limit. Oversized logs fail before
writing. Typed job and step outputs also have a separate 16 MiB limit.
Generated declarations use the frame budget. Planning diagnostics mask declared
secret argument defaults and supplied values in messages, hints and nested
details, including keys. Execution masking belongs to the host.

Tracked memo inputs encode each path, entry type and file bytes or symlink
target. They never read a symlink's target bytes. Tree inputs hash regular files,
skip symlink entries and reject roots that traverse symlinks.

On Windows, the host supplies decimal handles through
`--sw-protocol-handles <request>,<frame>`. A packaged native bridge converts them
to Node descriptors and clears inheritance before SDK tools spawn. Independently
imported modules may execute before SDK initialization.

Build and verify each bridge in its matching native Windows MSVC environment,
with Node and Go running on that architecture:

```sh
npm run release:native -- --version <engine-version> --headers <node-headers/include/node> --node-lib <node.lib> --arch x64
```

Repeat with `--arch arm64` on native Windows arm64. The command stamps both npm
manifests, builds the bridge and runs `TestNativeProtocol` and
`TestNativeConformance`. Missing, skipped or failed native tests stop the build.
Each output directory contains `protocol.node` and a `verification.json` record
bound to the engine version, bridge bytes and SDK sources.

Build both architectures from the same source and version. Transfer their output
directories to the package checkout, then assemble the package:

```sh
npm run release:pack -- --version <engine-version> --destination <output-directory>
```

The release package must contain `native/win32-x64/protocol.node` and
`native/win32-arm64/protocol.node`. `npm pack` refuses the development version
and inconsistent lockfile versions. It also checks both bridge files and their
architecture and DLL headers. Both matching native verification records are
required because header checks cannot prove that Node can load and use a bridge.
The bridge source is in [native/protocol.c](native/protocol.c). End users need
no compiler.

For development, run `npm ci`, `npm run typecheck`, `npm test` and `npm run build`.
The build emits ESM into `dist/`; package exports expose only the public root.
The release process stamps `package.json` and its lockfile with the engine
release version. Hello reads that package version.

Run `npm run test:native` from this directory with the repository's Go toolchain
installed and `GOWORK=off` when a parent workspace points at another checkout.
The tests build the SDK and launch public-package fixtures with inherited
Unix descriptors or Windows argv handles. It exchanges requests and replies
using the Go contract types and check that a child tool cannot access the
protocol pipes. A second fixture runs the same v2 corpus as the Go SDK, including
strict plan validation, generated hashes, replay and canonical error envelopes. Use Node and Go for the same CPU architecture. On Windows,
build the matching native bridge first. Missing Node, TypeScript dependencies
or bridge binaries fail the test.

The Go acceptance suite also runs SDK boundary tests for the protocol scenarios.
These tests use native pipes, fake bound services and a temporary Git file remote.
Run the native pipe test on Windows to verify raw handles there. Controller
authorization, store transitions, artifact/memo persistence, real DinD and the
six placement paths require their corresponding integration hosts.
