
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { fmtDateTime, fmtUntil } from "./timeFormat";

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

describe("fmtUntil", () => {
  const now = Date.parse("2026-09-08T12:00:00Z");

  function ahead(ms: number): string {
    return new Date(now + ms).toISOString();
  }

  it("counts seconds under a minute", () => {
    assert.equal(fmtUntil(ahead(45_000), now), "in 45s");
  });

  it("counts whole minutes under an hour", () => {
    assert.equal(fmtUntil(ahead(12 * 60_000 + 30_000), now), "in 12m");
  });

  it("pairs hours with minutes under a day", () => {
    assert.equal(fmtUntil(ahead(4 * 3_600_000 + 12 * 60_000), now), "in 4h 12m");
    assert.equal(fmtUntil(ahead(4 * 3_600_000), now), "in 4h");
  });

  it("drops to whole days past 24 hours", () => {
    assert.equal(fmtUntil(ahead(3 * 86_400_000 + 5 * 3_600_000), now), "in 3d");
  });

  it("reads an elapsed instant as now", () => {
    assert.equal(fmtUntil(ahead(0), now), "now");
    assert.equal(fmtUntil(ahead(-90_000), now), "now");
  });

  it("degrades instead of throwing on junk", () => {
    assert.equal(fmtUntil("", now), "--");
    assert.equal(fmtUntil(null, now), "--");
    assert.equal(fmtUntil("not-a-date", now), "--");
  });

  it("defaults to the wall clock", () => {
    const target = new Date(Date.now() + 3_630_000).toISOString();
    assert.equal(fmtUntil(target), "in 1h");
  });
});
