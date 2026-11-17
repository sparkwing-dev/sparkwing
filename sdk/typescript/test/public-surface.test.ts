import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync } from "node:child_process";
import {
  git, docker, services, definePipeline, Git, gitFromTree, InputError, VerifyError, StepError, NoProjectError, workDir,
  Plan, Job, Step, ConcurrencyGroup, startup, constant, repoFiles, ignore, refTo, refToLastRun,
} from "../src/index.ts";
import type { docker as DockerAPI, services as ServicesAPI } from "../index.d.ts";
import type {
  AfterRunFn, Approval, ApprovalOptions, ArgSpec, Awaitable, Checkout, CleanupRegistration,
  ConcurrencyLimit, Dependency, EnvironmentDescription, EnvOption, Failure, FailurePolicy,
  GenerateFn, Generated, Generator, GitInfo, Hook, JobGroup, JobResult, Level, Logger,
  MemoInput, MemoKeyFn, MemoOptions, OnLimit, Outcome, PipelineRef, Predicate, PullRequest,
  Ref, RefOptions, RepoFilesOption, RetryOptions, RunnerInfo, Scope, SecretSpec, SkipOptions,
  StepGroup, UsageExample,
} from "../src/index.ts";
import { makeContext } from "../src/context.ts";
import { describe } from "../src/describe.ts";
import { resetPipelines } from "../src/plan.ts";
import { registerHelpers } from "../src/helpers/host.ts";
import type { HostServices } from "../src/routes.ts";
import { fakeContext } from "./helper-context.ts";

test("public gitFromTree preserves host facts and reports helper errors", async () => {
  const { ctx } = fakeContext(() => { throw new Error("SDK spawned a command"); });
  const calls: string[] = [];
  const facts: GitInfo = { repo: "owner/project", commit: "a".repeat(40), branch: "feature", defaultBranch: "main", repoURL: "https://github.com/owner/project.git" };
  const values: Record<string, string> = { "git.CurrentSHA": facts.commit, "git.CurrentBranch": facts.branch, "git.DefaultBranch": "main", "git.RemoteOriginURL": facts.repoURL! };
  registerHelpers(ctx, { async helper(operation: string, args: readonly unknown[]) {
    assert.deepEqual(args, ["/repo"]);
    calls.push(operation);
    return values[operation];
  } } as unknown as HostServices);
  const git = await gitFromTree(ctx, "/repo");
  assert.ok(git instanceof Git);
  assert.equal(git.workDir(), "/repo");
  assert.equal(git.repo, facts.repo);
  assert.equal(git.commit, facts.commit);
  assert.equal(git.branch, facts.branch);
  assert.equal(git.defaultBranch, "main");
  assert.deepEqual(calls, Object.keys(values));
  values["git.RemoteOriginURL"] = "https://example.com/owner/project.git";
  assert.equal((await gitFromTree(ctx, "/repo")).repo, "");
  registerHelpers(ctx, { async helper() { throw new Error("host failed"); } } as unknown as HostServices);
  await assert.rejects(gitFromTree(ctx, "/repo"), /host failed/);
});

test("public error wrappers retain causes and step identity", () => {
  const cause = new Error("failed");
  for (const ErrorType of [InputError, VerifyError]) {
    const error = new ErrorType(cause);
    assert.equal(error.cause, cause);
    assert.equal(error.message, "failed");
    assert.equal(error.name, ErrorType.name);
  }
  const error = new StepError("compile", cause);
  assert.equal(error.cause, cause);
  assert.equal(error.stepId, "compile");
  assert.equal(error.message, "failed");
});

test("public workDir throws NoProjectError outside a project", async () => {
  {
    const moduleURL = new URL("../src/index.ts", import.meta.url).href;
    const output = execFileSync(process.execPath, ["--input-type=module", "-e", `
      import { workDir, NoProjectError } from ${JSON.stringify(moduleURL)};
      try { workDir(); process.exitCode = 1; }
      catch (error) { if (!(error instanceof NoProjectError)) throw error; console.log(error.message); }
    `], { cwd: "/", encoding: "utf8" });
    assert.match(output, /no \.sparkwing project found/);
    assert.ok(new NoProjectError("missing") instanceof Error);
    assert.equal(typeof workDir(), "string");
  }
});

test("public declaration types preserve explicit options in plan output", async () => {
  const plan = new Plan();
  const checkout: Checkout = { fullHistory: false, depth: 0 };
  plan.checkout(checkout);
  const scope: Scope = "run", onLimit: OnLimit = "fail";
  const limit: ConcurrencyLimit = { capacity: 2, scope, onLimit };
  const capacity = new ConcurrencyGroup("shared", limit);
  const approvalOptions: ApprovalOptions = { timeoutMs: 0, onTimeout: "deny" };
  const approval: Approval = plan.approval("approve", approvalOptions);
  const dependency: Dependency = approval;
  const arg: ArgSpec = { name: "count", type: "int", default: "1" };
  const retry: RetryOptions = { backoffMs: 0 }, skip: SkipOptions = { budgetMs: 0 }, memo: MemoOptions = { ttlMs: 0 };
  const predicate: Predicate = () => false;
  const hook: Hook = () => {};
  const events: string[] = [];
  const after: AfterRunFn = (_ctx, result: JobResult<unknown>) => { events.push(result.outcome); };
  const option: EnvOption = startup();
  const failurePolicy: FailurePolicy = "collect_all";
  const input: MemoInput = constant("v1");
  const ignored: RepoFilesOption = ignore("generated/");
  assert.deepEqual(repoFiles(ignored), { kind: "repo_files", patterns: ["generated/"] });
  const job = plan.job<number>("build").needs(dependency).withArgs(arg).retry(2, retry).skipIf(predicate, skip).beforeRun(hook).afterRun(after).memo(input, memo).env("CI", "true", option).concurrency(capacity).failurePolicy(failurePolicy);
  assert.ok(job instanceof Job);
  const step = job.step("compile", () => 2);
  assert.ok(step instanceof Step);
  const steps: StepGroup = job.groupSteps("steps", step);
  const group: JobGroup = plan.groupJobs("builds", job);
  assert.equal(steps.members[0], step);
  assert.equal(group.members[0], job);
  const reference: Ref<number> = refTo(job);
  const refOptions: RefOptions = { fromAnyRun: true, maxAgeMs: 0 };
  const pipelineRef: PipelineRef<number> = refToLastRun("other", "output", refOptions);
  assert.equal(reference.jobId, "build");
  assert.equal(pipelineRef.fromAnyRun, true);
  const doc = plan.toDoc();
  assert.deepEqual(doc.source, { full_history: false, depth: 0 });
  const built = doc.jobs.find(value => value.id === "build")!;
  assert.deepEqual(built.retry, { attempts: 2, backoff_ms: 0 });
  assert.deepEqual(built.memo, { input: { kind: "const", value: "v1" }, ttl_ms: 0 });
  assert.deepEqual(built.env, { CI: { value: "true", startup: true } });
  assert.equal(built.skip_if_budget_ms, 0);
  assert.equal(built.failure_policy, "collect_all");
  assert.deepEqual(built.deps, ["approve"]);
  const { ctx } = fakeContext(() => "");
  for (const closure of job.closures()) if (closure.kind === "after_run") await closure.fn(ctx, { outcome: "failed" });
  assert.deepEqual(events, ["failed"]);
  const memoKey: MemoKeyFn = () => "key";
  const awaited: Awaitable<unknown> = memoKey(ctx);
  assert.equal(await awaited, "key");
  const generate: GenerateFn<number> = (generated: Generated, output) => { generated.job("child", () => output); };
  const generator: Generator<number> = job.generate(generate);
  assert.equal(generator.source, job);
});

test("public metadata and context types retain their values", async () => {
  const base = fakeContext(() => "").ctx;
  const runner: RunnerInfo = { name: "bound-runner", type: "test", labels: ["os=linux"] };
  const pullRequest: PullRequest = { number: 42, baseRef: "main", headRef: "feature" };
  const failure: Failure = { job: "build", stage: "verify", message: "unhealthy" };
  const frames: Record<string, unknown>[] = [];
  let unregistered = false;
  const ctx = makeContext({
    run: { ...base.run, runner, trigger: { source: "pull_request", pullRequest } }, jobId: "build", args: {}, failure,
    signal: base.signal, outputs: new Map(), definition: { name: "Metadata", plan() {} },
    frames: { write(line: string) { frames.push(JSON.parse(line)); } },
    services: { async cleanup(spec: { argv: readonly string[] }) {
      assert.deepEqual(spec.argv, ["remove", "resource"]);
      return { id: "owned", async unregister() { unregistered = true; } };
    } } as unknown as HostServices,
  });
  const outcome: Outcome = "failed";
  const result: JobResult<number> = { outcome, failure: ctx.failure!, output: 7 };
  assert.equal(result.failure?.stage, "verify");
  assert.equal(ctx.runner.name, "bound-runner");
  assert.equal(ctx.run.trigger.pullRequest?.number, 42);
  const level: Level = "warn";
  const logger: Logger = ctx.log;
  logger[level]("warning");
  assert.equal(frames[0]?.level, "warn");
  assert.equal(frames[0]?.msg, "warning");
  const cleanup: CleanupRegistration = await ctx.cleanup({ argv: ["remove", "resource"] });
  assert.equal(cleanup.id, "owned");
  await cleanup.unregister();
  assert.equal(unregistered, true);
  const secret: SecretSpec = { name: "TOKEN", optional: true };
  const environment: EnvironmentDescription = { name: "REGION", default: "west" };
  const example: UsageExample = { command: "sparkwing run build", comment: "Build" };
  resetPipelines();
  try {
    definePipeline({ name: "Metadata", examples: [example], envVars: [environment], secrets: [secret], plan() {} });
    const document = await describe(ctx.run);
    assert.deepEqual(document.pipelines[0]?.examples, [example]);
    assert.deepEqual(document.pipelines[0]?.env_vars, [environment]);
    assert.deepEqual(document.pipelines[0]?.secrets, [secret]);
  } finally { resetPipelines(); }
});

test("public Git namespace relays operation arguments and host results", async () => {
  const { ctx } = fakeContext(() => "");
  let request: { operation: string; args: readonly unknown[] } | undefined;
  let result: unknown;
  registerHelpers(ctx, { async helper(operation: string, args: readonly unknown[]) { request = { operation, args }; return result; } } as unknown as HostServices);
  for (const [operation, args, value, call] of [
    ["Fetch", ["repo"], undefined, () => git.fetch(ctx, "repo")],
    ["ShortCommit", ["repo"], "abc", () => git.shortCommit(ctx, "repo")],
    ["CurrentBranch", ["repo"], "feature", () => git.currentBranch(ctx, "repo")],
    ["DefaultBranch", ["repo"], "main", () => git.defaultBranch(ctx, "repo")],
    ["RemoteOriginURL", ["repo"], "remote", () => git.remoteOriginURL(ctx, "repo")],
    ["IsDirty", ["repo"], true, () => git.isDirty(ctx, "repo")],
    ["FilesetHash", ["repo"], "hash", () => git.filesetHash(ctx, "repo")],
    ["ChangedFiles", ["repo", "base"], ["file"], () => git.changedFiles(ctx, "repo", "base")],
    ["TagsAtHead", ["repo"], ["v1"], () => git.tagsAtHead(ctx, "repo")],
    ["LatestTag", ["repo", "v"], "v1", () => git.latestTag(ctx, "repo", "v")],
    ["TagExistsOnRemote", ["repo", "v1"], true, () => git.tagExistsOnRemote(ctx, "repo", "v1")],
  ] as const) {
    result = value;
    assert.deepEqual(await call(), value);
    assert.deepEqual(request, { operation: `git.${operation}`, args });
  }
});

test("public Docker namespace preserves builds, platforms, tags and error classes", async () => {
  const { ctx } = fakeContext(() => "");
  let request: { operation: string; args: readonly unknown[] } | undefined;
  let result: unknown;
  registerHelpers(ctx, { async helper(operation: string, args: readonly unknown[]) { request = { operation, args }; return result; } } as unknown as HostServices);
  const build: DockerAPI.BuildResult = { image: "registry/app", digests: { "registry/app": "sha256:value" }, registries: ["registry"] };
  result = build;
  assert.deepEqual(await docker.buildAndPush(ctx, { image: "app", tags: ["v1"] }), build);
  assert.equal(request?.operation, "docker.BuildAndPush");
  assert.equal((request?.args[0] as { image: string }).image, "app");
  result = undefined;
  await docker.push(ctx, "image", ["v1"], ["registry"]);
  assert.deepEqual(request, { operation: "docker.Push", args: ["image", ["v1"], ["registry"]] });
  result = ["linux/amd64"];
  assert.deepEqual(await docker.filterBuildxPlatforms(ctx, ["linux/amd64", "linux/arm64"]), ["linux/amd64"]);
  assert.deepEqual(request, { operation: "docker.FilterBuildxPlatforms", args: [["linux/amd64", "linux/arm64"]] });
  result = { commit: "abc", content: "def", dirty: true, branch: "feature" };
  const tags: DockerAPI.ImageTag = await docker.computeTagsIn(ctx, "repo");
  assert.deepEqual(request, { operation: "docker.ComputeTagsIn", args: ["repo"] });
  assert.deepEqual(tags.all(), ["commit-abc-files-def-dirty", "commit-abc-files-def-dirty-prod"]);
  assert.equal((await docker.computeTags(ctx)).deployTag(), tags.deployTag());
  assert.deepEqual(request, { operation: "docker.ComputeTagsIn", args: [ctx.run.git.workDir()] });
  const options: DockerAPI.RunOptions = { image: "app", cmd: ["true"] };
  result = { stdout: "", stderr: "" };
  await docker.run(ctx, options);
  assert.equal(request?.operation, "docker.Run");
  for (const [code, ErrorType] of [
    ["docker: buildx plugin required for multi-platform builds", docker.BuildxRequiredError],
    ["docker: requested platform not supported by active buildx builder", docker.PlatformUnsupportedError],
  ] as const) {
    const cause = Object.assign(new Error("host error"), { code });
    registerHelpers(ctx, { async helper() { throw cause; } } as unknown as HostServices);
    await assert.rejects(docker.buildxPlatforms(ctx), error => error instanceof ErrorType && error.cause === cause);
  }
});

test("public service addresses and default readiness timeout retain their contract", async () => {
  const { ctx } = fakeContext(() => "");
  const address: ServicesAPI.Addr = { name: "db", hostPort: 1234 };
  registerHelpers(ctx, { async helper(operation: string) { return operation === "services.Start" ? [{ ...address, cleanup_id: "owned" }] : null; } } as unknown as HostServices);
  let seen: readonly ServicesAPI.Addr[] = [];
  await services.withServicesAddrs(ctx, [{ image: "postgres", readyTimeoutMs: services.defaultReadyTimeoutMs }], (_ctx, addrs) => { seen = addrs; });
  assert.deepEqual(seen, [address]);
  assert.equal(services.defaultReadyTimeoutMs, 30000);
  assert.equal(services.autoPort, -1);
});
