import assert from "node:assert/strict";
import { test } from "node:test";
import { Host, hello } from "./helpers.ts";

test("protocol v2 negotiates once and rejects the removed operation", async () => {
  const host = new Host();
  try {
    const before = await host.send({ id: "before", op: "describe" });
    assert.equal(before.error.code, "protocol_error");
    const accepted = await host.send(hello);
    assert.equal(accepted.result.protocol, 2);
    assert.equal(accepted.result.sdk.language, "typescript");
    assert.ok(Array.isArray(accepted.result.features));
    const repeat = await host.send({ ...hello, id: "repeat" });
    assert.equal(repeat.error.code, "protocol_error");
    const old = await host.send({ id: "old", op: "run_node" });
    assert.equal(old.error.code, "unknown_op");
  } finally {
    await host.close();
  }
});

test("protocol mismatch names supported majors", async () => {
  const host = new Host();
  try {
    const frame = await host.send({ ...hello, protocol: 1 });
    assert.equal(frame.error.code, "protocol_mismatch");
    assert.deepEqual(frame.error.details.supported, [2]);
  } finally {
    await host.close();
  }
});

test("known nested fields reject wrong types while additive fields remain readable", async () => {
  for (const run of [
    { git: { sha: {}, branch: "main" } },
    { trigger: { source: 123 } },
    { trigger: { source: "push", pull_request: { number: "1" } } },
    { started_at: "yesterday" },
    { dry_run: null },
    { git: null },
  ]) {
    const host = new Host();
    try {
      await host.send({ ...hello, future: { accepted: true } });
      const result = await host.send({ id: "bad", op: "plan", pipeline: "absent", args: {}, run: { run_id: "r", pipeline: "p", ...run } });
      assert.equal(result.error.code, "bad_request");
    } finally {
      await host.close();
    }
  }
});

test("uncorrelatable or repeated request IDs close the protocol", async () => {
  const malformed = new Host();
  const malformedResult = assert.rejects(malformed.done, /nonempty string id/);
  malformed.input.write("{}\n");
  await malformedResult;
  const duplicate = new Host();
  await duplicate.send(hello);
  const duplicateResult = assert.rejects(duplicate.done, /duplicate request id/);
  duplicate.input.write(JSON.stringify(hello) + "\n");
  await duplicateResult;
});
