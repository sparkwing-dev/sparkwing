import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import type { ArgSpec } from "../index.d.ts";
import { SDKError, decodeError } from "../src/errors.ts";
import { Masker, PlanningDiagnostics } from "../src/diagnostics.ts";
const vectors = JSON.parse(readFileSync(new URL("../../../internal/secrets/testdata/masking.json", import.meta.url), "utf8")) as {
  name: string;
  values?: string[];
  lists?: string[];
  mergeValues?: string[];
  input: string;
  want: string;
  jsonInput?: string;
  jsonWant?: string;
}[];
for (const vector of vectors)
  test(`masking conformance: ${vector.name}`, () => {
    const masker = new Masker();
    const other = new Masker();
    for (const value of vector.mergeValues ?? []) other.register(value);
    masker.merge(other);
    for (const value of vector.values ?? []) masker.register(value);
    for (const value of vector.lists ?? []) masker.registerList(value);
    assert.equal(masker.mask(vector.input), vector.want);
    if (vector.jsonInput) assert.deepEqual(masker.maskJSON(JSON.parse(vector.jsonInput)), JSON.parse(vector.jsonWant!));
    const diagnostics = new PlanningDiagnostics(
      [...(vector.values ?? []), ...(vector.mergeValues ?? [])]
        .map((value, i) => ({ name: String(i), type: "string" as ArgSpec["type"], secret: true, default: value }))
        .concat(
          (vector.lists ?? []).map((value, i) => ({
            name: `list-${i}`,
            type: "[]string" as ArgSpec["type"],
            secret: true,
            default: value,
          })),
        ),
    );
    const error = diagnostics.error(
      new SDKError(vector.input, { code: "plan_failed", hint: vector.input, details: { nested: [vector.input] } }),
    );
    assert.equal(error.message, vector.want);
    assert.equal(error.hint, vector.want);
    assert.deepEqual(error.details, { nested: [vector.want] });
  });

test("short derived fragments become keys only when explicitly registered", () => {
  const masker = new Masker();
  masker.register('attribute"sentinel\nvalue');
  assert.deepEqual(masker.maskJSON({ value: true }), { value: true });
  masker.register("value");
  assert.deepEqual(masker.maskJSON({ value: true }), { "***": true });
});

test("planning code containing a secret uses the typed fallback", () => {
  const diagnostics = new PlanningDiagnostics([{ name: "token", type: "string", secret: true }], { token: "private-token" });
  assert.equal(diagnostics.error(new SDKError("message", { code: "prefix-private-token" })).code, "plan_failed");
});

test("planning details with colliding keys retain the wire error shape", () => {
  const diagnostics = new PlanningDiagnostics([{ name: "token", type: "string", secret: true }], { token: "private-token" });
  const error = diagnostics.error(new SDKError("message", { code: "plan_failed", details: { "private-token": 1, "***": 2 } }));
  assert.equal(error.details, undefined);
  assert.equal(decodeError(error).code, "plan_failed");
});
