import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Run } from "./api";
import { latestFailedPipelines, recentPipelineTriage } from "./homeTriage";

function run(id: string, repo: string, branch: string, status: string, minute: number): Run {
  return {
    id, repo, git_branch: branch, pipeline: "pre-push", status,
    started_at: new Date(Date.UTC(2026, 8, 24, 18, minute)).toISOString(),
  };
}

describe("Home latest failed pipelines", () => {
  it("shows a recovery only when the previous finished result failed", () => {
    const rows = [
      run("old-green", "owner/app", "main", "success", 1),
      run("red", "app", "main", "failed", 2),
      run("green", "owner/app", "main", "success", 3),
      run("retry", "owner/app", "main", "running", 4),
      run("cancelled", "owner/app", "main", "cancelled", 5),
      run("feature-red", "owner/app", "feature/demo", "failed", 6),
      run("feature-green", "owner/app", "feature/demo", "success", 7),
      run("steady-green", "owner/other", "main", "success", 8),
    ];
    assert.deepEqual(recentPipelineTriage(rows, false).recovered.map((r) => r.latest.id), ["green"]);
    assert.deepEqual(recentPipelineTriage(rows, true).recovered.map((r) => r.latest.id), ["feature-green", "green"]);
    assert.deepEqual(recentPipelineTriage([...rows, run("newer-green", "owner/app", "main", "success", 9)], false).recovered, []);
  });

  it("clears a default-branch failure after a newer success and joins a unique repo alias", () => {
    const rows = [
      run("red", "product", "main", "failed", 1),
      run("green", "owner/product", "main", "success", 2),
      run("other-red", "owner/other", "master", "failed", 3),
      run("feature-red", "owner/product", "feature/demo", "failed", 4),
    ];
    assert.deepEqual(latestFailedPipelines(rows, false).map((r) => r.latest.id), ["other-red"]);
    assert.deepEqual(latestFailedPipelines(rows, true).map((r) => r.latest.id), ["feature-red", "other-red"]);
  });

  it("does not merge equal short repo names across different owners", () => {
    const rows = [
      run("a", "alice/app", "main", "failed", 1),
      run("b", "bob/app", "main", "success", 2),
    ];
    assert.deepEqual(latestFailedPipelines(rows, false).map((r) => r.latest.id), ["a"]);
  });

  it("keeps a failed pipeline visible while a newer retry runs, then clears on success", () => {
    const failed = run("failed", "owner/app", "main", "failed", 1);
    const retry = run("retry", "owner/app", "main", "running", 2);
    assert.deepEqual(latestFailedPipelines([failed, retry], false).map((r) => r.latest.id), ["failed"]);
    assert.deepEqual(latestFailedPipelines([failed, { ...retry, status: "cancelled" }], false).map((r) => r.latest.id), ["failed"]);
    assert.deepEqual(latestFailedPipelines([failed, { ...retry, status: "success" }], false), []);
  });

  it("normalizes SSH GitHub URLs and prefers verified owner/repo fields", () => {
    const ssh = { ...run("red", "app", "main", "failed", 1), repo_url: "git@github.com:owner/app.git" };
    const newer = { ...run("green", "app", "main", "success", 2), github_owner: "owner", github_repo: "app" };
    assert.deepEqual(latestFailedPipelines([ssh, newer], false), []);
  });

  it("keeps different Git hosts separate even with the same owner and repo", () => {
    const gitlab = { ...run("gitlab-red", "app", "main", "failed", 1), repo_url: "https://gitlab.com/owner/app.git" };
    const github = { ...run("github-green", "owner/app", "main", "success", 2), repo_url: "https://github.com/owner/app.git" };
    assert.deepEqual(latestFailedPipelines([gitlab, github], false).map((r) => r.latest.id), ["gitlab-red"]);
  });
});
