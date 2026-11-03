import assert from "node:assert/strict";
import { createPublicKey, verify } from "node:crypto";
import type { JsonWebKey } from "node:crypto";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import {
  definePipeline, dir, docker, ExecError, main, parts, path, SDKError,
} from "../../../sdk/typescript/src/index.ts";
import type { Context, Job, Plan } from "../../../sdk/typescript/index.d.ts";
import type { CleanupRegistration } from "../../../sdk/typescript/index.d.ts";

const goCheckout = fileURLToPath(new URL("../../../", import.meta.url));
const cacheDirectory = ".sparkwing/cache/example-ts-deps";
const artifactFile = ".sparkwing/cache/example-ts-artifact.txt";
const audience = "sparkwing-example-gate";

function skip(ctx: Context, reason: string): true {
  ctx.log.info(`skipped: ${reason}`);
  return true;
}

async function cli(ctx: Context, ...args: string[]): Promise<string> {
  const binary = ctx.args["cli"] || "sparkwing";
  return ctx.exec([binary, "-C", ctx.args["go-checkout"] || goCheckout, ...args]).string();
}

function profile(ctx: Context): string {
  return `--profile=${ctx.args["api-profile"] || ""}`;
}

interface RunDetail {
  id: string;
  status: string;
  jobs: { id: string; status: string; outcome?: string; attempt: number }[];
  retry?: { mode: string; reused: string[] };
}

function lastDetail(text: string): RunDetail {
  const records = text.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line) as RunDetail);
  const detail = records.findLast((record) => typeof record.id === "string" && typeof record.status === "string");
  assert.ok(detail, "status API returned no run detail");
  return { ...detail, jobs: detail.jobs || [] };
}

async function status(ctx: Context, id: string, follow = false, targetProfile = ctx.args["api-profile"] || ""): Promise<RunDetail> {
  let output: string;
  try {
    output = await cli(ctx, "runs", "status", "--run", id, `--profile=${targetProfile}`, "-o", "json", "--exit-zero", ...(follow ? ["--follow"] : []));
  } catch (error) {
    if (!(error instanceof ExecError) || error.exitCode !== 3) throw error;
    output = error.stdout;
  }
  return lastDetail(output);
}

interface ProbeRun { id: string; cleanup: CleanupRegistration }

async function trigger(ctx: Context, token: string, ...args: string[]): Promise<ProbeRun> {
  const command = ctx.args["api-profile"]
    ? ["pipeline", "trigger", "example-probe", profile(ctx), "--detach", "-o", "plain"]
    : ["run", "example-probe", "--sw-detached", "--sw-output", "plain"];
  const id = (await cli(ctx, ...command, "--", "--token", token, "--cli", ctx.args["cli"] || "sparkwing", "--api-profile", ctx.args["api-profile"] || "", ...args)).trim();
  assert.match(id, /^[A-Za-z0-9_-]+$/, "trigger returned no plain run ID");
  return { id, cleanup: await trackRun(ctx, id) };
}

async function trackRun(ctx: Context, id: string, targetProfile = ctx.args["api-profile"] || ""): Promise<CleanupRegistration> {
  return ctx.cleanup({ argv: [ctx.args["cli"] || "sparkwing", "runs", "cancel", "--run", id, `--profile=${targetProfile}`], description: "Cancel TypeScript gate probe" });
}

async function awaitProbe(ctx: Context, run: ProbeRun, targetProfile = ctx.args["api-profile"] || ""): Promise<RunDetail> {
  const detail = await status(ctx, run.id, true, targetProfile);
  if (["success", "failed", "cancelled"].includes(detail.status)) await run.cleanup.unregister();
  return detail;
}

async function verifyOIDC(ctx: Context): Promise<void> {
  const token = await ctx.oidcToken(audience);
  const [header64, claims64, signature64, extra] = token.split(".");
  assert.ok(header64 && claims64 && signature64 && !extra, "OIDC token must be a JWT");
  const header = JSON.parse(Buffer.from(header64, "base64url").toString()) as { alg: string; kid: string };
  const claims = JSON.parse(Buffer.from(claims64, "base64url").toString()) as Record<string, unknown>;
  assert.equal(header.alg, "RS256");
  assert.equal(claims["aud"], audience);
  assert.equal(claims["run_id"], ctx.run.id);
  assert.equal(claims["pipeline"], ctx.run.pipeline);
  assert.ok(typeof claims["sub"] === "string" && claims["sub"].includes(`pipeline:${ctx.run.pipeline}:`));
  const now = Date.now() / 1000;
  assert.ok(typeof claims["exp"] === "number" && claims["exp"] > now, "OIDC expiry must be in the future");
  assert.ok(typeof claims["iat"] === "number" && claims["iat"] <= now + 30, "OIDC issued-at must not be in the future");
  assert.ok(typeof claims["nbf"] === "number" && claims["nbf"] <= now + 30, "OIDC not-before must not be in the future");
  assert.equal(typeof claims["iss"], "string");
  const issuer = new URL(String(claims["iss"]));
  assert.equal(issuer.protocol, "https:", "OIDC issuer must use HTTPS");
  const response = await fetch(new URL("/.well-known/jwks.json", issuer), { signal: ctx.signal, redirect: "error" });
  assert.equal(response.status, 200, "OIDC issuer must publish JWKS");
  const jwks = await response.json() as { keys: ({ kid: string } & JsonWebKey)[] };
  const key = jwks.keys.find((candidate) => candidate.kid === header.kid);
  assert.ok(key, "OIDC signing key must appear in JWKS");
  assert.ok(verify("RSA-SHA256", Buffer.from(`${header64}.${claims64}`), createPublicKey({ key, format: "jwk" }), Buffer.from(signature64, "base64url")), "OIDC signature must verify");
}

function nativeAssertions(plan: Plan): Job<unknown>[] {
  const checks: Job<unknown>[] = [];
  const matrix: Job<{ platform: string; version: number }>[] = [];
  for (const platform of ["linux", "darwin"]) {
    for (const version of [22, 24]) {
      matrix.push(plan.job(`matrix-${platform}-${version}`, () => ({ platform, version })));
    }
  }
  plan.groupJobs("matrix", ...matrix);
  checks.push(plan.job("matrix-assert", async (ctx) => {
    const values = await Promise.all(matrix.map((job) => ctx.output(job)));
    assert.deepEqual(values.map((value) => `${value?.platform}/${value?.version}`).sort(), ["darwin/22", "darwin/24", "linux/22", "linux/24"]);
  }).needs(...matrix));

  const producer = plan.job("outputs-produce", () => ({ text: "typed-output", count: 42 }));
  checks.push(plan.job("outputs-assert", async (ctx) => {
    assert.deepEqual(await ctx.output(producer), { text: "typed-output", count: 42 });
  }).needs(producer));

  const steps = plan.job("predecessor-outputs-assert");
  const first = steps.step("first", () => ({ value: 21 }));
  const second = steps.step("second", (ctx) => {
    assert.equal(ctx.stepOutput(first)?.value, 21);
    return { value: ctx.stepOutput(first)!.value * 2 };
  }).needs(first);
  steps.step("assert", (ctx) => {
    assert.equal(ctx.stepOutput(first)?.value, 21);
    assert.equal(ctx.stepOutput(second)?.value, 42);
  }).needs(second).result();
  checks.push(steps);

  const discovery = plan.job("dynamic-discover", () => ({ cities: ["Boise", "Portland"] }));
  const generator = discovery.generate((generated, output) => {
    for (const city of output.cities) {
      generated.job(`dynamic-${city.toLowerCase()}`, async (ctx) => {
        const weather = await ctx.runPipeline<{ forecast: string; city: string }>("weather-report", "stamp", { inputs: { city }, timeoutMs: 30_000 });
        assert.deepEqual(weather, { forecast: "sunny", city });
      }).startsChildren("weather-report");
    }
  });
  checks.push(plan.job("dynamic-children-assert", async (ctx) => {
    assert.equal((await ctx.output(discovery))?.cities.length, 2);
  }).needsGenerated(generator));

  checks.push(plan.job("scoped-secret-assert", async (ctx) => {
    const value = await ctx.secret(ctx.args["secret-name"]!);
    assert.ok(value, "configured scoped secret must not be empty");
    await ctx.mask(value);
    ctx.log.info("scoped secret resolved and masked");
  }).skipIf(async (ctx) => {
    const name = ctx.args["secret-name"];
    if (!name) return skip(ctx, "--secret-name was not configured");
    if (await ctx.secret(name) === undefined) return skip(ctx, `${name} is not configured for this run's scope`);
    return false;
  }));

  checks.push(plan.job("oidc-assert", verifyOIDC).skipIf(async (ctx) => {
    try {
      await ctx.oidcToken(audience);
      return false;
    } catch (error) {
      if (error instanceof SDKError && error.code === "not_found") return skip(ctx, "this run has no OIDC issuer");
      throw error;
    }
  }));

  const cache = dir(cacheDirectory, ".sparkwing/package.json");
  const reset = plan.job("dependency-cache-reset", () => rm(path(cacheDirectory), { recursive: true, force: true }));
  const seed = plan.job("dependency-cache-seed", async () => {
    await mkdir(path(cacheDirectory), { recursive: true });
    await writeFile(path(cacheDirectory, "value"), "typescript-cache-roundtrip");
  }).needs(reset).cache(cache);
  const clear = plan.job("dependency-cache-clear", () => rm(path(cacheDirectory), { recursive: true, force: true })).needs(seed);
  checks.push(plan.job("dependency-cache-assert", async () => {
    assert.equal(await readFile(path(cacheDirectory, "value"), "utf8"), "typescript-cache-roundtrip");
  }).needs(clear).cache(cache));

  checks.push(plan.job("native-memo-reuse-assert", async (ctx) => {
    const options = { inputs: { token: `${ctx.run.id}-ts-native-memo` }, timeoutMs: 30_000 };
    const first = await ctx.runPipeline<{ run: string }>("memo-probe", "memo", options);
    const second = await ctx.runPipeline<{ run: string }>("memo-probe", "memo", options);
    assert.ok(first.run, "memo seed must return its child run ID");
    assert.equal(second.run, first.run, "second TypeScript child must reuse the first child's memo output");
  }).startsChildren("memo-probe").timeout(90_000));

  const artifacts = plan.job("artifacts-produce", async (ctx) => {
    await mkdir(path(".sparkwing/cache"), { recursive: true });
    await writeFile(path(artifactFile), ctx.run.id);
  }).artifacts(artifactFile);
  checks.push(plan.job("artifacts-assert", async (ctx) => {
    assert.equal(await readFile(path(".sparkwing/cache/example-ts-consumed", artifactFile), "utf8"), ctx.run.id);
  }).consumes(artifacts, { into: ".sparkwing/cache/example-ts-consumed" }));

  checks.push(plan.job("docker-assert", async (ctx) => {
    let output = "";
    await docker.run(ctx, { image: "alpine:3.21", cmd: ["printf", "typescript-docker-ok"], stdout: (bytes) => { output += Buffer.from(bytes).toString(); } });
    assert.equal(output, "typescript-docker-ok");
  }).skipIf((ctx) => ctx.runner.type === "cloud" || ctx.runner.labels.includes("docker") || ctx.runner.labels.includes("docker=true") ? false : skip(ctx, "runner is neither Cloud nor labeled docker")));

  const skipped = plan.job("skip-if", () => { throw new Error("skip-if executed a skipped body"); }).skipIf(() => true);
  const optional = plan.job("optional-failure", () => { throw new Error("expected optional failure"); }).optional();
  const collected = plan.job("collect-all").failurePolicy("collect_all").optional();
  const failed = collected.step("expected-failure", () => { throw new Error("expected collected failure"); });
  collected.step("independent-result", () => 42).result();
  collected.step("finally", (ctx) => { assert.equal(ctx.stepOutput(failed), undefined); }).finally();
  const approval = plan.approval("approval", { timeoutMs: 1000, onTimeout: "approve" });
  checks.push(plan.job("approval-assert", () => true).needs(approval));
  checks.push(plan.job("hooks-backoff-timeout-assert", (ctx) => {
    assert.equal(process.env["EXAMPLE_TS_HOOK"], "before");
    ctx.log.info("body ran after beforeRun");
    return "hook-output";
  }).beforeRun(() => { process.env["EXAMPLE_TS_HOOK"] = "before"; }).verify(() => {
    assert.equal(process.env["EXAMPLE_TS_HOOK"], "before");
  }).afterRun((_ctx, result) => { assert.equal(result.outcome, "success"); }).retry(2, { backoffMs: 10 }).timeout(10_000));
  checks.push(skipped, optional, collected);
  return checks;
}

function lifecycleAssertions(plan: Plan): Job<unknown>[] {
  const checks: Job<unknown>[] = [];
  checks.push(plan.job("logs-history-api-assert", async (ctx) => {
    const run = await trigger(ctx, `${ctx.run.id}-ts-logs`);
    const detail = await awaitProbe(ctx, run);
    assert.equal(detail.id, run.id);
    assert.equal(detail.status, "success");
    const logs = await cli(ctx, "runs", "logs", "--run", run.id, profile(ctx), "-o", "json", "--follow");
    assert.ok(logs.includes("example-probe-output-ok"), "API logs must include the probe source marker");
    const history = await cli(ctx, "runs", "list", "--pipeline", "example-probe", profile(ctx), "-o", "plain", "--limit", "100");
    assert.ok(history.split(/\s+/).includes(run.id), "completed probe must appear in run history");
  }).timeout(90_000));
  checks.push(plan.job("failed-only-retry-assert", async (ctx) => {
    const original = await trigger(ctx, `${ctx.run.id}-ts-retry`, "--fail-once");
    assert.equal((await awaitProbe(ctx, original)).status, "failed");
    const id = (await cli(ctx, "runs", "retry", "--run", original.id, "--failed", profile(ctx), "-o", "plain")).trim();
    const detail = await awaitProbe(ctx, { id, cleanup: await trackRun(ctx, id) });
    assert.equal(detail.status, "success");
    assert.equal(detail.retry?.mode, "failed");
    for (const name of ["source", "memo"]) assert.ok(detail.retry?.reused.includes(name), `failed-only retry must reuse ${name} output`);
  }).skipIf((ctx) => ctx.args["fail-once"] === "true" ? false : skip(ctx, "--fail-once was not requested")).timeout(90_000));
  checks.push(plan.job("cancellation-assert", async (ctx) => {
    const run = await trigger(ctx, `${ctx.run.id}-ts-cancel`, "--long");
    for (;;) {
      const detail = await status(ctx, run.id);
      if (detail.jobs.some((job) => job.id === "long" && job.status === "running")) break;
      assert.ok(!["failed", "success", "cancelled"].includes(detail.status), "long probe ended before cancellation");
      await delay(100, undefined, { signal: ctx.signal });
    }
    await cli(ctx, "runs", "cancel", "--run", run.id, profile(ctx), "-o", "plain");
    const detail = await awaitProbe(ctx, run);
    assert.equal(detail.status, "cancelled");
    assert.ok(detail.jobs.some((job) => job.id === "long" && job.outcome === "cancelled"), "long job must report cancellation");
  }).timeout(90_000));
  return checks;
}

definePipeline({
  name: "example-ts",
  short: "Assert staging integration features through the TypeScript SDK",
  help: "Independent assertions cover matrix jobs, typed outputs, predecessor outputs, generated child pipelines, optional scoped secrets, OIDC claims and signatures, dependency caches, memoized jobs, artifacts, Docker, lifecycle API operations and the SDK tour. Secrets, OIDC and Docker explain why they skip when unavailable. The CLI lifecycle checks use the Go example-probe in --go-checkout because a project selects one SDK language. Local checks need no Cloud or Docker and run in parallel.",
  examples: [
    { command: "sparkwing -C examples/staging-typescript run example-ts", comment: "Run local TypeScript integration assertions" },
    { command: "sparkwing -C examples/staging-typescript run example-ts -- --fail-once --secret-name EXAMPLE_GATE_SECRET", comment: "Also prove failed-only retry and read the optional scoped secret" },
  ],
  args: [
    { name: "cli", type: "string", default: "sparkwing", description: "Sparkwing CLI matching this checkout" },
    { name: "go-checkout", type: "string", default: goCheckout, description: "Go repository checkout containing example-probe" },
    { name: "api-profile", type: "string", description: "Controller profile for lifecycle probes, empty uses local" },
    { name: "secret-name", type: "string", default: "EXAMPLE_GATE_SECRET", enum: ["EXAMPLE_GATE_SECRET"], description: "Optional declared secret to resolve in this run's scope" },
    { name: "fail-once", type: "bool", description: "Exercise a failed run and failed-only retry" },
    { name: "smoke-profile", type: "string", description: "Target environment profile for the separate reference run" },
    { name: "smoke-checkout", type: "string", description: "Checkout of sparkwing-ci-smoke used by the trigger CLI" },
    { name: "smoke-pipeline", type: "string", default: "reference", description: "Reference pipeline containing hello and docker-test" },
  ],
  secrets: [{ name: "EXAMPLE_GATE_SECRET", optional: true }],
  plan(plan) {
    const checks = [...nativeAssertions(plan), ...lifecycleAssertions(plan)];
    const summary = plan.job("summary", async (ctx) => {
      await ctx.summary("TypeScript staging integration assertions passed. Unavailable secrets, OIDC and Docker report skip reasons in their jobs.");
    }).needs(...checks);
    plan.job("reference-pipeline-assert", async (ctx) => {
      const checkout = ctx.args["smoke-checkout"];
      assert.ok(checkout, "--smoke-checkout is required with --smoke-profile");
      const id = (await ctx.exec([ctx.args["cli"] || "sparkwing", "-C", checkout, "pipeline", "trigger", ctx.args["smoke-pipeline"] || "reference", "--profile", ctx.args["smoke-profile"]!, "--detach", "-o", "plain"]).string()).trim();
      assert.match(id, /^[A-Za-z0-9_-]+$/, "reference trigger returned no plain run ID");
      const detail = await awaitProbe(ctx, { id, cleanup: await trackRun(ctx, id, ctx.args["smoke-profile"]) }, ctx.args["smoke-profile"]);
      assert.equal(detail.status, "success");
      for (const name of ["hello", "docker-test"]) {
        assert.ok(detail.jobs.some((job) => job.id === name && job.outcome === "success"), `reference job ${name} must succeed`);
      }
    }).needs(summary).skipIf((ctx) => ctx.args["smoke-profile"] ? false : skip(ctx, "--smoke-profile was not configured for the separate reference run"));
  },
});

definePipeline({
  name: "memo-probe",
  short: "Return the producing child run ID so the TypeScript gate can assert memo reuse",
  args: [{ name: "token", type: "string", required: true }],
  plan(plan, run) {
    plan.job("memo", (ctx) => ({ run: ctx.run.id })).memo(parts("typescript-native-memo", run.args["token"] || ""));
  },
});

definePipeline({
  name: "weather-report",
  short: "Return a deterministic child output for the TypeScript integration gate",
  args: [{ name: "city", type: "string", default: "Boise" }],
  plan(plan) {
    plan.job("stamp", (ctx) => ({ forecast: "sunny", city: ctx.args["city"] || "Boise" })).inline();
  },
});

if (process.argv.includes("--sw-node-protocol")) await main();
