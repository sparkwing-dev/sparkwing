import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { SDKError } from "../src/errors.ts";
import { Masker, PlanningDiagnostics } from "../src/diagnostics.ts";
const vectors = JSON.parse(readFileSync(new URL("../../../internal/secrets/testdata/masking.json", import.meta.url), "utf8")) as {
  name: string;
  values: string[];
  input: string;
  want: string;
  jsonInput?: string;
  jsonWant?: string;
}[];
for (const vector of vectors)
  test(`masking conformance: ${vector.name}`, () => {
    const masker = new Masker();
    for (const value of vector.values) masker.register(value);
    assert.equal(masker.mask(vector.input), vector.want);
    if (vector.jsonInput) assert.deepEqual(masker.maskJSON(JSON.parse(vector.jsonInput)), JSON.parse(vector.jsonWant!));
    const diagnostics = new PlanningDiagnostics(
      vector.values.map((value, i) => ({ name: String(i), type: "string", secret: true, default: value })),
    );
    const error = diagnostics.error(
      new SDKError(vector.input, { code: "plan_failed", hint: vector.input, details: { nested: [vector.input] } }),
    );
    assert.equal(error.message, vector.want);
    assert.equal(error.hint, vector.want);
    assert.deepEqual(error.details, { nested: [vector.want] });
  });

test("explicit registration promotes a multiline fragment to a secret key", () => {
  const masker = new Masker();
  masker.register('attribute"sentinel\nvalue');
  assert.deepEqual(masker.maskJSON({ value: true }), { value: true });
  masker.register("value");
  assert.deepEqual(masker.maskJSON({ value: true }), { "***": true });
});
