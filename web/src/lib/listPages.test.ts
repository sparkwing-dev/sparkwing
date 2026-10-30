import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { listPages } from "./listPages";

describe("complete inventory reads", () => {
  it("drains cursors, preserves filters and combines related page collections", async () => {
    const calls: string[] = [];
    const res = await listPages("/api/v1/schedules?all=true", ["schedules", "upcoming"], async (path) => {
      calls.push(path);
      return Response.json(calls.length === 1
        ? { health: { schedules: 2 }, schedules: [{ id: "a" }], upcoming: [{ schedule: "a" }], next_cursor: "next/a+b", truncated: true }
        : { health: { schedules: 2 }, schedules: [{ id: "b" }], upcoming: [{ schedule: "b" }], next_cursor: "", truncated: false });
    });
    assert.deepEqual(calls, ["/api/v1/schedules?all=true", "/api/v1/schedules?all=true&cursor=next%2Fa%2Bb"]);
    assert.deepEqual(await res.json(), {
      health: { schedules: 2 },
      schedules: [{ id: "a" }, { id: "b" }],
      upcoming: [{ schedule: "a" }, { schedule: "b" }],
    });
  });

  it("returns a later page's refusal instead of a partial inventory", async () => {
    const refusal = Response.json({ error: { message: "access removed" } }, { status: 403 });
    let calls = 0;
    const res = await listPages("/api/v1/secrets", ["secrets"], async () => ++calls === 1
      ? Response.json({ secrets: [{ name: "A" }], next_cursor: "next", truncated: true })
      : refusal);
    assert.equal(res, refusal);
    assert.equal(calls, 2);
  });

  it("refuses cursor cycles and inconsistent pagination", async () => {
    for (const page of [
      { members: [], next_cursor: "loop", truncated: true },
      { members: [], next_cursor: "", truncated: true },
      { members: [], next_cursor: "unexpected", truncated: false },
    ]) {
      let calls = 0;
      await assert.rejects(listPages("/api/v1/team/members", ["members"], async () => {
        calls++;
        return Response.json(page);
      }), /inconsistent collection pagination/);
      assert.ok(calls <= 2);
    }
  });
});
