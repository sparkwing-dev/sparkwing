import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

test("initialization security comments limit the inheritance guarantee to SDK tools", () => {
  for (const file of ["../src/main.ts", "../native/protocol.c"]) {
    const text = readFileSync(new URL(file, import.meta.url), "utf8");
    const comment = text.split("\n").find((line) => line.includes("// safety:") && line.includes("inherit"));
    assert.ok(comment, file);
    assert.match(comment, /before SDK tools spawn/, file);
    assert.doesNotMatch(comment, /before author/, file);
  }
});
