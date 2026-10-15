import assert from "node:assert/strict";
import test from "node:test";
import * as fs from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { git, Git, gitFromTree, githubOwnerRepo } from "../src/index.ts";
import { runScope } from "../src/scope.ts";
import { fakeContext, commandFailure } from "./helper-context.ts";

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
