import assert from "node:assert/strict";
import test from "node:test";
import * as fs from "node:fs/promises";
import { tmpdir } from "node:os";
import * as paths from "node:path";
import {
  workDir,
  files,
  parts,
  tree,
  repoFiles,
  ignore,
  constant,
  env,
  compose,
  Plan,
  memoKey,
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

for (const value of [NaN, Infinity, -Infinity, undefined, { nested: undefined }, [undefined], { nested: NaN }, Array(1)]) {
  test(`memo literals reject lossy JSON values: ${String(value)}`, () => {
    for (const recipe of [parts(value), compose(constant("v1"), parts(value))]) {
      assert.throws(() => new Plan().job("build", () => {}).memo(recipe), /JSON serializable/);
    }
  });
}

test("memo literals preserve JSON values and distinguish constants from parts", () => {
  const plan = new Plan();
  const literal = { nested: [null, "", 0, false] };
  plan.job("build", () => {}).memo(parts(literal));
  literal.nested[1] = "changed";
  plan.job("constant", () => {}).memo(constant(""));
  plan.job("part", () => {}).memo(parts(""));
  assert.deepEqual(JSON.parse(JSON.stringify(plan.toDoc())).jobs.map((job: { memo: unknown }) => job.memo), [
    { input: { kind: "parts", parts: [{ nested: [null, "", 0, false] }] } },
    { input: { kind: "const", value: "" } },
    { input: { kind: "parts", parts: [""] } },
  ]);
});
