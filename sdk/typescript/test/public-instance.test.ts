import assert from "node:assert/strict";
import test from "node:test";
import { resolve } from "node:path";
import { ConcurrencyGroup, Git, Plan, definePipeline, docker, refToLastRun } from "../src/index.ts";
import type { Approval } from "../index.d.ts";
import { Generated, resetPipelines } from "../src/plan.ts";
import { describe } from "../src/describe.ts";
import { makeContext } from "../src/context.ts";
import { registerHelpers } from "../src/helpers/host.ts";
import { NodeRoutes, type HostServices } from "../src/routes.ts";
import type { HostExecRequest } from "../src/command.ts";
import { fakeContext } from "./helper-context.ts";

const directory = resolve("/bound/project");
for (const [method, operation, args, result, call] of [
  ["isDirty", "git.IsDirty", [], true, (git: Git, ctx: ReturnType<typeof fakeContext>["ctx"]) => git.isDirty(ctx)],
  ["filesetHash", "git.FilesetHash", [], "content-hash", (git: Git, ctx: ReturnType<typeof fakeContext>["ctx"]) => git.filesetHash(ctx)],
  ["changedFiles", "git.ChangedFiles", ["base-revision"], ["file.ts"], (git: Git, ctx: ReturnType<typeof fakeContext>["ctx"]) => git.changedFiles(ctx, "base-revision")],
  ["tagsAtHead", "git.TagsAtHead", [], ["v1.2.3"], (git: Git, ctx: ReturnType<typeof fakeContext>["ctx"]) => git.tagsAtHead(ctx)],
  ["latestTag", "git.LatestTag", ["release-"], "release-2", (git: Git, ctx: ReturnType<typeof fakeContext>["ctx"]) => git.latestTag(ctx, "release-")],
  ["pushTag", "git.PushTag", ["v2", "release message"], undefined, (git: Git, ctx: ReturnType<typeof fakeContext>["ctx"]) => git.pushTag(ctx, "v2", "release message")],
] as const) {
  test(`Git instance ${method} preserves its host request and result`, async () => {
    const { ctx } = fakeContext(() => { throw new Error("SDK spawned a command"); });
    const git = new Git(directory, { repo: "owner/project", commit: "abc", branch: "main" });
    const requests: { operation: string; args: readonly unknown[] }[] = [];
    registerHelpers(ctx, { async helper(actual: string, actualArgs: readonly unknown[]) {
      requests.push({ operation: actual, args: actualArgs });
      return result;
    } } as unknown as HostServices);
    const pending = call(git, ctx);
    assert.ok(pending instanceof Promise);
    assert.deepEqual(await pending, result);
    assert.deepEqual(requests, [{ operation, args: [directory, ...args] }]);
    const failure = new Error("host failed");
    registerHelpers(ctx, { async helper() { throw failure; } } as unknown as HostServices);
    await assert.rejects(call(git, ctx), error => error === failure);
  });
}

test("public approval and job flags preserve independent declarations", async () => {
  const plan = new Plan();
  const source = plan.job("source", () => 1);
  const generator = source.generate(() => {});
  const gate: Approval = plan.approval("gate").needs(source).needsIfPresent("maybe").needsGenerated(generator).optional();
  plan.approval("continue").continueOnError();
  const job = plan.job("inline").inline().continueOnError();
  const before = job.step("before", () => 1);
  const first = job.step("first", () => 2).continueOnError();
  const second = job.step("second", () => 3);
  job.groupSteps("pair", first, second).needs(before).skipIf(() => true);
  for (const member of [first, second]) {
    const closure = member.closures().find(closure => closure.kind === "skip_if");
    assert.ok(closure?.kind === "skip_if");
    assert.equal(await closure.fn(fakeContext(() => "").ctx), true);
  }
  assert.equal(gate.id, "gate");
  const doc = plan.toDoc();
  const approval = doc.jobs.find(job => job.id === "gate")!;
  assert.deepEqual(approval.deps, ["source"]);
  assert.deepEqual(approval.deps_if_present, ["maybe"]);
  assert.deepEqual(approval.needs_generated, [generator.id]);
  assert.equal(approval.optional, true);
  assert.equal(doc.jobs.find(job => job.id === "continue")?.continue_on_error, true);
  const inline = doc.jobs.find(job => job.id === "inline")!;
  assert.equal(inline.inline, true);
  assert.equal(inline.continue_on_error, true);
  assert.equal(inline.optional, undefined);
  assert.deepEqual(inline.steps?.find(step => step.id === "first")?.needs, ["before"]);
  assert.deepEqual(inline.steps?.find(step => step.id === "second")?.needs, ["before"]);
  assert.equal(inline.steps?.find(step => step.id === "first")?.continue_on_error, true);
  assert.equal(inline.steps?.find(step => step.id === "first")?.optional, undefined);
});

test("public generated approvals and groups retain expansion identity", async () => {
  const plan = new Plan();
  const source = plan.job("source", () => 1);
  const generator = source.generate(generated => {
    const one = generated.job("one", () => 1);
    const two = generated.job("two", () => 2);
    const group = generated.groupJobs("pair", one, two);
    generated.approval("gate").needs(group);
  });
  const closure = plan.findClosure(generator.id);
  assert.equal(closure?.kind, "generate");
  if (closure?.kind !== "generate") throw new Error("missing generation callback");
  const generated = new Generated(source, generator.id, plan);
  await closure.fn(generated, 1, fakeContext(() => "").ctx);
  const doc = generated.toDoc();
  assert.deepEqual(doc.jobs.find(job => job.id === "one")?.groups, [generator.id, "pair"]);
  assert.deepEqual(doc.jobs.find(job => job.id === "two")?.groups, [generator.id, "pair"]);
  const gate = doc.jobs.find(job => job.id === "gate")!;
  assert.deepEqual(gate.approval, {});
  assert.deepEqual(gate.deps, ["source", "one", "two"]);
  assert.deepEqual(gate.groups, [generator.id]);
});

test("public context methods preserve host arguments and return values", async () => {
  const base = fakeContext(() => "").ctx;
  const reference = refToLastRun<number>("upstream", "result");
  const frames: Record<string, unknown>[] = [];
  const calls: unknown[] = [];
  let command: HostExecRequest | undefined;
  const ctx = makeContext({
    run: base.run, jobId: "job", stepId: "compile", args: {}, signal: base.signal, outputs: new Map(),
    definition: { name: "Context", plan() {} }, frames: { write(line) { frames.push(JSON.parse(line)); } },
    services: {
      async latest(ref: unknown, signal: AbortSignal) { calls.push(["latest", ref, signal]); return 42; },
      async oidcToken(audience: string, signal: AbortSignal) { calls.push(["oidc", audience, signal]); return "fixture-token"; },
    } as unknown as HostServices,
    async hostExec(request) {
      command = request;
      return { exitCode: 0, signaled: false, notFound: false, stdout: Buffer.from("captured"), stderr: Buffer.alloc(0) };
    },
  });
  assert.equal(await ctx.latest(reference), 42);
  assert.equal(await ctx.oidcToken("audience"), "fixture-token");
  assert.deepEqual(calls, [["latest", reference, base.signal], ["oidc", "audience", base.signal]]);
  const result = await ctx.bash("echo captured", { cwd: directory }).capture();
  assert.deepEqual(command?.argv, ["bash", "-c", "echo captured"]);
  assert.equal(command?.dir, directory);
  assert.equal(result.stdout, "captured");
  ctx.log.warn("warning", { code: 3 });
  ctx.log.error("failure", { code: 7 });
  assert.deepEqual(frames.filter(frame => frame.msg === "warning" || frame.msg === "failure").map(({ level, msg, attrs, step }) => ({ level, msg, attrs, step })), [
    { level: "warn", msg: "warning", attrs: { code: 3 }, step: "compile" },
    { level: "error", msg: "failure", attrs: { code: 7 }, step: "compile" },
  ]);
});

test("public string and production-tag methods return their declared values", async () => {
  assert.equal(new ConcurrencyGroup("budget", { capacity: 2 }).toString(), "budget");
  const { ctx } = fakeContext(() => "");
  registerHelpers(ctx, { async helper() { return { commit: "abc", content: "def", branch: "main", dirty: false }; } } as unknown as HostServices);
  assert.equal((await docker.computeTagsIn(ctx, directory)).prodTag(), "commit-abc-files-def-prod");
});

test("public pipeline callbacks contribute observable discovery declarations", async () => {
  resetPipelines();
  try {
    definePipeline({ name: "PublicCallback", plan(plan) {
      plan.job("publish").step("upload", () => true).risk("prod");
    } });
    const doc = await describe(fakeContext(() => "").ctx.run);
    assert.deepEqual(doc.pipelines[0]?.risks, ["prod"]);
    assert.deepEqual(doc.pipelines[0]?.risks_by_step, [{ job_id: "publish", step_id: "upload", labels: ["prod"] }]);
  } finally { resetPipelines(); }
});

test("public cleanup registrations unregister the owned resource after cancellation", async () => {
  const base = fakeContext(() => "").ctx;
  const controller = new AbortController();
  const requests: { method: string | undefined; path: string; body: unknown; aborted: boolean | undefined }[] = [];
  const routes = new NodeRoutes({ url: "http://127.0.0.1:12345", bearer: "fixture-only" }, async (url, options) => {
    requests.push({ method: options?.method, path: new URL(String(url)).pathname, body: options?.body ? JSON.parse(String(options.body)) : undefined, aborted: options?.signal?.aborted });
    return Response.json(options?.method === "POST" ? { id: "owned/id" } : {});
  });
  const ctx = makeContext({ run: base.run, jobId: "job", args: {}, signal: controller.signal, outputs: new Map(), definition: { name: "Cleanup", plan() {} }, frames: { write() {} }, services: routes });
  const registration = await ctx.cleanup({ argv: ["remove", "resource"] });
  assert.equal(registration.id, "owned/id");
  controller.abort();
  await registration.unregister();
  assert.deepEqual(requests, [
    { method: "POST", path: "/node/v1/cleanups", body: { argv: ["remove", "resource"] }, aborted: false },
    { method: "DELETE", path: "/node/v1/cleanups/owned%2Fid", body: undefined, aborted: false },
  ]);
});
