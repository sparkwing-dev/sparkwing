import type { Page } from "@playwright/test";

export async function onboardingFixture(page: Page, options: {
  connected?: boolean; enabled?: boolean; bound?: boolean; role?: string;
  status?: string; manualOverride?: boolean; failBinding?: boolean;
  failAutomationOnce?: boolean; push?: boolean;
  noTeam?: boolean; noActiveTeam?: boolean;
} = {}) {
  let hasActiveTeam = !options.noTeam && !options.noActiveTeam;
  let connected = options.connected ?? true;
  let enabled = options.enabled ?? false;
  let bound = options.bound ?? false;
  let runner = enabled ? "github-actions" : "";
  let failAutomation = options.failAutomationOnce;
  let team = { slug: "korey-gam", display_name: "Korey's space", role: options.role ?? "owner" };
  const mutations: string[] = [];
  const permissionRequests: { method: string; path: string; body: unknown }[] = [];
  const requestsWithoutTeam: string[] = [];
  await page.context().addCookies([{ name: "sw_csrf", value: "fixture-csrf", url: "http://127.0.0.1" }]);
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const method = route.request().method();
    if (!hasActiveTeam && path !== "/api/v1/capabilities" && path !== "/api/v1/me" &&
      !(method === "POST" && ["/api/v1/teams", "/api/v1/me/active-team"].includes(path))) {
      requestsWithoutTeam.push(`${method} ${path}`);
      await route.fulfill({ status: 403, json: { error: { code: "forbidden", message: "Permission denied" } } });
      return;
    }
    if (method !== "GET") mutations.push(`${method} ${path}`);
    if ((method === "POST" && path === "/api/v1/runners/github") || (method === "PUT" && path === "/api/v1/team/github-app/automation")) {
      permissionRequests.push({ method, path, body: route.request().postDataJSON() });
    }
    let json: unknown = {};
    if (path === "/api/v1/capabilities") json = { mode: "cluster", teams: { enabled: true }, github_app: { slug: "test-app" } };
    else if (path === "/api/v1/me") json = { user: { id: "u1", email: "korey@example.test", name: "Korey" }, active_team: hasActiveTeam ? team : null, waitlisted: false, memberships: options.noTeam && !hasActiveTeam ? [] : [team], invitations: [] };
    else if (path === "/api/v1/teams" && method === "POST") { team = { ...team, ...route.request().postDataJSON() }; hasActiveTeam = true; json = team; }
    else if (path === "/api/v1/me/active-team" && method === "POST") { hasActiveTeam = true; }
    else if (path === "/api/v1/team/github-app") json = { slug: "test-app", installations: connected ? [{ installation_id: 7, account_id: 8, account_login: "acme", account_type: "Organization", suspended: false, manage_url: "https://github.com/settings/installations/7" }] : [] };
    else if (path.endsWith("/repositories")) json = { repositories: [{ repository_id: 9, full_name: "acme/app", private: true }, { repository_id: 10, full_name: "acme/other", private: true }] };
    else if (path === "/api/v1/team/github-app/automation") {
      if (method === "PUT" && failAutomation) {
        failAutomation = false;
        await route.fulfill({ status: 503, json: { error: "unavailable" } });
        return;
      }
      if (method === "PUT") { enabled = true; runner = route.request().postDataJSON().runner; }
      if (method === "DELETE") enabled = false;
      const repository = new URL(route.request().url()).searchParams.get("repository") ?? "acme/app";
      json = { repository, repository_id: repository === "acme/other" ? 10 : 9, installation_id: 7, enabled, runner, default_branch: "main", source_sha: "abc123", status: options.status ?? "ready", pipelines: [{ pipeline: "release-frontend-ci", push: options.push ?? true, branches: ["main"], pull_request: options.push === false, actions: [], base_branches: [], manual_override: options.manualOverride ?? false }], skipped: [] };
    } else if (path.startsWith("/api/v1/runners/github")) {
      if (method === "POST" && options.failBinding) { await route.fulfill({ status: 503, json: { error: "unavailable" } }); return; }
      if (method === "POST") bound = true;
      if (method === "DELETE") bound = false;
      json = { bindings: bound ? [{ repository: "acme/app", repository_id: 9, repository_owner_id: 8 }] : [], workflow: "name: Sparkwing\non: push\n# Use your existing workflow if it already starts a Sparkwing runner.\n# --team korey-gam\n" };
    } else if (path === "/api/v1/team/settings") json = { members_can_add_machines: false, max_children_per_root: 50 };
    else if (path === "/api/v1/team/members") json = { members: [{ user_id: "u1", email: "korey@example.test", name: "Korey", role: "owner" }] };
    else if (path === "/api/v1/auth/device/lookup") json = { user_code: "BCDF-GHJK", kind: "member", machine: "Korey's desktop", team: "korey-gam", grants: ["runs.read", "runs.write"], created_at: "2026-11-04T00:00:00Z", expires_at: "2099-01-01T00:00:00Z" };
    else json = { triggers: [], extra_repos: [], deliveries: [], tokens: [], invitations: [], approvals: [] };
    await route.fulfill({ json });
  });
  return { mutations, permissionRequests, requestsWithoutTeam, connect: () => { connected = true; } };
}
