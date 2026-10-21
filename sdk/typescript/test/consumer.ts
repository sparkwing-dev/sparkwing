import {
  definePipeline,
  Plan,
  ConcurrencyGroup,
  refTo,
  refToLastRun,
  startup,
  dir,
  goModules,
  npm,
  constant,
  noMemo,
  compose,
  memoKey,
  files,
  tree,
  repoFiles,
  ignore,
  env,
  withCommandEnv,
  docker,
  git,
  services,
} from "@sparkwing/sdk";
import type {
  Context,
  Args,
  Job,
  Step,
  Body,
  CacheSpec,
  CleanupSpec,
  Annotation,
  RunContext,
  ChildOptions,
  PipelineDef,
} from "@sparkwing/sdk";

const input: Args = { count: "1" };
const body: Body<{ count: number }> = (ctx) => ({ count: Number(ctx.args.count) });
const plan = new Plan();
const job: Job<{ count: number }> = plan.job("build", body);
const group = new ConcurrencyGroup("group", { capacity: 2, scope: "box", onLimit: "queue", queueTimeoutMs: 0 });
const reference = refToLastRun<{ count: number }>("build", "result", { maxAgeMs: 0 });
const caches: CacheSpec[] = [goModules(), npm(), dir("cache", "package-lock.json")];
job
  .withArgs({ name: "count", type: "int64", default: "1" })
  .references(reference)
  .cache(...caches)
  .memo(compose(constant("v1"), files("*.ts"), tree("src"), repoFiles(ignore("generated/")), env("CI")))
  .retry(0, { backoffMs: 0 })
  .timeout(0)
  .noProgressTimeout(0)
  .concurrency(group)
  .requires("os=linux")
  .prefers("fast")
  .needsTools("go")
  .inline()
  .optional()
  .continueOnError()
  .env("NODE_OPTIONS", "--max-old-space-size=1024", startup())
  .artifacts("out/**");
const step: Step<string> = job
  .step("step", () => "text")
  .dryRun(() => "dry")
  .safeWithoutDryRun()
  .risk("prod")
  .finally()
  .optional()
  .continueOnError()
  .skipIf(() => false)
  .result();
job
  .groupSteps("steps", step)
  .needs(step)
  .skipIf(() => false);
plan
  .groupJobs("jobs", job)
  .requires("cpu=amd64")
  .memo(() => noMemo);
const generator = job.generate((generated, output) => {
  generated.job("child", () => output);
  generated.approval("gate").skipIf(() => false);
});
plan
  .approval("approve", { timeoutMs: 0 })
  .needs(job)
  .needsGenerated(generator)
  .optional()
  .continueOnError()
  .onFailure("recover", () => 0);
plan
  .priority(0)
  .claimWait(0)
  .admissionClass("standard")
  .concurrency(group)
  .checkout({ fullHistory: false, depth: 0, tags: false });
const definition: PipelineDef = {
  name: "consumer",
  plan(_plan: Plan, _run: RunContext) {},
  args: [{ name: "list", type: "[]string", enum: ["one"], required: true }],
};
definePipeline(definition);
async function contextSurface(ctx: Context) {
  const output: { count: number } = await refTo(job).get(ctx);
  const maybe = await reference.tryGet(ctx);
  const child: ChildOptions = { inputs: { count: 1, names: ["one"] }, args: input, timeoutMs: 0 };
  await ctx.runPipeline("child", "result", child);
  await ctx.latest(reference);
  await ctx.secret("TOKEN");
  await ctx.variable("REGION");
  await ctx.oidcToken("audience");
  const annotation: Annotation = { message: "note", file: "x", line: 1, endLine: 2, column: 1 };
  await ctx.annotate(annotation);
  await ctx.summary("text");
  await ctx.mask("value");
  const cleanup: CleanupSpec = { argv: ["echo", "cleanup"] };
  await (await ctx.cleanup(cleanup)).unregister();
  await withCommandEnv(ctx, { CI: "true" }).exec(["echo", "ok"]).dir(".").envMap({ A: "B" }).env("C", "D").capture();
  await ctx.bash("echo ok").mustBeEmpty("reason");
  ctx.stepOutput(step);
  const build: docker.BuildConfig = { image: "image", tags: ["tag"] };
  const service: services.Service = { image: "redis", hostPort: services.autoPort };
  const clone: git.CloneOptions = { depth: 1 };
  void [output, maybe, build, service, clone, memoKey(1)];
}
void contextSurface;
// @ts-expect-error Removed runtime plumbing is not part of the public SDK.
import { NodeRoutes } from "@sparkwing/sdk";
// @ts-expect-error Result reuse is memo, and the old cache alias is removed.
job.memoize(() => "key");
// @ts-expect-error Approval bodies and before hooks are removed.
plan.approval("control").beforeRun(() => {});
import { SDKError, ExecError, InputError, VerifyError, StepError } from "@sparkwing/sdk";
new SDKError("failed", { code: "sdk_error" });
new ExecError({ command: "tool", stdout: "", stderr: "failed", exitCode: 1 });
new InputError(new Error("input"));
new VerifyError(new Error("verify"));
new StepError("step", new Error("body"));
// @ts-expect-error Execution diagnostics require the command result.
new ExecError();
