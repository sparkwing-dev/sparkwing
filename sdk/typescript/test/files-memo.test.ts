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
  Plan,
  memoKey,
  NoProjectError,
} from "../src/index.ts";
import { runScope } from "../src/scope.ts";

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

test("memo recipes can be declared during planning without filesystem or environment reads", () => {
  runScope({ phase: "plan", root: "/missing" }, () => {
    const plan = new Plan();
    const recipe = compose(files("*.txt"), repoFiles(ignore("cache/")), tree("missing"), env("MODE"), constant("v1"));
    plan.job("build", () => {}).memo(recipe);
    const job = plan.toDoc().jobs[0]!;
    assert.deepEqual(job.memo?.input, recipe);
    assert.equal(job.closures?.memo_key, undefined);
    assert.deepEqual(memoKey("ab", "c"), ["ab", "c"]);
  });
});
