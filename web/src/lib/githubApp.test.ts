import assert from "node:assert/strict";
import { afterEach, before, beforeEach, describe, it } from "node:test";

type GitHubAppLib = typeof import("./githubApp");
let lib!: GitHubAppLib;

type Runtime = {
  window?: { __SPARKWING_REQUIRE_LOGIN__?: string };
  document?: { cookie: string };
  fetch: typeof fetch;
};
const runtime = globalThis as unknown as Runtime;

before(async () => {
  runtime.window = {};
  try {
    lib = await import("./githubApp");
  } finally {
    delete runtime.window;
  }
});

type Call = { url: string; method: string; headers: Headers; body: string };
let calls: Call[] = [];
let respond: (call: Call) => Response;
const realFetch = runtime.fetch;

beforeEach(() => {
  calls = [];
  respond = () => new Response(null, { status: 204 });
  runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "true" };
  runtime.document = { cookie: "__Host-sw_csrf=session-csrf" };
  runtime.fetch = async (input, init) => {
    const call = {
      url: String(input),
      method: init?.method ?? "GET",
      headers: new Headers(init?.headers),
      body: typeof init?.body === "string" ? init.body : "",
    };
    calls.push(call);
    return respond(call);
  };
});

afterEach(() => {
  delete runtime.window;
  delete runtime.document;
  runtime.fetch = realFetch;
});

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status });

const installation = {
  installation_id: 42,
  account_id: 7,
  account_login: "octo-org",
  account_type: "Organization" as const,
  suspended: false,
  connected_by: "u1",
  created_at: 1_700_000_000,
  updated_at: 1_700_000_000,
  manage_url:
    "https://github.com/organizations/octo-org/settings/installations/42",
};

describe("githubAppEnabled", () => {
  it("reads only a controller that names its App", () => {
    assert.equal(lib.githubAppEnabled(null), false);
    assert.equal(lib.githubAppEnabled({ mode: "cluster" }), false);
    assert.equal(
      lib.githubAppEnabled({
        mode: "cluster",
        github_app: { slug: "", source_tokens: true },
      }),
      false,
    );
    assert.equal(
      lib.githubAppEnabled({
        mode: "cluster",
        github_app: { slug: "sparkwing", source_tokens: true },
      }),
      true,
    );
  });
});

describe("GitHub App calls", () => {
  it("reads the team's App and installations", async () => {
    respond = () => json({ slug: "sparkwing", installations: [installation] });
    const app = await lib.getGitHubApp();
    assert.equal(calls[0].url, "/api/v1/team/github-app");
    assert.equal(calls[0].method, "GET");
    assert.deepEqual(app, { slug: "sparkwing", installations: [installation] });
  });

  it("reads an App with no installations as an empty list", async () => {
    respond = () => json({ slug: "sparkwing" });
    assert.deepEqual(await lib.getGitHubApp(), {
      slug: "sparkwing",
      installations: [],
    });
  });

  it("lists one installation's repositories", async () => {
    const repo = { repository_id: 9, full_name: "octo-org/api", private: true };
    respond = () => json({ repositories: [repo] });
    assert.deepEqual(await lib.listInstallationRepositories(42), [repo]);
    assert.equal(
      calls[0].url,
      "/api/v1/team/github-app/installations/42/repositories",
    );
  });

  it("disconnects with the session CSRF token", async () => {
    await lib.disconnectInstallation(42);
    assert.equal(calls[0].url, "/api/v1/team/github-app/installations/42");
    assert.equal(calls[0].method, "DELETE");
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
  });

  it("lists subscriptions", async () => {
    respond = () => json({ triggers: [] });
    assert.deepEqual(await lib.listSubscriptions(), []);
    assert.equal(calls[0].url, "/api/v1/team/github-app/triggers");
  });

  it("saves a subscription as the controller's request shape", async () => {
    respond = (call) => json({ ...JSON.parse(call.body), repository_id: 9 });
    await lib.putSubscription({
      repository: " octo-org/api ",
      pipeline: " ci ",
      push: true,
      pull_request: false,
      branches: ["main", "release/*"],
      base_branches: [],
    });
    assert.equal(calls[0].method, "PUT");
    assert.equal(calls[0].url, "/api/v1/team/github-app/triggers");
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
    assert.deepEqual(JSON.parse(calls[0].body), {
      repository: "octo-org/api",
      pipeline: "ci",
      push: true,
      pull_request: false,
      branches: ["main", "release/*"],
      base_branches: [],
    });
  });

  it("removes a subscription by repository id and pipeline", async () => {
    await lib.deleteSubscription(9, "release build");
    assert.equal(calls[0].method, "DELETE");
    assert.equal(
      calls[0].url,
      "/api/v1/team/github-app/triggers?repository_id=9&pipeline=release+build",
    );
  });

  it("lists and saves the owner's extra repositories", async () => {
    const list = { repository: "octo-org/api", extra_repos: ["octo-org/lib"] };
    respond = () => json({ extra_repos: [list] });
    assert.deepEqual(await lib.listExtraRepos(), [list]);
    assert.equal(calls[0].url, "/api/v1/team/github-app/extra-repos");
    respond = (call) => json(JSON.parse(call.body));
    await lib.putExtraRepos(list);
    assert.equal(calls[1].method, "PUT");
    assert.equal(calls[1].url, "/api/v1/team/github-app/extra-repos");
    assert.equal(calls[1].headers.get("X-CSRF-Token"), "session-csrf");
    assert.deepEqual(JSON.parse(calls[1].body), list);
  });

  it("surfaces the controller's refusal", async () => {
    respond = () =>
      json({ error: "no installation this team holds covers a/b" }, 404);
    await assert.rejects(
      lib.putSubscription({
        repository: "a/b",
        pipeline: "ci",
        push: true,
        pull_request: false,
      }),
      /Save subscription: no installation this team holds covers a\/b/,
    );
  });
});

describe("extra repository rules", () => {
  it("offers the other covered repositories of the source's owner", () => {
    assert.deepEqual(
      lib.extraRepoChoices("Octo-Org/api", [
        "octo-org/api",
        "octo-org/lib",
        "other/lib",
      ]),
      ["octo-org/lib"],
    );
  });

  it("refuses more than the controller's cap", () => {
    const eleven = Array.from({ length: 11 }, (_, i) => `octo-org/r${i}`);
    assert.match(
      lib.extraReposProblem({
        repository: "octo-org/api",
        extra_repos: eleven,
      }) ?? "",
      /at most 10/,
    );
    assert.equal(
      lib.extraReposProblem({
        repository: "octo-org/api",
        extra_repos: eleven.slice(0, 10),
      }),
      null,
    );
  });
});

describe("subscription form rules", () => {
  const draft = {
    repository: "octo-org/api",
    pipeline: "ci",
    push: true,
    pull_request: true,
  };

  it("accepts a repository, a pipeline and at least one event", () => {
    assert.equal(lib.subscriptionProblem(draft), null);
    assert.equal(
      lib.subscriptionProblem({ ...draft, pull_request: false }),
      null,
    );
  });

  it("names what is missing", () => {
    assert.match(
      lib.subscriptionProblem({ ...draft, repository: "" }) ?? "",
      /repository/,
    );
    assert.match(
      lib.subscriptionProblem({ ...draft, pipeline: "  " }) ?? "",
      /pipeline/,
    );
    assert.match(
      lib.subscriptionProblem({ ...draft, pipeline: "a/b" }) ?? "",
      /without spaces or slashes/,
    );
    assert.match(
      lib.subscriptionProblem({
        ...draft,
        push: false,
        pull_request: false,
      }) ?? "",
      /pushes, pull requests or both/,
    );
  });

  it("checks branch pattern limits in bytes", () => {
    assert.match(
      lib.subscriptionProblem({ ...draft, branches: Array(11).fill("main") }) ?? "",
      /at most 10/,
    );
    assert.match(
      lib.subscriptionProblem({ ...draft, base_branches: ["é".repeat(65)] }) ?? "",
      /128 bytes/,
    );
  });

  it("never asks for pull requests from forks", () => {
    const stored = {
      repository: "octo-org/api",
      repository_id: 9,
      installation_id: 42,
      pipeline: "ci",
      push: true,
      pull_request: true,
      created_by: "u1",
      created_at: 1_700_000_000,
      fork_pull_requests: true,
      branches: [],
      base_branches: [],
    };
    assert.deepEqual(
      Object.keys(lib.subscriptionRequest(lib.draftFromSubscription(stored))),
      ["repository", "pipeline", "push", "pull_request", "branches", "base_branches"],
    );
  });

  it("offers the repositories of installations that can start runs", () => {
    const suspended = { ...installation, installation_id: 43, suspended: true };
    const repos = lib.subscribableRepositories([installation, suspended], {
      42: [
        { repository_id: 2, full_name: "octo-org/web", private: false },
        { repository_id: 1, full_name: "octo-org/api", private: true },
      ],
      43: [{ repository_id: 3, full_name: "frozen/repo", private: false }],
    });
    assert.deepEqual(repos, ["octo-org/api", "octo-org/web"]);
  });

  it("reads the account the dashboard server just connected", () => {
    assert.equal(lib.connectedAccount("?connected=octo-org"), "octo-org");
    assert.equal(lib.connectedAccount(""), null);
    assert.equal(lib.connectedAccount("?connected="), null);
  });
});
