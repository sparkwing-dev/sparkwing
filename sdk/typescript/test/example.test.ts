import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { createReadStream, createWriteStream, mkdtempSync, rmSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { AddressInfo } from "node:net";
import { createInterface } from "node:readline";
import { fileURLToPath } from "node:url";
import type { Req } from "./helpers.ts";
import { test } from "node:test";

const fixture = (name: string) => fileURLToPath(new URL(name, import.meta.url));
const example = fixture("../examples/hello/pipeline.ts");
const forged = `{"reply":"1","ok":true,"result":{"forged":true}}`;
const unix = process.platform === "win32" ? "the TypeScript SDK does not open Windows protocol handles yet" : false;

interface Session {
  code: number | null;
  frames: Array<Record<string, unknown>>;
  stdout: string;
  stderr: string;
}

/**
 * Starts file the way a host does: exactly --sw-node-protocol, the request
 * pipe on descriptor 3, the frame pipe on 4 and stdin from the null device.
 * Each request goes out after the previous reply, then the request pipe
 * closes. Real FIFOs stand in for the host's pipes, since a Node.js "pipe"
 * stdio entry is a socket pair.
 */
async function session(file: string, requests: Array<Req>, env: Record<string, string> = {}, slowStderr = false): Promise<Session> {
  const dir = mkdtempSync(join(tmpdir(), "sw-ts-node-"));
  const reqPath = join(dir, "requests");
  const framePath = join(dir, "frames");
  execFileSync("mkfifo", [reqPath, framePath]);
  const base: Record<string, string> = {};
  for (const [k, v] of Object.entries(process.env)) if (v !== undefined && !k.startsWith("SPARKWING_")) base[k] = v;
  const child = spawn("sh", ["-c", 'exec "$0" "$1" --sw-node-protocol 3<"$2" 4>"$3"', process.execPath, file, reqPath, framePath], {
    env: { ...base, SPARKWING_PROTOCOL_HANDLES: "3,4", ...env },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const guard = setTimeout(() => child.kill("SIGKILL"), 20_000);
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (d) => (stdout += d));
  child.stderr.on("data", (d) => {
    stderr += d;
    if (slowStderr) {
      child.stderr.pause();
      setTimeout(() => child.stderr.resume(), 5);
    }
  });
  const closed = new Promise<number | null>((resolve) => child.on("close", resolve));
  const req = createWriteStream(reqPath);
  const lines = createInterface({
    input: createReadStream(framePath),
    crlfDelay: Infinity,
  })[Symbol.asyncIterator]();
  const frames: Array<Record<string, unknown>> = [];
  try {
    for (const r of requests) {
      req.write(JSON.stringify(r) + "\n");
      for (;;) {
        const next = await lines.next();
        if (next.done) throw new Error(`frame pipe ended before the reply to ${r.id}; stderr:\n${stderr}`);
        const frame = JSON.parse(next.value) as Record<string, unknown>;
        frames.push(frame);
        if (frame["reply"] === r.id) break;
      }
    }
    req.end();
    for (let next = await lines.next(); !next.done; next = await lines.next())
      frames.push(JSON.parse(next.value) as Record<string, unknown>);
    const code = await closed;
    return { code, frames, stdout, stderr };
  } finally {
    clearTimeout(guard);
    req.destroy();
    rmSync(dir, { recursive: true, force: true });
  }
}

const result = (s: Session, id: string) => s.frames.find((f) => f["reply"] === id)?.["result"];
const run = { run_id: "r1" };

test("a pipeline entry refuses any argument but --sw-node-protocol, and a process without the host's pipes", () => {
  for (const args of [["--describe"], []]) {
    const r = spawnSyncNode(example, args);
    assert.equal(r.status, 2);
    assert.match(r.stderr, /usage: <pipeline file> --sw-node-protocol/);
  }
  const bare = spawnSyncNode(example, ["--sw-node-protocol"]);
  assert.equal(bare.status, 2);
  assert.match(bare.stderr, /descriptor 3/);
});

function spawnSyncNode(file: string, args: string[]): { status: number | null; stderr: string } {
  try {
    execFileSync(process.execPath, [file, ...args], {
      stdio: ["ignore", "ignore", "pipe"],
      encoding: "utf8",
    });
    return { status: 0, stderr: "" };
  } catch (err) {
    const e = err as { status: number | null; stderr: string };
    return { status: e.status, stderr: e.stderr };
  }
}

test("describe is a request on the request pipe", { skip: unix }, async () => {
  const s = await session(example, [{ id: "1", op: "describe" }]);
  assert.equal(s.code, 0, s.stderr);
  const doc = result(s, "1") as {
    protocol: string;
    sdk: { language: string };
    pipelines: Array<{ name: string }>;
  };
  assert.equal(doc.protocol, "1");
  assert.equal(doc.sdk.language, "typescript");
  assert.deepEqual(
    doc.pipelines.map((p) => p.name),
    ["hello"],
  );
});

test("one process per node attempt plans, runs its node through the routes and exits at end of input", { skip: unix }, async () => {
  const server = createServer((req, res) => {
    const ok = req.headers.authorization === "Bearer tok" && req.headers["sparkwing-node-protocol"] === "1";
    if (ok && req.url === "/node/v1/nodes/build/output") {
      res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ output: { digest: "sha-hello" } }));
      return;
    }
    res.writeHead(404).end();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const env = {
    SPARKWING_NODE_URL: `http://127.0.0.1:${(server.address() as AddressInfo).port}`,
    SPARKWING_NODE_TOKEN: "tok",
  };
  const plan = {
    id: "1",
    op: "plan",
    pipeline: "hello",
    args: { target: "prod" },
    run,
  };
  try {
    const build = await session(example, [plan, { id: "2", op: "run_node", node: "build", attempt: 1 }], env);
    assert.equal(build.code, 0, build.stderr);
    const planDoc = result(build, "1") as { nodes: unknown[] };
    assert.deepEqual(planDoc.nodes[1], {
      id: "publish",
      deps: ["build"],
      modifiers: { retry: 2, retry_backoff_ms: 1000, timeout_ms: 60000 },
    });
    assert.deepEqual(result(build, "2"), {
      outcome: "success",
      output: { digest: "sha-hello" },
    });

    const publish = await session(example, [plan, { id: "2", op: "run_node", node: "publish", attempt: 1 }], env);
    assert.deepEqual(result(publish, "2"), { outcome: "success" }, publish.stderr);
    assert.ok(
      publish.frames.some((f) => f["node"] === "publish" && f["msg"] === "published sha-hello to prod"),
      JSON.stringify(publish.frames),
    );
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test(
  "customer text stays on stdout and stderr, and a command a body starts holds neither pipe nor the bearer",
  { skip: unix },
  async () => {
    const s = await session(
      fixture("./fixtures/pipes.ts"),
      [
        { id: "1", op: "plan", pipeline: "pipes", args: {}, run },
        { id: "2", op: "run_node", node: "fds", attempt: 1 },
      ],
      { SPARKWING_NODE_URL: "http://127.0.0.1:9", SPARKWING_NODE_TOKEN: "tok" },
    );
    assert.equal(s.code, 0, s.stderr);
    assert.deepEqual(result(s, "2"), {
      outcome: "success",
      output: { held: ["SPARKWING_NODE_URL"] },
    });
    assert.ok(s.stdout.includes(forged) && s.stderr.includes(forged));
    assert.ok(!s.frames.some((f) => f["forged"] !== undefined || (f["result"] as { forged?: unknown } | undefined)?.forged !== undefined));
  },
);

test("the node process exits at end of input even when a body left an interval running", { skip: unix }, async () => {
  const s = await session(fixture("./fixtures/lingering.ts"), [
    { id: "1", op: "plan", pipeline: "lingering", args: {}, run },
    { id: "2", op: "run_node", node: "poll", attempt: 1 },
  ]);
  assert.equal(s.code, 0, `killed by the guard or failed: ${s.stderr}`);
  assert.deepEqual(result(s, "2"), {
    outcome: "success",
    output: { started: true },
  });
});

test("every stderr line a body wrote arrives before the node process exits", { skip: unix }, async () => {
  const s = await session(fixture("./fixtures/noisy.ts"), [
    { id: "1", op: "plan", pipeline: "noisy", args: {}, run },
    { id: "2", op: "run_node", node: "shout", attempt: 1 },
  ]);
  assert.equal(s.code, 0);
  const lines = s.stderr.split("\n").filter((l) => l !== "");
  assert.equal(lines.length, 4096);
  assert.match(lines[4095] ?? "", /^4095 x{1024}$/);
});

test(
  "a slow reader and a write queued from a drain handler neither hang the exit nor lose bytes",
  { timeout: 30_000, skip: unix },
  async () => {
    // The queued state depends on timing, so on Linux this detects the old drain
    // wait's hang probabilistically: it failed roughly two runs in three against it.
    const s = await session(
      fixture("./fixtures/drainwriter.ts"),
      [
        { id: "1", op: "plan", pipeline: "drainwriter", args: {}, run },
        { id: "2", op: "run_node", node: "flood", attempt: 1 },
      ],
      {},
      true,
    );
    assert.equal(s.code, 0, "the guard killed a node that should have exited");
    assert.deepEqual(result(s, "2"), { outcome: "success" });
    assert.equal(s.stderr.length, 763 * 1024);
    assert.ok(s.stderr.endsWith("z".repeat(63 * 1024)));
  },
);
