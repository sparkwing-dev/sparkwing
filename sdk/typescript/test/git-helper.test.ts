import assert from "node:assert/strict";
import test from "node:test";
import * as fs from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { definePipeline, git, Git, gitFromTree, githubOwnerRepo, withCommandEnv } from "../src/index.ts";
import { resetPipelines } from "../src/plan.ts";
import { runScope } from "../src/scope.ts";
import { fakeContext, commandFailure } from "./helper-context.ts";
import { Host, hello } from "./helpers.ts";

test("Git helpers clone, fetch and protect tags against a real local remote", async (t) => {
  const root = await fs.mkdtemp(join(tmpdir(), "sdk-git-remote "));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const seed = join(root, "seed"),
    remote = join(root, "remote.git"),
    clone = join(root, "clone with spaces");
  const config = join(root, "gitconfig"),
    hooks = join(root, "hooks");
  await fs.mkdir(hooks);
  await fs.writeFile(config, "");
  resetPipelines();
  definePipeline({
    name: "fixture",
    plan(plan) {
      plan.job("build", async (bound) => {
        const ctx = withCommandEnv(bound, { GIT_CONFIG_GLOBAL: config, GIT_CONFIG_NOSYSTEM: "1" });
        const command = (...args: [string, ...string[]]) => ctx.exec(["git", ...args]).string();
        const configure = async (directory: string) => {
          for (const [name, value] of [
            ["user.name", "SDK fixture"],
            ["user.email", "sdk@example.invalid"],
            ["commit.gpgsign", "false"],
            ["tag.gpgsign", "false"],
            ["core.hooksPath", hooks],
          ])
            await command("-C", directory, "config", "--local", name!, value!);
        };
        await command("init", "--initial-branch=main", seed);
        await command("init", "--bare", "--initial-branch=main", remote);
        await configure(seed);
        const commit = async (content: string) => {
          await fs.writeFile(join(seed, "file.txt"), content);
          await command("-C", seed, "add", "file.txt");
          await command("-C", seed, "commit", "-m", content);
          return git.currentSHA(ctx, seed);
        };
        await commit("first");
        const second = await commit("second");
        await command("-C", seed, "remote", "add", "origin", remote);
        await command("-C", seed, "push", "origin", "main");
        await git.clone(ctx, pathToFileURL(remote).href, "clone with spaces", { depth: 1 });
        await configure(clone);
        assert.equal(await command("-C", clone, "rev-parse", "--is-shallow-repository"), "true");
        assert.equal(await command("-C", clone, "rev-list", "--count", "HEAD"), "1");
        const snapshot = await gitFromTree(ctx, clone);
        assert.equal(snapshot.commit, second);
        assert.equal(snapshot.branch, "main");
        assert.equal(snapshot.defaultBranch, "main");
        assert.equal(await snapshot.isDirty(ctx), false);

        await git.pushTag(ctx, clone, "sdk/v1.0.0", "release");
        assert.equal(await git.tagExistsOnRemote(ctx, clone, "sdk/v1.0.0"), true);
        await assert.rejects(git.pushTag(ctx, clone, "sdk/v1.0.0", "retry"), /already exists on remote/);
        const third = await commit("third");
        await command("-C", seed, "push", "origin", "main");
        await git.fetch(ctx, clone);
        assert.equal(await git.currentSHA(ctx, clone), second);
        assert.equal(await command("-C", clone, "rev-parse", "origin/main"), third);
        await command("-C", clone, "merge", "--ff-only", "origin/main");
        assert.equal(await git.currentSHA(ctx, clone), third);
        assert.equal(snapshot.commit, second);
        assert.deepEqual(await git.changedFiles(ctx, clone, second), ["file.txt"]);
        await command("-C", clone, "tag", "sdk/stale", second);
        await assert.rejects(git.pushTag(ctx, clone, "sdk/stale", "stale"), /outside HEAD/);
        assert.equal(await git.tagExistsOnRemote(ctx, clone, "sdk/stale"), false);
        await command("-C", clone, "tag", "sdk/v1.1.0");
        await git.pushTag(ctx, clone, "sdk/v1.1.0", "existing local tag at HEAD");
        assert.equal(await git.tagExistsOnRemote(ctx, clone, "sdk/v1.1.0"), true);
        assert.equal(await git.latestTag(ctx, clone, "sdk/"), "sdk/v1.1.0");
        for (const directory of ["ignored", "nested/ignored"]) {
          const ignored = join(clone, directory, "payload.txt");
          await fs.mkdir(join(clone, directory), { recursive: true });
          await fs.writeFile(join(clone, ".dockerignore"), `${directory}\n`);
          await fs.writeFile(ignored, "first ignored content");
          const hash = await snapshot.filesetHash(ctx);
          await fs.writeFile(ignored, "changed ignored content");
          assert.equal(await snapshot.filesetHash(ctx), hash, `contents beneath ${directory} must be ignored`);
          await fs.writeFile(join(clone, "file.txt"), directory);
          assert.notEqual(await snapshot.filesetHash(ctx), hash, "included files must still change the hash");
        }
        await fs.rm(remote, { recursive: true });
        await assert.rejects(git.tagExistsOnRemote(ctx, clone, "sdk/v2.0.0"), (error: unknown) => {
          assert.ok(error instanceof Error && "exitCode" in error);
          assert.notEqual(error.exitCode, 0);
          return true;
        });
        return "verified";
      });
    },
  });
  const host = new Host({ root });
  try {
    const binding = { run_id: "run", job_id: "build", routes: { url: "http://127.0.0.1:1", bearer: "unused" } };
    assert.equal((await host.send({ ...hello, binding })).ok, true);
    const planned = await host.send({ id: "plan", op: "plan", pipeline: "fixture", args: {}, run: { run_id: "run", pipeline: "fixture" } });
    assert.equal(planned.ok, true, JSON.stringify(planned));
    const executed = await host.send({ id: "body", op: "run_step", settled: [], job: "build", step: "run", attempt: 1 });
    assert.equal(executed.result?.outcome, "success", JSON.stringify(executed));
    assert.equal(executed.result.output, "verified");
  } finally {
    await host.close();
  }
});

test("git queries preserve sealed snapshot and use direct promptless commands", async () => {
  const root = resolve("repo");
  const fake = fakeContext(({ argv, options }) => {
    assert.equal(options.cwd, root);
    assert.equal(options.env?.GIT_TERMINAL_PROMPT, "0");
    assert.equal(options.env?.GCM_INTERACTIVE, "0");
    if (argv[1] === "rev-parse") return "0123456789abcdef";
    if (argv[1] === "symbolic-ref") return argv.at(-1) === "HEAD" ? "new-branch" : "origin/main";
    if (argv[1] === "remote") return argv[2] === "get-url" ? "git@github.com:owner/repo.git" : "origin";
    if (argv[1] === "status") return " M file";
    if (argv[1] === "diff") return "space name.txt\0line\nname.txt\0";
    throw new Error("unexpected command");
  });
  const snapshot = new Git(root, { repo: "owner/repo", branch: "sealed", commit: "sealed0123456789" });
  assert.equal(snapshot.branch, "sealed");
  assert.equal(snapshot.shortSHA(), "sealed012345");
  assert.equal(snapshot.name(), "repo");
  assert.equal(await snapshot.isDirty(fake.ctx), true);
  assert.deepEqual(await snapshot.changedFiles(fake.ctx, "--bad"), ["space name.txt", "line\nname.txt"]);
  const diff = fake.calls.find((call) => call.argv[1] === "diff")!.argv;
  assert.deepEqual(diff.slice(-2), ["--end-of-options", "--bad...HEAD"]);
  const discovered = await gitFromTree(fake.ctx, root);
  assert.equal(discovered.repo, "owner/repo");
  assert.equal(discovered.commit, "0123456789abcdef");
  assert.equal(discovered.branch, "new-branch");
  assert.equal(discovered.defaultBranch, "main");
  assert.deepEqual(githubOwnerRepo(" owner/repo "), { owner: "owner", repo: "repo" });
  assert.deepEqual(githubOwnerRepo("repo"), { owner: "", repo: "" });
});

test("git detached state is optional while transport and non-repository failures propagate", async () => {
  const detached = fakeContext(({ argv }) => {
    if (argv[1] === "symbolic-ref") throw commandFailure("detached");
    return ".git";
  });
  assert.equal(await git.currentBranch(detached.ctx, "/repo"), "");
  const failed = fakeContext(() => {
    throw commandFailure("broken repository", 128);
  });
  await assert.rejects(git.currentBranch(failed.ctx, "/repo"), /broken repository/);
  await assert.rejects(git.remoteOriginURL(failed.ctx, "/repo"), /broken repository/);
});

test("latest tag uses semantic ordering and literal prefixes", async () => {
  const fake = fakeContext(() =>
    [
      "v0.2.0",
      "v0.10.0-rc.2",
      "v0.10.0-rc.12",
      "v0.10.0",
      "v0.11.0-beta",
      "garbage",
      "v01.12.0",
      "sdk/v9.0.0",
      "sdk/v10.0.0",
      "sdk/v10.0.0-rc.1",
    ].join("\n"),
  );
  assert.equal(await git.latestTag(fake.ctx, "/repo", ""), "v0.11.0-beta");
  assert.equal(await git.latestTag(fake.ctx, "/repo", "sdk/"), "sdk/v10.0.0");
  assert.equal(await git.latestTag(fake.ctx, "/repo", "none"), "");
});

test("tag publishing refuses remote and local collisions and permits retry at HEAD", async () => {
  const remote = fakeContext(({ argv }) => (argv[1] === "ls-remote" ? "abc\trefs/tags/v1.0.0\n" : ""));
  await assert.rejects(git.pushTag(remote.ctx, "/repo", "v1.0.0", ""), /already exists/);
  assert.equal(
    remote.calls.some((call) => call.argv[1] === "push" || call.argv[1] === "tag"),
    false,
  );
  const mismatch = fakeContext(({ argv }) => (argv[1] === "rev-parse" ? (argv.includes("--verify") ? "old" : "head") : ""));
  await assert.rejects(git.pushTag(mismatch.ctx, "/repo", "v1.0.0", ""), /outside HEAD/);
  assert.equal(
    mismatch.calls.some((call) => call.argv[1] === "push"),
    false,
  );
  const retry = fakeContext(({ argv }) => (argv[1] === "rev-parse" ? "head" : ""));
  await git.pushTag(retry.ctx, "/repo", "v1.0.0", "release");
  assert.equal(
    retry.calls.some((call) => call.argv[1] === "tag"),
    false,
  );
  assert.deepEqual(retry.calls.at(-1)!.argv, ["git", "push", "origin", "refs/tags/v1.0.0"]);
  const failure = new Error("transport failed");
  const broken = fakeContext(({ argv }) => {
    if (argv[1] === "ls-remote") throw failure;
    return "";
  });
  await assert.rejects(git.pushTag(broken.ctx, "/repo", "v1.0.0", ""), (error) => error === failure);
  const absent = fakeContext(({ argv }) => {
    if (argv.includes("--verify")) throw commandFailure("missing tag");
    return "";
  });
  await git.pushTag(absent.ctx, "/repo", "v1.0.0", "");
  assert.deepEqual(absent.calls.find((call) => call.argv[1] === "tag")!.argv, ["git", "tag", "-a", "v1.0.0", "-m", "v1.0.0"]);
  await assert.rejects(git.pushTag(absent.ctx, "/repo", "-invalid", ""), /valid nonempty tag/);
});

test("git clone and fetch have no cache-service path and enforce planning purity", async () => {
  const fake = fakeContext(() => "");
  await runScope({ phase: "execute", root: "/repo" }, async () => {
    await git.clone(fake.ctx, "https://example.test/owner/repo", "dest", { depth: 2 });
    await git.fetch(fake.ctx, "/repo");
  });
  assert.deepEqual(fake.calls[0]!.argv, ["git", "clone", "--depth", "2", "--", "https://example.test/owner/repo", "dest"]);
  assert.deepEqual(fake.calls[1]!.argv, ["git", "fetch"]);
  await runScope({ phase: "plan", root: "/repo" }, async () => {
    await assert.rejects(git.currentSHA(fake.ctx, "/repo"), /planning/);
  });
  assert.equal(fake.calls.length, 2);
});

test("fileset hashes local bytes, permissions, filenames and Docker ignore rules", async (t) => {
  const root = await fs.mkdtemp(join(tmpdir(), "sdk-git-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await fs.mkdir(root + "/.git");
  await fs.writeFile(root + "/a", "one");
  await fs.writeFile(root + "/ignored", "noise");
  await fs.writeFile(root + "/.dockerignore", "ignored\n");
  const fake = fakeContext(() => "a\0ignored\0gone\0");
  const before = await git.filesetHash(fake.ctx, root);
  await fs.writeFile(root + "/ignored", "different noise");
  assert.equal(before, await git.filesetHash(fake.ctx, root));
  await fs.writeFile(root + "/a", "two");
  assert.notEqual(before, await git.filesetHash(fake.ctx, root));
  const edited = await git.filesetHash(fake.ctx, root);
  await fs.chmod(root + "/a", 0o755);
  if (process.platform !== "win32") assert.notEqual(edited, await git.filesetHash(fake.ctx, root));
});
