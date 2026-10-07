import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { grantFor } from "./helpers.ts";

const example = fileURLToPath(new URL("../examples/hello/pipeline.ts", import.meta.url));

const lingering = fileURLToPath(new URL("./fixtures/lingering.ts", import.meta.url));

function runExample(args: string[], stdin: string, env: Record<string, string> = {}, file = example): Promise<{ code: number | null; stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [file, ...args], { env: { ...process.env, ...env }, stdio: ["pipe", "pipe", "pipe"] });
    const guard = setTimeout(() => child.kill("SIGKILL"), 15_000);
    child.on("close", () => clearTimeout(guard));
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("error", reject);
    child.on("close", (code) => resolve({ code, stdout, stderr }));
    child.stdin.end(stdin);
  });
}

test("the example prints its describe document", async () => {
  const { code, stdout } = await runExample(["--describe"], "");
  assert.equal(code, 0);
  const doc = JSON.parse(stdout) as { protocol: string; pipelines: Array<{ name: string }> };
  assert.equal(doc.protocol, "1");
  assert.deepEqual(doc.pipelines.map((p) => p.name), ["hello"]);
});

test("one example process per node attempt plans, runs its node and exits at end of stdin", async () => {
  const server = createServer((req, res) => {
    const blob = JSON.stringify({ digest: "sha-hello" });
    if (req.url === "/node/v1/nodes/build/output" && req.headers.authorization === "Bearer tok") {
      res.writeHead(200).end(JSON.stringify({ ...grantFor(blob), url: "/blob/build" }));
      return;
    }
    if (req.url === "/blob/build") {
      res.writeHead(200).end(blob);
      return;
    }
    res.writeHead(404).end();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const env = { SPARKWING_CONTROLLER_URL: `http://127.0.0.1:${(server.address() as AddressInfo).port}`, SPARKWING_AGENT_TOKEN: "tok" };
  const plan = { id: "p", op: "plan", pipeline: "hello", args: { target: "prod" }, run: { run_id: "r1", pipeline: "hello" } };
  const attempt = async (node: string) => {
    const stdin = [plan, { id: "n", op: "run_node", node, attempt: 1, options: { dry_run: false } }].map((r) => JSON.stringify(r)).join("\n") + "\n";
    const { code, stdout, stderr } = await runExample(["--sw-node-protocol"], stdin, env);
    assert.equal(code, 0, stderr);
    return stdout.trim().split("\n").map((l) => JSON.parse(l) as Record<string, unknown>);
  };
  try {
    const build = await attempt("build");
    const planDoc = build.find((l) => l["reply"] === "p")?.["result"] as { nodes: unknown[] };
    assert.deepEqual(planDoc.nodes[1], {
      id: "publish",
      deps: ["build"],
      modifiers: { retry: 2, retry_backoff_ms: 1000, timeout_ms: 60000 },
    });
    assert.deepEqual(build.find((l) => l["reply"] === "n")?.["result"], { outcome: "success", output: { digest: "sha-hello" } });

    const publish = await attempt("publish");
    assert.deepEqual(publish.find((l) => l["reply"] === "n")?.["result"], { outcome: "success" });
    assert.ok(publish.some((l) => l["node"] === "publish" && l["msg"] === "published sha-hello to prod"), JSON.stringify(publish));
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test("an unknown argument is a usage error", async () => {
  const { code, stderr } = await runExample(["serve"], "");
  assert.equal(code, 2);
  assert.match(stderr, /usage/);
});

test("the node process exits at end of stdin even when a body left an interval running", async () => {
  const stdin = [
    { id: "p", op: "plan", pipeline: "lingering", run: { run_id: "r1", pipeline: "lingering" } },
    { id: "n", op: "run_node", node: "poll", attempt: 1 },
  ].map((r) => JSON.stringify(r)).join("\n") + "\n";
  const { code, stdout, stderr } = await runExample(["--sw-node-protocol"], stdin, {}, lingering);
  assert.equal(code, 0, `killed by the guard or failed: ${stderr}`);
  const reply = stdout.trim().split("\n").map((l) => JSON.parse(l) as Record<string, unknown>).find((l) => l["reply"] === "n");
  assert.deepEqual(reply?.["result"], { outcome: "success", output: { started: true } });
});
