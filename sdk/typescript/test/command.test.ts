import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { closeSync, openSync } from "node:fs";
import { mkdtemp, mkdir, rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import type { TestContext } from "node:test";
import type { Context, ExecOptions } from "../index.d.ts";
import {
  cleanupContext,
  createCommand,
  ExecError,
  exitNotStarted,
  registerCommandContext,
  runCommand,
  runInput,
  withCommandEnv,
  withSignal,
} from "../src/command.ts";
import { runScope } from "../src/scope.ts";

interface CommandEvent {
  event: string;
  attrs: Record<string, unknown>;
}

function context(signal = new AbortController().signal) {
  const events: CommandEvent[] = [];
  const unused = (): never => {
    throw new Error("unexpected context call");
  };
  const log = { debug() {}, info() {}, warn() {}, error() {} };
  const ctx: Context = {
    get run(): Context["run"] {
      return unused();
    },
    jobId: "command-test",
    args: {},
    dryRun: false,
    runner: { name: "test", type: "local", labels: [] },
    signal,
    log,
    secret: unused,
    variable: unused,
    output: unused,
    stepOutput: unused,
    annotate: unused,
    summary: unused,
    mask: unused,
    oidcToken: unused,
    runPipeline: unused,
    latest: unused,
    cleanup: unused,
    exec(argv, options) {
      return createCommand(ctx, argv, options);
    },
    bash(script, options) {
      return createCommand(ctx, ["bash", "-c", script], options);
    },
  };
  registerCommandContext(ctx, (event, attrs) => events.push({ event, attrs }), { BODY_VALUE: "body" });
  return { ctx, events };
}

function node(ctx: Context, script: string, options?: ExecOptions) {
  return ctx.exec([process.execPath, "-e", script], options);
}

async function project(t: TestContext) {
  const root = await mkdtemp(join(dirname(fileURLToPath(import.meta.url)), ".command-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}

test("commands are lazy and execute literal argv without a shell", async (t) => {
  const root = await project(t);
  const { ctx, events } = context();
  await runScope({ phase: "execute", root }, async () => {
    const argv = ["a b", "$(echo injected)", "`echo injected`", "", "a;b"];
    const cmd = ctx.exec([process.execPath, "-e", "require('node:fs').writeSync(1, JSON.stringify(process.argv.slice(1)))", ...argv]);
    assert.equal(events.length, 0);
    assert.deepEqual(await cmd.json(), argv);
    assert.deepEqual(
      events.map((event) => event.event),
      ["exec_start", "exec_end"],
    );
  });
});

test("run streams complete and trailing lines while capture preserves exact bytes", async (t) => {
  const root = await project(t);
  const { ctx, events } = context();
  await runScope({ phase: "execute", root }, async () => {
    const script = "require('node:fs').writeSync(1, 'first\\r\\nsecond'); require('node:fs').writeSync(2, 'warning\\n')";
    const result = await node(ctx, script).run();
    assert.equal(result.stdout, "first\r\nsecond");
    assert.equal(result.stderr, "warning\n");
    assert.equal(result.exitCode, 0);
    assert.deepEqual(
      events
        .filter((value) => value.event === "exec_line")
        .map((value) => [value.attrs.stream, value.attrs.line])
        .sort(),
      [
        ["stderr", "warning"],
        ["stdout", "first"],
        ["stdout", "second"],
      ],
    );
    assert.equal(events[0]?.event, "exec_start");
    assert.equal(events.at(-1)?.event, "exec_end");
    events.length = 0;
    assert.equal((await node(ctx, script).capture()).stdout, result.stdout);
    assert.deepEqual(
      events.map((event) => event.event),
      ["exec_start", "exec_end"],
    );
    assert.equal(typeof events.at(-1)?.attrs.duration_ms, "number");
  });
});

test("result helpers trim, parse, assert empty and preserve execution errors", async (t) => {
  const root = await project(t);
  const { ctx } = context();
  await runScope({ phase: "execute", root }, async () => {
    assert.equal(await node(ctx, "require('node:fs').writeSync(1, '  value\\n')").string(), "value");
    assert.deepEqual(await node(ctx, "require('node:fs').writeSync(1, '  first\\n\\n second \\n')").lines(), ["first", "second"]);
    assert.deepEqual(await node(ctx, "require('node:fs').writeSync(1, '{\"ok\":true}')").json(), { ok: true });
    await node(ctx, "require('node:fs').writeSync(1, ' \\n')").mustBeEmpty("expected empty");
    await assert.rejects(
      node(ctx, "require('node:fs').writeSync(1, 'changed.txt')").mustBeEmpty("formatting drift"),
      /formatting drift.*changed.txt/s,
    );
    await assert.rejects(node(ctx, "require('node:fs').writeSync(1, 'invalid-json')").json(), /JSON/);
    const failing = () =>
      node(ctx, "require('node:fs').writeSync(1, 'partial'); require('node:fs').writeSync(2, 'failure'); process.exitCode = 9");
    for (const execute of [
      () => failing().run(),
      () => failing().capture(),
      () => failing().string(),
      () => failing().lines(),
      () => failing().json(),
      () => failing().mustBeEmpty("empty"),
    ]) {
      await assert.rejects(execute(), (error) => {
        assert.ok(error instanceof ExecError);
        assert.equal(error.exitCode, 9);
        assert.equal(error.stdout, "partial");
        assert.equal(error.stderr, "failure");
        assert.equal(error.terminated, "");
        assert.match(error.message, /exit 9.*failure/s);
        return true;
      });
    }
  });
});

test("command env precedence and context derivation preserve bindings", async (t) => {
  const root = await project(t);
  const { ctx, events } = context();
  await runScope({ phase: "execute", root }, async () => {
    const first = withCommandEnv(ctx, { BODY_VALUE: "context", LEFT: "first" });
    const second = withCommandEnv(first, { RIGHT: "second" });
    assert.equal(second.log, ctx.log);
    assert.equal(second.signal, ctx.signal);
    assert.equal(second.output, ctx.output);
    assert.deepEqual(
      await node(second, "require('node:fs').writeSync(1, JSON.stringify([process.env.BODY_VALUE, process.env.LEFT, process.env.RIGHT]))", {
        env: { BODY_VALUE: "option" },
      })
        .env("BODY_VALUE", "builder")
        .envMap({ RIGHT: "" })
        .json(),
      ["builder", "first", ""],
    );
    assert.equal(await node(ctx, "require('node:fs').writeSync(1, process.env.BODY_VALUE)").string(), "body");
    assert.equal(events.filter((event) => event.event === "exec_start").length, 2);
  });
});

test("commands preserve prototype-shaped environment names through every env setter", async (t) => {
  const root = await project(t);
  const { ctx } = context();
  const script = "require('node:fs').writeSync(1, JSON.stringify([process.env.__proto__, process.env.constructor]))";
  const values: Record<string, string> = JSON.parse('{"__proto__":"value","constructor":"constructor-value"}');
  await runScope({ phase: "execute", root }, async () => {
    const commands = [
      node(ctx, script, { env: values }),
      node(ctx, script).env("__proto__", "value").env("constructor", "constructor-value"),
      node(ctx, script).envMap(values),
      node(withCommandEnv(ctx, values), script),
    ];
    for (const command of commands) assert.deepEqual(await command.json(), ["value", "constructor-value"]);
  });
});

test("relative command cwd resolves at the project root", async (t) => {
  const root = await project(t);
  const sub = join(root, "sub");
  await mkdir(sub);
  const { ctx } = context();
  await runScope({ phase: "execute", root }, async () => {
    assert.equal(await node(ctx, "require('node:fs').writeSync(1, process.cwd())").string(), root);
    assert.equal(await node(ctx, "require('node:fs').writeSync(1, process.cwd())").dir("sub").string(), sub);
    assert.equal(await node(ctx, "require('node:fs').writeSync(1, process.cwd())", { cwd: "sub" }).string(), sub);
    await assert.rejects(node(ctx, "").dir("missing").run(), (error) => {
      assert.ok(error instanceof ExecError);
      assert.equal(error.exitCode, exitNotStarted);
      assert.equal(error.terminated, "");
      return true;
    });
  });
  await runScope({ phase: "execute" }, async () => {
    assert.equal(await node(ctx, "require('node:fs').writeSync(1, process.cwd())").dir(sub).string(), sub);
  });
});

test("launch failures and signal exits retain distinct diagnostics", async (t) => {
  const root = await project(t);
  const { ctx } = context();
  await runScope({ phase: "execute", root }, async () => {
    await assert.rejects(ctx.exec([join(root, "does-not-exist")]).capture(), (error) => {
      assert.ok(error instanceof ExecError);
      assert.equal(error.exitCode, exitNotStarted);
      assert.equal(error.terminated, "");
      assert.ok(error.cause instanceof Error);
      assert.match(error.message, /failed to start/);
      return true;
    });
    if (process.platform !== "win32") {
      await assert.rejects(node(ctx, "process.kill(process.pid, 'SIGTERM')").capture(), (error) => {
        assert.ok(error instanceof ExecError);
        assert.equal(error.exitCode, exitNotStarted);
        assert.equal(error.terminated, "signal");
        return true;
      });
    }
  });
});

test("cancellation preserves output and derived cleanup contexts can execute", async (t) => {
  const root = await project(t);
  const controller = new AbortController();
  const reason = new Error("cancelled by test");
  const { ctx, events } = context(controller.signal);
  await runScope({ phase: "execute", root }, async () => {
    await assert.rejects(
      runCommand(
        ctx,
        [process.execPath, "-e", "require('node:fs').writeSync(1, 'ready'); Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0)"],
        {
          stdout() {
            controller.abort(reason);
          },
        },
      ),
      (error) => {
        assert.ok(error instanceof ExecError);
        assert.equal(error.terminated, "cancellation");
        assert.equal(error.exitCode, exitNotStarted);
        assert.equal(error.cause, reason);
        assert.equal(error.stdout, "ready");
        return true;
      },
    );
    await assert.rejects(node(ctx, "").run(), { terminated: "cancellation", exitCode: exitNotStarted });
    assert.equal(await node(cleanupContext(ctx), "require('node:fs').writeSync(1, process.env.BODY_VALUE)").string(), "body");
    const other = new AbortController();
    assert.equal(withSignal(ctx, other.signal).signal, other.signal);
    assert.equal(events.filter((event) => event.event === "exec_end").length, 3);
  });
});

test("input goes through stdin and callbacks receive unmodified bytes", async (t) => {
  const root = await project(t);
  const { ctx, events } = context();
  await runScope({ phase: "execute", root }, async () => {
    const bytes: Uint8Array[] = [];
    const script =
      "require('node:fs').writeSync(1, require('node:fs').readFileSync(0)); require('node:fs').writeSync(2, Buffer.from([0,255,10]))";
    const output = await runCommand(ctx, [process.execPath, "-e", script], {
      input: "secret-input",
      capture: true,
      stderr: (data) => bytes.push(data),
    });
    assert.equal(output.stdout, "secret-input");
    assert.deepEqual(Buffer.concat(bytes), Buffer.from([0, 255, 10]));
    assert.ok(!JSON.stringify(events).includes("secret-input"));
    assert.equal(
      (await runInput(ctx, [process.execPath, "-e", "require('node:fs').writeSync(1, require('node:fs').readFileSync(0))"], "password"))
        .stdout,
      "password",
    );
  });
});

test("cancellation closes output inherited by child processes", { timeout: 5000 }, async (t) => {
  const root = await project(t);
  const controller = new AbortController();
  const { ctx } = context(controller.signal);
  const descendant = "require('node:fs').writeSync(1, 'descendant-ready'); Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0)";
  const script = `require('node:child_process').spawn(process.execPath, ['-e', ${JSON.stringify(descendant)}], {stdio:['ignore','inherit','inherit']})`;
  await runScope({ phase: "execute", root }, async () => {
    await assert.rejects(
      runCommand(ctx, [process.execPath, "-e", script], {
        stdout() {
          controller.abort(new Error("stop descendant"));
        },
      }),
      (error) => {
        assert.ok(error instanceof ExecError);
        assert.equal(error.terminated, "cancellation");
        assert.equal(error.stdout, "descendant-ready");
        return true;
      },
    );
  });
});

test("planning refuses commands before spawning", async () => {
  const { ctx, events } = context();
  await assert.rejects(
    runScope({ phase: "plan" }, () => node(ctx, "").capture()),
    /plan/i,
  );
  assert.deepEqual(events, []);
});

test("tool env excludes engine transport credentials and retains user command env", async (t) => {
  const root = await project(t);
  const { ctx } = context();
  const key = "SPARKWING_NODE_TOKEN";
  const previous = process.env[key];
  process.env[key] = "stale-parent-token";
  t.after(() => {
    if (previous === undefined) delete process.env[key];
    else process.env[key] = previous;
  });
  await runScope({ phase: "execute", root }, async () => {
    const value = await node(ctx, "require('node:fs').writeSync(1, JSON.stringify(process.env))", {
      env: {
        SPARKWING_PROTOCOL_HANDLES: "3,4",
        SPARKWING_API_TOKEN: "forbidden",
        SPARKWING_RUN_ID: "run-1",
        SPARKWING_CUSTOM_TOKEN: "user-choice",
        TOOL_TOKEN: "explicit-tool-secret",
      },
    }).json<Record<string, string>>();
    assert.equal(value.SPARKWING_NODE_TOKEN, undefined);
    assert.equal(value.SPARKWING_PROTOCOL_HANDLES, undefined);
    assert.equal(value.SPARKWING_API_TOKEN, undefined);
    assert.equal(value.SPARKWING_RUN_ID, "run-1");
    assert.equal(value.SPARKWING_CUSTOM_TOKEN, "user-choice");
    assert.equal(value.TOOL_TOKEN, "explicit-tool-secret");
    assert.equal(value.BODY_VALUE, "body");
  });
});

test("commands do not inherit either protocol descriptor", async (t) => {
  const root = await project(t);
  const marker = join(root, "protocol");
  const descriptor = openSync(marker, "w+");
  t.after(() => closeSync(descriptor));
  const child = `
    const fs = require('node:fs');
    const marker = fs.statSync(process.argv[1]);
    const inherited = [3, 4].some(fd => {
      try { const value = fs.fstatSync(fd); return marker.dev === value.dev && marker.ino === value.ino; }
      catch { return false; }
    });
    require('node:fs').writeSync(1, JSON.stringify({ inherited }));
  `;
  const parent = `
    import fs from 'node:fs';
    import { createCommand } from ${JSON.stringify(new URL("../src/command.ts", import.meta.url).href)};
    const ctx = { signal: new AbortController().signal, log: { info() {} } };
    const result = await createCommand(ctx, [process.execPath, '-e', ${JSON.stringify(child)}, ${JSON.stringify(marker)}], { cwd: ${JSON.stringify(root)} }).json();
    fs.writeSync(1, JSON.stringify(result));
  `;
  const childProcess = spawn(process.execPath, ["--input-type=module", "-e", parent], {
    stdio: ["ignore", "pipe", "pipe", descriptor, descriptor],
  });
  let stdout = "",
    stderr = "";
  childProcess.stdout?.on("data", (data) => {
    stdout += data.toString();
  });
  childProcess.stderr?.on("data", (data) => {
    stderr += data.toString();
  });
  const status = await new Promise<number | null>((resolve, reject) => {
    childProcess.once("error", reject);
    childProcess.once("close", resolve);
  });
  assert.equal(status, 0, stderr);
  assert.deepEqual(JSON.parse(stdout), { inherited: false });
});
