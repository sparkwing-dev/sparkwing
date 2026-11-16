import assert from "node:assert/strict";
import { join } from "node:path";
import { test } from "node:test";
import { checkSnippets, extractSnippets, packageRoot } from "./doc-snippets.ts";

test("the snippet check reports a wrong SDK call at its documented line", () => {
  const snippets = extractSnippets(join(packageRoot, "test", "fixtures", "doc-snippets"));
  assert.deepEqual(
    snippets.map((s) => [s.line, s.skip]),
    [
      [6, undefined],
      [15, undefined],
      [24, undefined],
      [38, "illustrates a wrong call"],
    ],
  );
  const failures = checkSnippets(snippets);
  assert.equal(failures.length, 1, JSON.stringify(failures, null, 2));
  const [failure] = failures;
  assert.equal(failure?.snippet.line, 15);
  assert.match(failure?.messages.join("\n") ?? "", /^page\.md:16: Property 'output' does not exist on type 'Cmd'/);
});

test("every TypeScript block in docs/ type-checks against the published declarations", () => {
  const snippets = extractSnippets(join(packageRoot, "..", "..", "docs"));
  assert.ok(snippets.length > 0, "no ```ts blocks found under docs/");
  const failures = checkSnippets(snippets);
  assert.equal(failures.length, 0, failures.flatMap((f) => f.messages).join("\n"));
});
