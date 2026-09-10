import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  activeFilterCount,
  buildGroupsFromState,
  clearAllFilters,
  computeOptions,
  createFilterCtx,
  runMatchesFilter,
  type RunFilterState,
} from "./RunFilters";
import type { Run } from "@/lib/api";

function filterState(
  pipelines: string[],
  setPipelines: (value: string[]) => void,
): RunFilterState {
  const setList = (value: string[]) => void value;
  const setText = (value: string) => void value;
  return {
    filterTrigger: [],
    setFilterTrigger: setList,
    excludeTrigger: [],
    setExcludeTrigger: setList,
    filterStatus: [],
    setFilterStatus: setList,
    excludeStatus: [],
    setExcludeStatus: setList,
    filterRepo: [],
    setFilterRepo: setList,
    excludeRepo: [],
    setExcludeRepo: setList,
    filterPipeline: pipelines,
    setFilterPipeline: setPipelines,
    excludePipeline: [],
    setExcludePipeline: setList,
    filterBranch: [],
    setFilterBranch: setList,
    excludeBranch: [],
    setExcludeBranch: setList,
    filterCommit: [],
    setFilterCommit: setList,
    excludeCommit: [],
    setExcludeCommit: setList,
    filterTag: [],
    setFilterTag: setList,
    excludeTag: [],
    setExcludeTag: setList,
    startedAfter: "",
    setStartedAfter: setText,
    startedBefore: "",
    setStartedBefore: setText,
    finishedAfter: "",
    setFinishedAfter: setText,
    finishedBefore: "",
    setFinishedBefore: setText,
    filterText: "",
    setFilterText: setText,
  };
}

describe("createFilterCtx", () => {
  it("renders and toggles from the newly navigated filter state", () => {
    let update: string[] | undefined;
    const initial = createFilterCtx(filterState([], () => {}));
    const navigated = createFilterCtx(
      filterState(["deploy-production"], (value) => {
        update = value;
      }),
    );

    assert.notEqual(initial, navigated);
    assert.equal(navigated.isIncluded("pipeline", "deploy-production"), true);
    navigated.toggle("pipeline", "deploy-production", "include");
    assert.deepEqual(update, []);
  });
});


describe("trigger filters", () => {
  const run = (trigger?: string): Run => ({
    id: `run-${trigger || "unknown"}`,
    pipeline: "checks",
    status: "success",
    started_at: "2026-09-09T12:00:00Z",
    trigger_source: trigger,
  });

  it("includes selected sources, excludes denied sources, and combines with other facets", () => {
    const state = filterState([], () => {});
    state.filterTrigger = ["cron", "manual"];
    state.excludeTrigger = ["manual"];
    assert.equal(runMatchesFilter(run("cron"), state, {}), true);
    assert.equal(runMatchesFilter(run("manual"), state, {}), false);
    assert.equal(runMatchesFilter(run("push"), state, {}), false);
    assert.equal(runMatchesFilter(run(), state, {}), false);
    state.filterStatus = ["failed"];
    assert.equal(runMatchesFilter(run("cron"), state, {}), false);
    state.filterStatus = [];
    state.filterTrigger = [];
    assert.equal(runMatchesFilter(run(), state, {}), true);
    assert.equal(runMatchesFilter(run("manual"), state, {}), false);
  });

  it("offers sorted distinct observed trigger sources in the filter bar", () => {
    const state = filterState([], () => {});
    const options = computeOptions([run("push"), run(), run("cron"), run("cron")], {});
    const group = buildGroupsFromState(state, options).find((g) => g.key === "trigger");
    assert.ok(group);
    assert.deepEqual(group.options, ["cron", "push"]);
    assert.equal(group.label, "TRIGGER");
  });

  it("moves a source from excluded to included through its badge menu", () => {
    const state = filterState([], () => {});
    state.excludeTrigger = ["cron", "push"];
    state.setFilterTrigger = (value) => { state.filterTrigger = value; };
    state.setExcludeTrigger = (value) => { state.excludeTrigger = value; };
    const ctx = createFilterCtx(state);
    assert.equal(ctx.isExcluded("trigger", "cron"), true);
    ctx.toggle("trigger", "cron", "include");
    assert.deepEqual(state.filterTrigger, ["cron"]);
    assert.deepEqual(state.excludeTrigger, ["push"]);
    assert.equal(ctx.isIncluded("trigger", "cron"), true);
    ctx.toggle("trigger", "cron", "exclude");
    assert.deepEqual(state.filterTrigger, []);
    assert.deepEqual(state.excludeTrigger, ["push", "cron"]);
  });

  it("counts and clears both trigger selections", () => {
    const state = filterState([], () => {});
    state.filterTrigger = ["cron"];
    state.excludeTrigger = ["push"];
    state.setFilterTrigger = (value) => { state.filterTrigger = value; };
    state.setExcludeTrigger = (value) => { state.excludeTrigger = value; };
    assert.equal(activeFilterCount(state), 2);
    clearAllFilters(state);
    assert.equal(activeFilterCount(state), 0);
    assert.deepEqual(state.filterTrigger, []);
    assert.deepEqual(state.excludeTrigger, []);
  });
});
