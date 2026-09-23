import { type Capabilities, asList, send } from "./teams";

export interface GitHubAppInstallation {
  installation_id: number;
  account_id: number;
  account_login: string;
  account_type: "User" | "Organization";
  suspended: boolean;
  connected_by: string;
  // Unix seconds, as the controller reports them.
  created_at: number;
  updated_at: number;
  manage_url: string;
}

export interface GitHubApp {
  slug: string;
  installations: GitHubAppInstallation[];
}

export interface GitHubAppRepository {
  repository_id: number;
  full_name: string;
  private: boolean;
}

export interface GitHubAppSubscription {
  repository: string;
  repository_id: number;
  installation_id: number;
  pipeline: string;
  push: boolean;
  pull_request: boolean;
  created_by: string;
  created_at: number;
}

export interface SubscriptionDraft {
  repository: string;
  pipeline: string;
  push: boolean;
  pull_request: boolean;
}

export const emptySubscriptionDraft: SubscriptionDraft = {
  repository: "",
  pipeline: "",
  push: true,
  pull_request: true,
};

// The dashboard server owns the connect flow, because it keeps the state and
// verifier in a cookie the page cannot read; the page posts a form to it.
export const connectFormAction = "/github/app/connect";

export const pullRequestHelp =
  "Runs when a pull request is opened, updated or reopened. Pull requests from forks aren't run.";

export function githubAppEnabled(caps: Capabilities | null): boolean {
  return Boolean(caps?.github_app?.slug);
}

const seg = encodeURIComponent;

export async function getGitHubApp(): Promise<GitHubApp> {
  const res = await send("GET", "/api/v1/team/github-app", "Load GitHub");
  const body = (await res.json()) as Partial<GitHubApp>;
  return {
    slug: body.slug ?? "",
    installations: asList<GitHubAppInstallation>(body, "installations"),
  };
}

export async function disconnectInstallation(id: number): Promise<void> {
  await send(
    "DELETE",
    `/api/v1/team/github-app/installations/${seg(String(id))}`,
    "Disconnect",
  );
}

export async function listInstallationRepositories(
  id: number,
): Promise<GitHubAppRepository[]> {
  const res = await send(
    "GET",
    `/api/v1/team/github-app/installations/${seg(String(id))}/repositories`,
    "List repositories",
  );
  return asList<GitHubAppRepository>(await res.json(), "repositories");
}

export async function listSubscriptions(): Promise<GitHubAppSubscription[]> {
  const res = await send(
    "GET",
    "/api/v1/team/github-app/triggers",
    "List subscriptions",
  );
  return asList<GitHubAppSubscription>(await res.json(), "triggers");
}

export async function putSubscription(
  draft: SubscriptionDraft,
): Promise<GitHubAppSubscription> {
  const res = await send(
    "PUT",
    "/api/v1/team/github-app/triggers",
    "Save subscription",
    subscriptionRequest(draft),
  );
  return (await res.json()) as GitHubAppSubscription;
}

export async function deleteSubscription(
  repositoryID: number,
  pipeline: string,
): Promise<void> {
  const query = new URLSearchParams({
    repository_id: String(repositoryID),
    pipeline,
  });
  await send(
    "DELETE",
    `/api/v1/team/github-app/triggers?${query.toString()}`,
    "Remove subscription",
  );
}

export function subscriptionRequest(
  draft: SubscriptionDraft,
): SubscriptionDraft {
  return {
    repository: draft.repository.trim(),
    pipeline: draft.pipeline.trim(),
    push: draft.push,
    pull_request: draft.pull_request,
  };
}

// The controller checks the same rules; the form says so before the save.
export function subscriptionProblem(draft: SubscriptionDraft): string | null {
  const req = subscriptionRequest(draft);
  if (!req.repository) return "Choose a repository.";
  if (!req.pipeline) return "Name the pipeline to run.";
  if (encodeURIComponent(req.pipeline) !== req.pipeline) {
    return "Use a pipeline name without spaces or slashes.";
  }
  if (!req.push && !req.pull_request) {
    return "Run on pushes, pull requests or both.";
  }
  return null;
}

export function draftFromSubscription(
  sub: GitHubAppSubscription,
): SubscriptionDraft {
  return {
    repository: sub.repository,
    pipeline: sub.pipeline,
    push: sub.push,
    pull_request: sub.pull_request,
  };
}

// Repositories an installation covers, in one sorted list for the form's
// picker, skipping installations GitHub suspended because the controller
// refuses a subscription to them.
export function subscribableRepositories(
  installations: GitHubAppInstallation[],
  repositories: Record<number, GitHubAppRepository[] | undefined>,
): string[] {
  const names = new Set<string>();
  for (const inst of installations) {
    if (inst.suspended) continue;
    for (const repo of repositories[inst.installation_id] ?? []) {
      names.add(repo.full_name);
    }
  }
  return [...names].sort((a, b) => a.localeCompare(b));
}

export function accountTypeLabel(type: string): string {
  return type === "Organization" ? "Organization" : "Personal account";
}

// The dashboard server returns here after connecting with the account it
// bound; the page shows it once and drops it from the address bar.
export function connectedAccount(search: string): string | null {
  const value = new URLSearchParams(search).get("connected");
  return value ? value : null;
}
