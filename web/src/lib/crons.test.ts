import { describe, it } from "node:test";
import assert from "node:assert/strict";
import type { CronHealth, CronSchedule } from "./api";
import {
  declaredVsEffective,
  fmtArgs,
  fmtCatchUp,
  fmtOverlap,
  healthBanner,
  isOverridden,
  lockBadge,
  lockNote,
  outcomeLabel,
  outcomeTone,
  overrideMarker,
  partitionUndeclared,
  scheduleCounts,
  shortRef,
  sortSchedules,
  stateTone,
} from "./crons";

function schedule(over: Partial<CronSchedule> = {}): CronSchedule {
  return {
    id: "crn_0123456789ab",
    name: "dotfiles/nightly-vault-sweep",
    schedule_name: "default",
    repo_path: "/home/me/code/dotfiles",
    pipeline: "nightly-vault-sweep",
    where: "local",
    cron: "0 3 * * *",
    tz: "America/Denver",
    overlap: "skip",
    catch_up_ns: 3_600_000_000_000,
    args: { depth: "deep" },
    lock: { ref: "", binary: "", digest: "", state: "follows" },
    override: { fields: [], stale: false, set_at: "" },
    effective: {
      cron: "0 3 * * *",
      tz: "America/Denver",
      overlap: "skip",
      catch_up_ns: 3_600_000_000_000,
      args: { depth: "deep" },
    },
    paused: false,
    declared: true,
    state: "armed",
    state_detail: "",
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
    locked: 0,
    following: 3,
    ahead: 0,
    missing_binary: 0,
    stale_override: 0,
    detail: "3 schedules armed on this host",
    remedy: "",
    ...over,
  };
}

const PIN_REMEDY =
  "re-run `sparkwing crons install` to pin the checkout as it stands";

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
      health({ timer: { ...health().timer, installed: false, foreign: true } }),
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
      scheduleCounts(health({ paused: 0, undeclared: 0, following: 0 })),
      "3 declared · 2 armed",
    );
    assert.equal(
      scheduleCounts(health({ paused: 1, undeclared: 2, following: 0 })),
      "3 declared · 2 armed · 1 paused · 2 undeclared",
    );
  });

  it("counts what is pinned, what follows the checkout and what has drifted", () => {
    assert.equal(
      scheduleCounts(
        health({ paused: 0, undeclared: 0, locked: 2, following: 1, ahead: 1 }),
      ),
      "3 declared · 2 armed · 2 locked · 1 following · 1 ahead",
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

describe("lockBadge", () => {
  it("carries the abbreviated commit on a clean pin", () => {
    const badge = lockBadge({
      ref: "abc1234def5678",
      binary: "/home/me/.sparkwing/crons/crn_1/pipeline",
      digest: "sha256:aa",
      state: "pinned",
    });
    assert.equal(badge.tone, "ok");
    assert.equal(badge.label, "pinned abc1234");
  });

  it("leads with the drift and tones each state", () => {
    const lock = { ref: "abc1234def5678", binary: "/p", digest: "d", state: "ahead" as const };
    assert.deepEqual(lockBadge(lock), { tone: "warning", label: "ahead" });
    assert.deepEqual(lockBadge({ ...lock, state: "dirty" }), {
      tone: "warning",
      label: "dirty",
    });
    assert.deepEqual(lockBadge({ ...lock, state: "missing" }), {
      tone: "danger",
      label: "missing",
    });
  });

  it("calls an unpinned schedule follows, whatever it carries", () => {
    assert.deepEqual(
      lockBadge({ ref: "", binary: "", digest: "", state: "follows" }),
      { tone: "muted", label: "follows" },
    );
    assert.deepEqual(lockBadge(null), { tone: "muted", label: "follows" });
  });

  it("names a pin whose ref the server did not send", () => {
    assert.equal(
      lockBadge({ ref: "", binary: "/p", digest: "d", state: "pinned" }).label,
      "pinned",
    );
  });
});

describe("shortRef", () => {
  it("abbreviates to seven characters and leaves shorter refs alone", () => {
    assert.equal(shortRef("abc1234def5678"), "abc1234");
    assert.equal(shortRef("abc12"), "abc12");
    assert.equal(shortRef(""), "");
  });
});

describe("lockNote", () => {
  it("says what each state runs, and what to run when it wants attention", () => {
    assert.match(lockNote({ ref: "", binary: "", digest: "", state: "follows" }), /compiles the checkout/);
    const pinned = { ref: "abc1234", binary: "/p", digest: "d", state: "pinned" as const };
    assert.match(lockNote(pinned), /runs the pinned binary/);
    assert.doesNotMatch(lockNote(pinned), /crons install/);
    assert.match(lockNote({ ...pinned, state: "ahead" }), /newer commits[\s\S]*crons install/);
    assert.match(lockNote({ ...pinned, state: "dirty" }), /uncommitted edits[\s\S]*crons install/);
    assert.match(lockNote({ ...pinned, state: "missing" }), /fires nothing[\s\S]*crons install/);
  });
});

describe("fmtArgs", () => {
  it("renders the flags a launch passes, sorted by key", () => {
    assert.equal(fmtArgs({ depth: "deep", "dry-run": "true" }), "--depth=deep --dry-run=true");
    assert.equal(fmtArgs({ b: "2", a: "1" }), "--a=1 --b=2");
    assert.equal(fmtArgs({}), "-");
    assert.equal(fmtArgs(null), "-");
  });
});

describe("overrideMarker", () => {
  it("returns nothing for a schedule running what its repo declares", () => {
    assert.equal(overrideMarker(schedule()), null);
  });

  it("names the overridden fields", () => {
    const marker = overrideMarker(
      schedule({ override: { fields: ["cron", "args"], stale: false, set_at: "2026-09-08T20:00:00Z" } }),
    );
    assert.equal(marker?.label, "override");
    assert.equal(marker?.tone, "muted");
    assert.match(marker!.title, /overrides cron, args/);
  });

  it("warns about an override whose declaration has moved under it", () => {
    const marker = overrideMarker(
      schedule({ override: { fields: ["cron"], stale: true, set_at: "2026-09-08T20:00:00Z" } }),
    );
    assert.equal(marker?.tone, "warning");
    assert.match(marker!.title, /declaration that has changed/);
  });

  it("reads one field at a time", () => {
    const s = schedule({ override: { fields: ["cron"], stale: false, set_at: "" } });
    assert.equal(isOverridden(s, "cron"), true);
    assert.equal(isOverridden(s, "tz"), false);
  });
});

describe("declaredVsEffective", () => {
  it("pairs every overridable field with what this host runs", () => {
    const rows = declaredVsEffective(schedule());
    assert.deepEqual(
      rows.map((r) => r.field),
      ["cron", "tz", "overlap", "catch_up", "args"],
    );
    assert.deepEqual(
      rows.map((r) => r.overridden),
      [false, false, false, false, false],
    );
    const cron = rows[0];
    assert.equal(cron.declared, "0 3 * * *");
    assert.equal(cron.effective, "0 3 * * *");
  });

  it("marks the overridden rows and renders each field in its own units", () => {
    const rows = declaredVsEffective(
      schedule({
        override: { fields: ["cron", "catch_up", "args"], stale: true, set_at: "2026-09-08T20:00:00Z" },
        effective: {
          cron: "0 5 * * *",
          tz: "America/Denver",
          overlap: "skip",
          catch_up_ns: 120_000_000_000,
          args: { depth: "shallow", "dry-run": "true" },
        },
      }),
    );
    const by = Object.fromEntries(rows.map((r) => [r.field, r]));
    assert.equal(by.cron.effective, "0 5 * * *");
    assert.equal(by.cron.overridden, true);
    assert.equal(by.tz.overridden, false);
    assert.equal(by.overlap.declared, "skip if running");
    assert.equal(by.catch_up.declared, "1h");
    assert.equal(by.catch_up.effective, "2m");
    assert.equal(by.args.declared, "--depth=deep");
    assert.equal(by.args.effective, "--depth=shallow --dry-run=true");
    assert.equal(by.args.overridden, true);
  });

  it("labels the catch-up row the way the detail pane reads it", () => {
    assert.deepEqual(
      declaredVsEffective(schedule()).map((r) => r.label),
      ["cron", "tz", "overlap", "catch-up", "args"],
    );
  });
});

describe("healthBanner drift", () => {
  it("calls a gone pinned binary a warning and carries the server's remedy", () => {
    const banner = healthBanner(
      health({ locked: 1, following: 2, missing_binary: 1, remedy: `1 pinned binary/binaries are gone; ${PIN_REMEDY}` }),
    );
    assert.equal(banner.tone, "warning");
    assert.match(banner.headline, /pinned pipeline binary is gone/);
    assert.match(banner.remedy, /crons install/);
  });

  it("warns when the checkout has moved past a pin", () => {
    const one = healthBanner(
      health({ locked: 2, ahead: 1, remedy: `1 schedule(s) are pinned behind the checkout; ${PIN_REMEDY}` }),
    );
    assert.equal(one.tone, "warning");
    assert.match(one.headline, /moved past a pin/);
    assert.match(one.remedy, /pin the checkout as it stands/);
    const many = healthBanner(health({ locked: 3, ahead: 2 }));
    assert.match(many.headline, /moved past 2 pins/);
  });

  it("prefers the gone binary over the pin the checkout has passed", () => {
    const banner = healthBanner(health({ missing_binary: 1, ahead: 2, stale_override: 1 }));
    assert.match(banner.headline, /binary is gone/);
  });

  it("names the schedule whose override went stale", () => {
    const rows = [
      schedule({ id: "a", name: "dotfiles/nightly", override: { fields: ["cron"], stale: true, set_at: "" } }),
      schedule({ id: "b", name: "sparkwing/bench" }),
    ];
    const banner = healthBanner(health({ stale_override: 1 }), rows);
    assert.equal(banner.tone, "warning");
    assert.match(banner.headline, /dotfiles\/nightly/);
    assert.match(banner.headline, /declaration that has changed/);
  });

  it("cuts a long list of stale overrides to a count", () => {
    const rows = ["one", "two", "three", "four"].map((n) =>
      schedule({ id: n, name: `repo/${n}`, override: { fields: ["cron"], stale: true, set_at: "" } }),
    );
    const banner = healthBanner(health({ stale_override: 4 }), rows);
    assert.match(banner.headline, /repo\/one, repo\/two and 2 more/);
  });

  it("falls back to a count when it has no rows to name", () => {
    const banner = healthBanner(health({ stale_override: 2 }));
    assert.match(banner.headline, /^2 overrides were set/);
  });

  it("leaves a host with no drift alone", () => {
    const banner = healthBanner(health({ locked: 3, following: 0 }));
    assert.equal(banner.tone, "ok");
  });

  it("prefers a stopped tick over any pin drift it hides", () => {
    const banner = healthBanner(health({ tick_stale: true, missing_binary: 1 }));
    assert.match(banner.headline, /ticks have stopped/);
  });
});
