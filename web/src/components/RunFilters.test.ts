import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { createFilterCtx, type RunFilterState } from "./RunFilters";

function filterState(
  pipelines: string[],
  setPipelines: (value: string[]) => void,
): RunFilterState {
  const setList = (value: string[]) => void value;
  const setText = (value: string) => void value;
  return {
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
