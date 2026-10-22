import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { splitList } from "../src/args.ts";

test("list parsing matches the shared host normalization contract", () => {
  const cases: { input: string; want: string[] }[] = JSON.parse(
    readFileSync(new URL("../../../internal/argvalue/testdata/lists.json", import.meta.url), "utf8"),
  );
  for (const item of cases) assert.deepEqual(splitList(item.input), item.want);
});
