import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

const example = fileURLToPath(new URL("../examples/hello/pipeline.ts", import.meta.url));

function runExample(args: string[], stdin: string, env: Record<string, string> = {}): Promise<{ code: number | null; stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [example, ...args], { env: { ...process.env, ...env }, stdio: ["pipe", "pipe", "pipe"] });
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
  const doc = JSON.parse(stdout) as { protocol: number; pipelines: Array<{ name: string }> };
  assert.equal(doc.protocol, 1);
  assert.deepEqual(doc.pipelines.map((p) => p.name), ["hello"]);
});

test("the example serves plan and both nodes as a separate process", async () => {
  const server = createServer((req, res) => {
    if (req.url === "/node/v1/outputs/build" && req.headers.authorization === "Bearer tok") {
      res.writeHead(200).end(JSON.stringify({ digest: "sha-hello" }));
      return;
    }
    res.writeHead(404).end();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  try {
    const run = { pipeline: "hello", run_id: "r1", args: { target: "prod" } };
    const requests = [
      { id: 1, method: "plan", params: run },
      { id: 2, method: "run_node", params: { ...run, node: "build" } },
      { id: 3, method: "run_node", params: { ...run, node: "publish" } },
      { id: 4, method: "shutdown" },
    ].map((r) => JSON.stringify(r)).join("\n") + "\n";
    const { code, stdout, stderr } = await runExample(["serve"], requests, {
      SPARKWING_CONTROLLER_URL: url,
      SPARKWING_AGENT_TOKEN: "tok",
    });
    assert.equal(code, 0, stderr);
    const lines = stdout.trim().split("\n").map((l) => JSON.parse(l) as Record<string, unknown>);
    const byId = new Map(lines.filter((l) => typeof l["id"] === "number").map((l) => [l["id"], l["result"]]));
    const plan = byId.get(1) as { nodes: Array<{ id: string; needs: string[]; retry?: unknown; timeout_ms?: number }> };
    assert.deepEqual(plan.nodes.find((n) => n.id === "publish"), {
      id: "publish",
      needs: ["build"],
      retry: { attempts: 2, backoff_ms: 1000 },
      timeout_ms: 60000,
    });
    assert.deepEqual(byId.get(2), { outcome: "success", output: { digest: "sha-hello" } });
    assert.deepEqual(byId.get(3), { outcome: "success" });
    assert.ok(lines.some((l) => l["node"] === "publish" && l["msg"] === "published sha-hello to prod"), stdout);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test("an unknown verb is a usage error", async () => {
  const { code, stderr } = await runExample(["run"], "");
  assert.equal(code, 2);
  assert.match(stderr, /usage/);
});
