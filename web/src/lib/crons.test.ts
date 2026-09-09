import { describe, it } from "node:test";
import assert from "node:assert/strict";
import type { CronHealth, CronSchedule } from "./api";
import {
  fmtCatchUp,
  fmtOverlap,
  healthBanner,
  outcomeLabel,
  outcomeTone,
  partitionUndeclared,
  scheduleCounts,
  sortSchedules,
  stateTone,
} from "./crons";

function schedule(over: Partial<CronSchedule> = {}): CronSchedule {
  return {
    id: "crn_0123456789ab",
    name: "dotfiles/nightly-vault-sweep",
    repo_path: "/home/me/code/dotfiles",
    pipeline: "nightly-vault-sweep",
    cron: "0 3 * * *",
    tz: "America/Denver",
    overlap: "skip",
    catch_up_ns: 3_600_000_000_000,
    paused: false,
    declared: true,
    state: "armed",
    armed_at: "2026-09-08T20:00:00Z",
    updated_at: "2026-09-08T23:59:00Z",
    last_fired_at: "2026-09-08T09:00:00Z",
    last_run_id: "run_abc",
    last_outcome: "fired",
    next_due_at: "2026-09-09T09:00:00Z",
    ...over,
  };
}

function health(over: Partial<CronHealth> = {}): CronHealth {
  return {
    timer: {
      installed: true,
      foreign: false,
      enabled: true,
      stale: false,
      path: "/home/me/.config/systemd/user/sparkwing-crons.timer",
      binary: "/home/me/.local/bin/sparkwing",
      detail: "timer enabled; last tick 12s ago",
    },
    last_tick: {
      at: "2026-09-08T23:59:00Z",
      host: "wsl",
      version: "v0.42.0",
      error: "",
    },
    tick_stale: false,
    schedules: 3,
    armed: 2,
    paused: 1,
    undeclared: 0,
    detail: "3 schedules armed on this host",
    ...over,
  };
}

describe("sortSchedules", () => {
  it("orders armed schedules by the next due instant", () => {
    const rows = [
      schedule({ id: "b", name: "b", next_due_at: "2026-09-10T00:00:00Z" }),
      schedule({ id: "a", name: "a", next_due_at: "2026-09-09T00:00:00Z" }),
    ];
    assert.deepEqual(
      sortSchedules(rows).map((s) => s.id),
      ["a", "b"],
    );
  });

  it("sinks paused and undeclared below armed whatever their due instant", () => {
    const rows = [
      schedule({
        id: "undeclared",
        name: "u",
        state: "undeclared",
        declared: false,
        next_due_at: "2026-09-01T00:00:00Z",
      }),
      schedule({
        id: "paused",
        name: "p",
        state: "paused",
        paused: true,
        next_due_at: "2026-09-02T00:00:00Z",
      }),
      schedule({ id: "armed", name: "a", next_due_at: "2026-09-30T00:00:00Z" }),
    ];
    assert.deepEqual(
      sortSchedules(rows).map((s) => s.id),
      ["armed", "paused", "undeclared"],
    );
  });

  it("puts schedules with no due instant last and breaks ties by name", () => {
    const rows = [
      schedule({ id: "second", name: "zeta", next_due_at: null }),
      schedule({ id: "first", name: "alpha", next_due_at: null }),
      schedule({ id: "due", name: "mid", next_due_at: "2026-09-09T00:00:00Z" }),
    ];
    assert.deepEqual(
      sortSchedules(rows).map((s) => s.id),
      ["due", "first", "second"],
    );
  });

  it("leaves the caller's array untouched", () => {
    const rows = [
      schedule({ id: "b", name: "b", next_due_at: "2026-09-10T00:00:00Z" }),
      schedule({ id: "a", name: "a", next_due_at: "2026-09-09T00:00:00Z" }),
    ];
    sortSchedules(rows);
    assert.deepEqual(
      rows.map((s) => s.id),
      ["b", "a"],
    );
  });
});

describe("partitionUndeclared", () => {
  it("splits the undeclared rows out of the sorted list", () => {
    const rows = [
      schedule({ id: "gone", name: "gone", state: "undeclared", declared: false }),
      schedule({ id: "live", name: "live" }),
    ];
    const { listed, undeclared } = partitionUndeclared(rows);
    assert.deepEqual(
      listed.map((s) => s.id),
      ["live"],
    );
    assert.deepEqual(
      undeclared.map((s) => s.id),
      ["gone"],
    );
  });
});

describe("healthBanner", () => {
  it("reports the healthy detail sentence", () => {
    const banner = healthBanner(health());
    assert.equal(banner.tone, "ok");
    assert.equal(banner.headline, "3 schedules armed on this host");
    assert.equal(banner.remedy, "");
  });

  it("counts armed schedules when the server sends no detail", () => {
    const banner = healthBanner(health({ detail: "", armed: 1 }));
    assert.equal(banner.headline, "1 schedule armed on this host.");
  });

  it("calls a missing timer a danger when schedules are armed", () => {
    const banner = healthBanner(
      health({
        timer: { ...health().timer, installed: false },
      }),
    );
    assert.equal(banner.tone, "danger");
    assert.match(banner.headline, /No cron timer is installed/);
    assert.match(banner.remedy, /sparkwing crons install/);
  });

  it("softens a missing timer when nothing is armed", () => {
    const banner = healthBanner(
      health({ armed: 0, timer: { ...health().timer, installed: false } }),
    );
    assert.equal(banner.tone, "warning");
  });

  it("names the foreign unit it found", () => {
    const banner = healthBanner(
      health({ timer: { ...health().timer, foreign: true } }),
    );
    assert.equal(banner.tone, "warning");
    assert.match(banner.headline, /sparkwing did not write it/);
    assert.match(banner.remedy, /sparkwing-crons\.timer/);
  });

  it("names the binary a stale timer should point at", () => {
    const banner = healthBanner(
      health({ timer: { ...health().timer, stale: true } }),
    );
    assert.match(banner.headline, /stale sparkwing binary/);
    assert.match(banner.remedy, /\/home\/me\/\.local\/bin\/sparkwing/);
  });

  it("flags an installed but disabled timer", () => {
    const banner = healthBanner(
      health({ timer: { ...health().timer, enabled: false } }),
    );
    assert.equal(banner.tone, "warning");
    assert.match(banner.headline, /not enabled/);
  });

  it("surfaces the tick error verbatim", () => {
    const banner = healthBanner(
      health({ last_tick: { ...health().last_tick, error: "store locked" } }),
    );
    assert.equal(banner.tone, "danger");
    assert.match(banner.headline, /store locked/);
  });

  it("distinguishes never ticked from tick stale", () => {
    const never = healthBanner(
      health({ last_tick: { ...health().last_tick, at: "" }, tick_stale: true }),
    );
    assert.match(never.headline, /never ticked/);
    const stale = healthBanner(health({ tick_stale: true }));
    assert.match(stale.headline, /ticks have stopped/);
  });

  it("prefers the missing timer over the stale tick it causes", () => {
    const banner = healthBanner(
      health({
        timer: { ...health().timer, installed: false },
        tick_stale: true,
        last_tick: { ...health().last_tick, at: "" },
      }),
    );
    assert.match(banner.headline, /No cron timer is installed/);
  });

  it("treats absent health as a danger", () => {
    const banner = healthBanner(null);
    assert.equal(banner.tone, "danger");
  });
});

describe("scheduleCounts", () => {
  it("omits the zero buckets", () => {
    assert.equal(
      scheduleCounts(health({ paused: 0, undeclared: 0 })),
      "3 declared · 2 armed",
    );
    assert.equal(
      scheduleCounts(health({ paused: 1, undeclared: 2 })),
      "3 declared · 2 armed · 1 paused · 2 undeclared",
    );
  });
});

describe("outcome and state rendering", () => {
  it("shortens the overlap skip outcome and tones each one", () => {
    assert.equal(outcomeLabel("skipped_overlap"), "skipped");
    assert.equal(outcomeLabel("fired"), "fired");
    assert.equal(outcomeLabel(""), "-");
    assert.equal(outcomeTone("fired"), "ok");
    assert.equal(outcomeTone("missed"), "warning");
    assert.equal(outcomeTone("failed"), "danger");
    assert.equal(outcomeTone("skipped_overlap"), "muted");
    assert.equal(outcomeTone(""), "muted");
  });

  it("tones the three schedule states", () => {
    assert.equal(stateTone("armed"), "ok");
    assert.equal(stateTone("paused"), "warning");
    assert.equal(stateTone("undeclared"), "muted");
  });
});

describe("fmtOverlap", () => {
  it("spells out the policy", () => {
    assert.equal(fmtOverlap("skip"), "skip if running");
    assert.equal(fmtOverlap("queue"), "queue behind running");
    assert.equal(fmtOverlap(""), "-");
  });
});

describe("fmtCatchUp", () => {
  it("scales nanoseconds to the largest unit", () => {
    assert.equal(fmtCatchUp(3_600_000_000_000), "1h");
    assert.equal(fmtCatchUp(120_000_000_000), "2m");
    assert.equal(fmtCatchUp(90 * 60 * 1_000_000_000), "1h 30m");
    assert.equal(fmtCatchUp(45_000_000_000), "45s");
    assert.equal(fmtCatchUp(26 * 3600 * 1_000_000_000), "1d 2h");
    assert.equal(fmtCatchUp(0), "off");
  });
});
