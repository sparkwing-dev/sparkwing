import type { Run } from "./api";

export interface FailedPipeline {
  key: string;
  repo: string;
  pipeline: string;
  branch: string;
  latest: Run;
  runs: Run[];
}

function repoIdentity(run: Run): string {
  const raw = (run.github_owner && run.github_repo
    ? `${run.github_owner}/${run.github_repo}`
    : run.repo_url || run.repo || run.github_repo || "unknown");
  let path = raw.replace(/^git@[^:]+:/, "");
  if (/^https?:\/\//.test(path)) {
    path = new URL(path).pathname;
  }
  return path.replace(/^\/+|\/+$/g, "").replace(/\.git$/i, "").toLowerCase();
}

export function latestFailedPipelines(
  runs: Run[],
  includeFeatureBranches: boolean,
): FailedPipeline[] {
  const fullByShort = new Map<string, Set<string>>();
  for (const run of runs) {
    const repo = repoIdentity(run);
    if (!repo.includes("/")) continue;
    const short = repo.slice(repo.lastIndexOf("/") + 1);
    const full = fullByShort.get(short) || new Set<string>();
    full.add(repo);
    fullByShort.set(short, full);
  }

  const groups = new Map<string, FailedPipeline>();
  for (const run of runs) {
    const branch = run.git_branch || "";
    const defaultBranch = branch === "main" || branch === "master";
    if (!defaultBranch && (!includeFeatureBranches || !branch)) continue;
    let repo = repoIdentity(run);
    if (!repo.includes("/")) {
      const matches = fullByShort.get(repo);
      if (matches?.size === 1) repo = [...matches][0];
    }
    const branchKey = defaultBranch ? "default" : branch;
    const key = `${repo}\0${run.pipeline}\0${branchKey}`;
    const group = groups.get(key);
    if (group) {
      group.runs.push(run);
    } else {
      groups.set(key, {
        key, repo, pipeline: run.pipeline, branch: defaultBranch ? "" : branch,
        latest: run, runs: [run],
      });
    }
  }
  return [...groups.values()]
    .map((group) => {
      group.runs.sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at));
      group.latest = group.runs.find((run) =>
        run.status === "success" || run.status === "failed" || run.status === "cancelled",
      ) || group.runs[0];
      return group;
    })
    .filter((group) => group.latest.status === "failed")
    .sort((a, b) => Date.parse(b.latest.started_at) - Date.parse(a.latest.started_at));
}
