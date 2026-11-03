import assert from "node:assert/strict";
import { after, test } from "node:test";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { generateKeyPairSync, sign } from "node:crypto";
import { access, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import "./main.ts";
import { Host, hello } from "../../../sdk/typescript/test/helpers.ts";
import { SDKError } from "../../../sdk/typescript/src/errors.ts";

const root = await mkdtemp(join(tmpdir(), "sparkwing-ts-example-"));
after(() => rm(root, { recursive: true, force: true }));

const outputs = new Map();
const unexpected = async () => { throw new Error("unexpected host service"); };
function services(overrides = {}) {
  return {
    secret: unexpected, output: async (job) => outputs.get(job), latest: unexpected,
    annotate: unexpected, summary: unexpected, mask: unexpected, oidcToken: unexpected,
    runPipeline: unexpected, cleanup: unexpected, ...overrides,
  };
}
async function session(job, overrides = {}, args = {}, pipeline = "example-ts", runId = "ts-test") {
  const host = new Host({ services: () => services(overrides), root });
  assert.equal((await host.send({ ...hello, binding: { run_id: runId, job_id: job, routes: { url: "http://127.0.0.1:12345", bearer: "test" } } })).ok, true);
  const planned = await host.send({ id: "plan", op: "plan", pipeline, args, run: { run_id: runId, pipeline, git: { sha: "sealed-sha", branch: "test", repo: "local:test" }, dry_run: false, no_memo: false } });
  assert.equal(planned.ok, true, JSON.stringify(planned));
  return { host, jobs: planned.result.jobs };
}
async function step(host, job, name = "run", settled = []) {
  const reply = await host.send({ id: `step-${name}`, op: "run_step", job, step: name, attempt: 1, settled });
  assert.equal(reply.ok, true, JSON.stringify(reply));
  assert.equal(reply.result.outcome, "success", JSON.stringify(reply));
  return reply.result.output;
}

test("matrix outputs survive the SDK output route and match all four cells", async () => {
  for (const platform of ["linux", "darwin"]) for (const version of [22, 24]) {
    const name = `matrix-${platform}-${version}`;
    const { host } = await session(name);
    try { outputs.set(name, await step(host, name)); } finally { await host.close(); }
  }
  const { host } = await session("matrix-assert");
  try { await step(host, "matrix-assert"); } finally { await host.close(); }
});

test("typed job output and terminal predecessor outputs reach the consumer", async () => {
  const producer = await session("outputs-produce");
  try { outputs.set("outputs-produce", await step(producer.host, "outputs-produce")); } finally { await producer.host.close(); }
  const consumer = await session("outputs-assert");
  try { await step(consumer.host, "outputs-assert"); } finally { await consumer.host.close(); }
  const { host } = await session("predecessor-outputs-assert");
  try {
    await step(host, "predecessor-outputs-assert", "first");
    await step(host, "predecessor-outputs-assert", "second", ["first"]);
    await step(host, "predecessor-outputs-assert", "assert", ["first", "second"]);
  } finally { await host.close(); }
});

test("generated-job replay reconstructs its child start and checks the child output", async () => {
  let child;
  const { host, jobs } = await session("dynamic-boise", { runPipeline: async (pipeline, job, options) => {
    assert.equal(pipeline, "weather-report");
    assert.equal(job, "stamp");
    child = options.inputs.city;
    return { forecast: "sunny", city: child };
  } });
  try {
    const source = jobs.find((job) => job.id === "dynamic-discover");
    const replay = await host.send({ id: "generate", op: "eval", closure: source.closures.generate[0], input: { cities: ["Boise", "Portland"] } });
    assert.equal(replay.ok, true, JSON.stringify(replay));
    assert.deepEqual(replay.result.value.map((job) => job.id), ["dynamic-boise", "dynamic-portland"]);
    await step(host, "dynamic-boise");
    assert.equal(child, "Boise");
  } finally { await host.close(); }
});

test("missing optional secret and absent OIDC skip through their named predicates", async () => {
  for (const [job, overrides, args] of [
    ["scoped-secret-assert", { secret: async () => { throw new SDKError("missing", { code: "not_found" }); } }, { "secret-name": "EXAMPLE_GATE_SECRET" }],
    ["oidc-assert", { oidcToken: async () => { throw new SDKError("not configured", { code: "not_found" }); } }, {}],
  ]) {
    const { host, jobs } = await session(job, overrides, args);
    try {
      const spec = jobs.find((candidate) => candidate.id === job);
      const reply = await host.send({ id: "skip", op: "eval", closure: spec.closures.skip_if[0] });
      assert.equal(reply.result.value, true, JSON.stringify(reply));
      assert.ok(host.frames.some((frame) => JSON.stringify(frame).includes("skipped:")));
    } finally { await host.close(); }
  }
});

test("OIDC assertion checks signed claims and rejects a signature from another key", async () => {
  const keys = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const other = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const header = Buffer.from(JSON.stringify({ alg: "RS256", kid: "test-key" })).toString("base64url");
  const claims = Buffer.from(JSON.stringify({ aud: "sparkwing-example-gate", iss: "https://issuer.test", sub: "team:test:pipeline:example-ts:trigger:manual", pipeline: "example-ts", run_id: "ts-test", iat: Date.now() / 1000 - 1, nbf: Date.now() / 1000 - 1, exp: Date.now() / 1000 + 60 })).toString("base64url");
  const input = `${header}.${claims}`;
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => Response.json({ keys: [{ kid: "test-key", ...keys.publicKey.export({ format: "jwk" }) }] });
  try {
    for (const [privateKey, succeeds] of [[keys.privateKey, true], [other.privateKey, false]]) {
      const token = `${input}.${sign("RSA-SHA256", Buffer.from(input), privateKey).toString("base64url")}`;
      const { host } = await session("oidc-assert", { oidcToken: async () => token });
      try {
        const reply = await host.send({ id: "check", op: "run_step", job: "oidc-assert", step: "run", attempt: 1, settled: [] });
        assert.equal(reply.result.outcome, succeeds ? "success" : "failed", JSON.stringify(reply));
      } finally { await host.close(); }
    }
  } finally { globalThis.fetch = originalFetch; }
});

test("cache reset removes stale local bytes before the cache-bearing seed", async () => {
  const cache = `${root}/.sparkwing/cache/example-ts-deps`;
  await mkdir(cache, { recursive: true });
  await writeFile(`${cache}/stale`, "old-home-data");
  const { host, jobs } = await session("dependency-cache-reset");
  try {
    assert.ok(jobs.find((job) => job.id === "dependency-cache-seed").deps.includes("dependency-cache-reset"));
    await step(host, "dependency-cache-reset");
    await assert.rejects(access(cache), { code: "ENOENT" });
  } finally { await host.close(); }
});

test("artifact assertion rejects stale bytes and accepts this run's producer bytes", async () => {
  const cacheRoot = `${root}/.sparkwing/cache`;
  const relative = ".sparkwing/cache/example-ts-artifact.txt";
  const destination = `${cacheRoot}/example-ts-consumed/${relative}`;
  const producer = await session("artifacts-produce");
  try { await step(producer.host, "artifacts-produce"); } finally { await producer.host.close(); }
  assert.equal(await readFile(`${cacheRoot}/example-ts-artifact.txt`, "utf8"), "ts-test");
  await mkdir(`${cacheRoot}/example-ts-consumed/.sparkwing/cache`, { recursive: true });
  await writeFile(destination, "previous-run");
  const stale = await session("artifacts-assert");
  try {
    const reply = await stale.host.send({ id: "stale", op: "run_step", job: "artifacts-assert", step: "run", attempt: 1, settled: [] });
    assert.equal(reply.result.outcome, "failed", JSON.stringify(reply));
  } finally { await stale.host.close(); }
  await copyFile(`${cacheRoot}/example-ts-artifact.txt`, destination);
  const current = await session("artifacts-assert");
  try { await step(current.host, "artifacts-assert"); } finally { await current.host.close(); }
});

test("native memo child publishes its run ID under the supplied token key", async () => {
  for (const run of ["memo-first", "memo-second"]) {
    const { host, jobs } = await session("memo", {}, { token: "parent-token" }, "memo-probe", run);
    try {
      assert.deepEqual(jobs[0].memo.input.parts, ["typescript-native-memo", "parent-token"]);
      assert.deepEqual(await step(host, "memo"), { run });
    } finally { await host.close(); }
  }
});

test("native memo assertion calls the declared child twice and rejects a rerun body", async () => {
  for (const reuses of [true, false]) {
    let calls = 0;
    const { host, jobs } = await session("native-memo-reuse-assert", { runPipeline: async (pipeline, job, options) => {
      assert.equal(pipeline, "memo-probe");
      assert.equal(job, "memo");
      assert.equal(options.inputs.token, "ts-test-ts-native-memo");
      calls++;
      return { run: reuses ? "memo-first" : `memo-${calls}` };
    } });
    try {
      assert.deepEqual(jobs.find((job) => job.id === "native-memo-reuse-assert").children, [{ pipeline: "memo-probe" }]);
      const reply = await host.send({ id: "memo-assert", op: "run_step", job: "native-memo-reuse-assert", step: "run", attempt: 1, settled: [] });
      assert.equal(reply.result.outcome, reuses ? "success" : "failed", JSON.stringify(reply));
      assert.equal(calls, 2);
    } finally { await host.close(); }
  }
});
