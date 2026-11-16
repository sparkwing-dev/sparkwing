import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { renderReference } from "../tools/reference.ts";

test("docs/sdk-typescript-reference.md matches index.d.ts; regenerate with bash bin/gen-ts-sdk-docs.sh", () => {
  const committed = readFileSync(new URL("../../../docs/sdk-typescript-reference.md", import.meta.url), "utf8");
  assert.equal(committed, renderReference());
});
