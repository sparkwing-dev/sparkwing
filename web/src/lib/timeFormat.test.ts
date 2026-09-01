
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { fmtDateTime } from "./timeFormat";

function localISO(
  year: number,
  month: number,
  day: number,
  hour: number,
  minute: number,
): string {
  const d = new Date(year, month - 1, day, hour, minute);
  return d.toISOString();
}

describe("fmtDateTime", () => {
  const thisYear = new Date().getFullYear();

  it("names the day", () => {
    const out = fmtDateTime(localISO(thisYear, 5, 31, 23, 53));
    assert.match(out, /May 31/);
    assert.match(out, /11:53/);
  });

  it("omits the year for the current year", () => {
    const out = fmtDateTime(localISO(thisYear, 5, 31, 23, 53));
    assert.ok(
      !out.includes(String(thisYear)),
      `expected no year in ${JSON.stringify(out)}`,
    );
  });

  it("spells out the year for other years", () => {
    const out = fmtDateTime(localISO(thisYear - 2, 5, 31, 23, 53));
    assert.ok(
      out.includes(String(thisYear - 2)),
      `expected year in ${JSON.stringify(out)}`,
    );
  });

  it("adds seconds only when asked", () => {
    const ts = localISO(thisYear, 5, 31, 23, 53);
    assert.match(fmtDateTime(ts, { seconds: true }), /11:53:00/);
    assert.doesNotMatch(fmtDateTime(ts), /11:53:00/);
  });

  it("degrades instead of throwing on junk", () => {
    assert.equal(fmtDateTime(""), "--");
    assert.equal(fmtDateTime("not-a-date"), "not-a-date");
  });
});
