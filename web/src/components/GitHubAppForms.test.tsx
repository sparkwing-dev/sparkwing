import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import {
  ConnectGitHubForm,
  InstallationRow,
  SubscriptionForm,
  SubscriptionsTable,
} from "./GitHubAppForms";
import { pullRequestHelp } from "@/lib/githubApp";

const installation = {
  installation_id: 42,
  account_id: 7,
  account_login: "octo-org",
  account_type: "Organization" as const,
  suspended: true,
  connected_by: "u1",
  created_at: 1_700_000_000,
  updated_at: 1_700_000_000,
  manage_url:
    "https://github.com/organizations/octo-org/settings/installations/42",
};

const subscription = {
  repository: "octo-org/api",
  repository_id: 9,
  installation_id: 42,
  pipeline: "ci",
  push: true,
  pull_request: true,
  created_by: "u1",
  created_at: 1_700_000_000,
};

const noop = () => {};

describe("ConnectGitHubForm", () => {
  it("offers an authorization-only path for an existing installation", () => {
    const html = renderToStaticMarkup(
      <ConnectGitHubForm csrfToken="session-csrf" connected={false} />,
    );
    assert.match(html, /Already installed the app\? Connect an existing installation/);
    assert.match(html, /action="\/github\/app\/connect\/existing"/);
  });
  it("posts the session CSRF token to the dashboard server", () => {
    const html = renderToStaticMarkup(
      <ConnectGitHubForm csrfToken="session-csrf" connected={false} />,
    );
    const form = html.match(/<form[^>]*>/)?.[0] ?? "";
    assert.match(form, / action="\/github\/app\/connect"/);
    assert.match(form, / method="POST"/);
    assert.match(
      html,
      /<input type="hidden" name="csrf_token" value="session-csrf"\/>/,
    );
    assert.match(html, />Connect GitHub</);
  });

  it("cannot submit without a token", () => {
    const html = renderToStaticMarkup(
      <ConnectGitHubForm csrfToken="" connected={false} />,
    );
    assert.match(html, /<button type="submit"[^>]* disabled=""/);
  });
});

describe("InstallationRow", () => {
  it("shows the account, a suspended badge, the manage link and its repositories", () => {
    const html = renderToStaticMarkup(
      <InstallationRow
        inst={installation}
        repositories={[
          { repository_id: 9, full_name: "octo-org/api", private: true },
        ]}
        canManage
        busy={false}
        onDisconnect={noop}
      />,
    );
    assert.match(html, /octo-org/);
    assert.match(html, /Organization/);
    assert.match(html, /Suspended/);
    assert.match(html, new RegExp(`href="${installation.manage_url}"`));
    assert.match(html, /octo-org\/api \(private\)/);
    assert.match(html, />Disconnect</);
  });

  it("offers a reader no disconnect", () => {
    const html = renderToStaticMarkup(
      <InstallationRow
        inst={{ ...installation, suspended: false }}
        repositories={undefined}
        canManage={false}
        busy={false}
        onDisconnect={noop}
      />,
    );
    assert.doesNotMatch(html, /Disconnect/);
    assert.doesNotMatch(html, /Suspended/);
    assert.match(html, /Loading repositories/);
    assert.match(html, /Manage on GitHub/);
  });
});

describe("SubscriptionForm", () => {
  it("offers the installation's repositories and says forks aren't run", () => {
    const html = renderToStaticMarkup(
      <SubscriptionForm
        repositories={["octo-org/api", "octo-org/web"]}
        busy={false}
        onSave={noop}
      />,
    );
    assert.match(html, /<option value="octo-org\/api">octo-org\/api<\/option>/);
    assert.match(html, /<option value="octo-org\/web">octo-org\/web<\/option>/);
    for (const label of ["Pushes", "Pull requests"]) {
      assert.match(html, new RegExp(`/>${label}</label>`));
    }
    assert.ok(
      html.includes(pullRequestHelp.replace(/'/g, "&#x27;")),
      "the pull request help is on the form",
    );
    assert.doesNotMatch(html, /Fork pull requests/);
  });

  it("keeps an edited repository the installation no longer lists", () => {
    const html = renderToStaticMarkup(
      <SubscriptionForm
        repositories={["octo-org/web"]}
        initial={{
          repository: "gone/repo",
          pipeline: "ci",
          push: true,
          pull_request: false,
        }}
        busy={false}
        onSave={noop}
      />,
    );
    assert.match(html, /<option value="gone\/repo" selected="">/);
  });
});

describe("SubscriptionsTable", () => {
  it("lets an owner edit and remove", () => {
    const html = renderToStaticMarkup(
      <SubscriptionsTable
        subscriptions={[subscription]}
        canManage
        busy={false}
        onEdit={noop}
        onRemove={noop}
      />,
    );
    assert.match(html, /octo-org\/api/);
    assert.match(html, />Edit</);
    assert.match(html, />Remove</);
    assert.match(html, /aria-label="Pull requests: yes"/);
    assert.doesNotMatch(html, /Fork/);
  });

  it("shows a reader the subscriptions read-only", () => {
    const html = renderToStaticMarkup(
      <SubscriptionsTable
        subscriptions={[subscription]}
        canManage={false}
        busy={false}
        onEdit={noop}
        onRemove={noop}
      />,
    );
    assert.match(html, /octo-org\/api/);
    assert.doesNotMatch(html, /Edit|Remove|<button/);
  });
});
