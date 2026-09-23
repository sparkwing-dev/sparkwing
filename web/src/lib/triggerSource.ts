// A team's runs are fetched by the team's own runners straight from the
// repository, so a run started from the dashboard names the repository and
// branch itself. The controller holds the same rules; checking here lets the
// form say what is wrong before the request.

export interface TriggerGit {
  repo_url: string;
  branch: string;
  repo: string;
  github_owner: string;
  github_repo: string;
}

export type Parsed<T> = { ok: true; value: T } | { ok: false; problem: string };

const ownerPattern = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$/;
const namePattern = /^[A-Za-z0-9._-]{1,100}$/;

export const repositoryExample = "https://github.com/acme/app";

// parseGitHubRepository reads https://github.com/owner/name, with or without
// the scheme, a trailing slash or a .git suffix.
export function parseGitHubRepository(
  input: string,
): Parsed<{ url: string; owner: string; name: string }> {
  const trimmed = input.trim();
  if (!trimmed) return { ok: false, problem: "Name the repository to run." };
  const hostPath = trimmed.replace(/^https:\/\//i, "");
  const parts = hostPath
    .replace(/\/+$/, "")
    .replace(/\.git$/i, "")
    .split("/");
  if (parts.length !== 3 || parts[0].toLowerCase() !== "github.com") {
    return {
      ok: false,
      problem: `Use a GitHub repository URL, such as ${repositoryExample}.`,
    };
  }
  const [, owner, name] = parts;
  if (!ownerPattern.test(owner)) {
    return { ok: false, problem: `"${owner}" is not a GitHub owner.` };
  }
  if (!namePattern.test(name) || name === "." || name === "..") {
    return { ok: false, problem: `"${name}" is not a GitHub repository name.` };
  }
  return {
    ok: true,
    value: { url: `https://github.com/${owner}/${name}`, owner, name },
  };
}

// branchProblem follows git check-ref-format --branch, which the runner applies
// before it fetches the branch.
export function branchProblem(branch: string): string | null {
  const b = branch.trim();
  if (!b) return "Name the branch to run.";
  if (
    b.startsWith("-") ||
    b.startsWith("/") ||
    b.endsWith("/") ||
    b.endsWith(".") ||
    b.endsWith(".lock") ||
    b.includes("..") ||
    b.includes("//") ||
    b.includes("@{") ||
    b === "@" ||
    /[\s~^:?*[\\\x00-\x1f\x7f]/.test(b) ||
    b.split("/").some((seg) => seg.startsWith("."))
  ) {
    return `"${b}" is not a branch name.`;
  }
  return null;
}

export function triggerGit(
  repository: string,
  branch: string,
): Parsed<TriggerGit> {
  const repo = parseGitHubRepository(repository);
  if (!repo.ok) return repo;
  const problem = branchProblem(branch);
  if (problem) return { ok: false, problem };
  return {
    ok: true,
    value: {
      repo_url: repo.value.url,
      branch: branch.trim(),
      repo: repo.value.name,
      github_owner: repo.value.owner,
      github_repo: repo.value.name,
    },
  };
}
