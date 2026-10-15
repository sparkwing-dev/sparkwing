import assert from "node:assert/strict";
import test from "node:test";
import * as fs from "node:fs/promises";
import { tmpdir } from "node:os";
import * as paths from "node:path";
import {
  workDir,
  path,
  readFile,
  writeFile,
  glob,
  files,
  tree,
  repoFiles,
  ignore,
  constant,
  env,
  compose,
  noMemo,
  memoKey,
  NoProjectError,
} from "../src/index.ts";
import { runScope } from "../src/scope.ts";
import { fakeContext } from "./helper-context.ts";

async function fixture(t: import("node:test").TestContext): Promise<string> {
  const root = await fs.mkdtemp(paths.join(tmpdir(), "sdk-files-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await fs.mkdir(paths.join(root, "nested"));
  await fs.writeFile(paths.join(root, "a.txt"), "one");
  await fs.writeFile(paths.join(root, "nested", "b.txt"), "two");
  await fs.writeFile(paths.join(root, "untracked.txt"), "ignored");
  return root;
}

test("file helpers resolve pipeline roots, preserve absolute paths and use nonrecursive globs", async (t) => {
  const root = await fixture(t);
  await runScope({ phase: "execute", root }, async () => {
    assert.equal(workDir(), root);
    assert.equal(path("nested", "b.txt"), paths.join(root, "nested", "b.txt"));
    assert.equal(Buffer.from(await readFile("a.txt")).toString(), "one");
    await writeFile("written.txt", Buffer.from("hello"));
    await writeFile("written.txt", Buffer.from("x"));
    assert.equal(Buffer.from(await readFile("written.txt")).toString(), "x");
    assert.deepEqual(await glob("nested/[ab].txt"), [paths.join(root, "nested", "b.txt")]);
    assert.deepEqual(await glob("**/?.txt"), [paths.join(root, "nested", "b.txt")]);
    assert.deepEqual(await glob("absent/*.txt"), []);
    await assert.rejects(glob("[oops"), /Invalid glob/);
    await assert.rejects(readFile("absent.txt"), { code: "ENOENT" });
  });
  runScope({ phase: "execute", root: "" }, () => {
    assert.equal(path(root, "a.txt"), paths.join(root, "a.txt"));
    assert.throws(() => path("a.txt"), NoProjectError);
  });
});

test("file and memo side effects reject planning before disk or git access", async () => {
  const fake = fakeContext(() => {
    throw new Error("must not execute");
  });
  await runScope({ phase: "plan", root: "/missing" }, async () => {
    await assert.rejects(readFile("file"), /planning/);
    await assert.rejects(writeFile("file", Buffer.from("x")), /planning/);
    await assert.rejects(glob("*"), /planning/);
    await assert.rejects(async () => files("*")(fake.ctx), /planning/);
    await assert.rejects(async () => tree(".")(fake.ctx), /planning/);
    assert.throws(() => env("PATH")(fake.ctx), /planning/);
  });
  assert.equal(fake.calls.length, 0);
});

test("tracked memo inputs hash content and relative names consistently across roots", async (t) => {
  const first = await fixture(t),
    second = await fixture(t);
  function context(root: string) {
    return fakeContext(({ argv }) => (argv[1] === "rev-parse" ? root : "nested/b.txt\0a.txt\0")).ctx;
  }
  const ctx = context(first);
  const before = await files("*.txt")(ctx);
  assert.equal(before, await files("*.txt")(context(second)));
  await fs.writeFile(paths.join(first, "untracked.txt"), "changed untracked");
  assert.equal(before, await files("*.txt")(ctx));
  assert.equal(await repoFiles(ignore("nested/"))(ctx), await files("a.txt")(ctx));
  assert.equal(await repoFiles(ignore("**/b.txt"))(ctx), await files("a.txt")(ctx));
  await fs.writeFile(paths.join(first, "a.txt"), "modified");
  assert.notEqual(before, await files("*.txt")(ctx));
  await fs.rm(paths.join(first, "a.txt"));
  await assert.rejects(async () => files("*.txt")(ctx), { code: "ENOENT" });
  assert.throws(() => files("dir/[oops"), /Invalid glob/);
  assert.throws(() => repoFiles(ignore("[")), /Invalid glob/);
});

test("tree hashes recursively, skips symlinks and rejects missing or non-directory roots", async (t) => {
  const root = await fixture(t),
    ctx = fakeContext(() => "").ctx;
  await runScope({ phase: "execute", root }, async () => {
    const before = await tree(".")(ctx);
    if (process.platform !== "win32") await fs.symlink(paths.join(root, "a.txt"), paths.join(root, "linked.txt"));
    await fs.symlink(paths.join(root, "nested"), paths.join(root, "linked-dir"), process.platform === "win32" ? "junction" : "dir");
    assert.equal(before, await tree(".")(ctx));
    await fs.writeFile(paths.join(root, "nested", "b.txt"), "updated");
    assert.notEqual(before, await tree(".")(ctx));
    await assert.rejects(async () => tree("absent")(ctx), { code: "ENOENT" });
    await assert.rejects(async () => tree("a.txt")(ctx), /Not a directory/);
  });
});

test("directory ignore basenames match at every depth", async (t) => {
  const root = await fixture(t);
  await fs.mkdir(paths.join(root, "nested", "cache"));
  await fs.mkdir(paths.join(root, "cache"));
  await fs.writeFile(paths.join(root, "nested", "cache", "data"), "nested cache");
  await fs.writeFile(paths.join(root, "cache", "data"), "root cache");
  const fake = fakeContext(({ argv }) => (argv[1] === "rev-parse" ? root : "a.txt\0nested/cache/data\0cache/data\0"));
  assert.equal(await repoFiles(ignore("cache/"))(fake.ctx), await files("a.txt")(fake.ctx));
});

test("memo composition preserves errors and bypass without evaluating subsequent inputs", async () => {
  const ctx = fakeContext(() => "").ctx;
  let calls = 0;
  assert.equal(
    await compose(
      () => noMemo,
      () => {
        calls++;
        return "bad";
      },
    )(ctx),
    noMemo,
  );
  assert.equal(calls, 0);
  const failure = new Error("unreadable");
  await assert.rejects(
    async () =>
      compose(
        () => {
          throw failure;
        },
        () => {
          calls++;
          return "bad";
        },
      )(ctx),
    (error) => error === failure,
  );
  assert.equal(calls, 0);
  await assert.rejects(async () => compose(() => "")(ctx), /empty key/);
  assert.throws(() => constant(""), /empty/);
  assert.equal(await constant("key")(ctx), "key");
  assert.notEqual(memoKey("ab", "c"), memoKey("a", "bc"));
  assert.equal(memoKey({ b: 2, a: 1 }), memoKey({ a: 1, b: 2 }));
  assert.throws(() => memoKey(undefined), /JSON serializable/);
});

test("environment keys sort names and distinguish unset from empty", async (t) => {
  const ctx = fakeContext(() => "").ctx;
  const old = process.env.SPARKWING_TEST_MEMO_HELPER;
  t.after(() => {
    if (old === undefined) delete process.env.SPARKWING_TEST_MEMO_HELPER;
    else process.env.SPARKWING_TEST_MEMO_HELPER = old;
  });
  delete process.env.SPARKWING_TEST_MEMO_HELPER;
  const unset = await env("SPARKWING_TEST_MEMO_HELPER", "PATH")(ctx);
  assert.equal(unset, await env("PATH", "SPARKWING_TEST_MEMO_HELPER")(ctx));
  process.env.SPARKWING_TEST_MEMO_HELPER = "";
  assert.notEqual(unset, await env("SPARKWING_TEST_MEMO_HELPER", "PATH")(ctx));
});
