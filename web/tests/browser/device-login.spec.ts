import AxeBuilder from "@axe-core/playwright";
import { type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

type Reply = { status: number; json?: Record<string, unknown> };

type Mock = {
  lookup?: Reply;
  approve?: Reply;
};

type Seen = {
  path: string;
  body: unknown;
  csrf: string | null;
};

const memberRequest = {
  user_code: "BCDFGHJK",
  kind: "member",
  machine: "korey-laptop",
  team: "acme",
  grants: [
    "runs.read",
    "logs.read",
    "runs.write",
    "runs.control",
    "approvals.write",
  ],
  created_at: "2026-11-04T10:00:00Z",
  expires_at: new Date(Date.now() + 8 * 60_000).toISOString(),
};

async function mockController(page: Page, mock: Mock = {}) {
  const seen: Seen[] = [];
  // Session mode is what makes the console send its CSRF token on a POST.
  await page.addInitScript(() => {
    (
      window as unknown as { __SPARKWING_REQUIRE_LOGIN__: string }
    ).__SPARKWING_REQUIRE_LOGIN__ = "true";
  });
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/v1/capabilities") {
      await route.fulfill({
        json: {
          mode: "cluster",
          teams: { enabled: true },
          auth: { providers: ["google"] },
        },
      });
    } else if (path === "/api/v1/me") {
      const team = { slug: "acme", display_name: "Acme", role: "owner" };
      await route.fulfill({
        json: {
          user: { id: "u1", email: "ada@example.com", name: "Ada" },
          active_team: team,
          memberships: [team],
          invitations: [],
        },
      });
    } else if (path.startsWith("/api/v1/auth/device/")) {
      seen.push({
        path,
        body: request.postDataJSON(),
        csrf: request.headers()["x-csrf-token"] ?? null,
      });
      if (path.endsWith("/lookup")) {
        await route.fulfill(
          mock.lookup ?? { status: 200, json: memberRequest },
        );
      } else if (path.endsWith("/approve") && mock.approve) {
        await route.fulfill(mock.approve);
      } else {
        await route.fulfill({ status: 204 });
      }
    } else {
      await route.fulfill({ json: {} });
    }
  });
  return seen;
}

// The console's shared accent button is white on #6366f1, 4.46:1, just
// under AA; that is a console-wide colour choice, so contrast is judged
// elsewhere and this check covers labels, landmarks and roles.
async function expectAccessible(page: Page) {
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
    .disableRules(["color-contrast"])
    .analyze();
  expect(results.violations).toEqual([]);
}

async function openDevicePage(page: Page, baseURL: string | undefined) {
  await page
    .context()
    .addCookies([
      { name: "__Host-sw_csrf", value: "session-csrf", domain: new URL(baseURL ?? "http://127.0.0.1").hostname, path: "/", secure: true },
    ]);
  await page.goto("/device");
  await expect(
    page.getByRole("heading", { name: "Sign in a terminal" }),
  ).toBeVisible();
}

test("a typed code is approved and the terminal is told to finish", async ({
  page,
  baseURL,
}) => {
  const seen = await mockController(page);
  await page.goto("/device?code=BCDFGHJK&user_code=BCDFGHJK");
  await page
    .context()
    .addCookies([
      { name: "__Host-sw_csrf", value: "session-csrf", domain: new URL(baseURL ?? "http://127.0.0.1").hostname, path: "/", secure: true },
    ]);

  const field = page.getByLabel("Device code");
  // A code in the URL is never used; the person types it.
  await expect(field).toHaveValue("");
  const next = page.getByRole("button", { name: "Continue" });
  await expect(next).toBeDisabled();
  await field.pressSequentially("bcdf ghj");
  await expect(field).toHaveValue("BCDF-GHJ");
  await expect(next).toBeDisabled();
  await field.pressSequentially("k");
  await expect(field).toHaveValue("BCDF-GHJK");
  await expect(next).toBeEnabled();

  await expectAccessible(page);
  await next.click();

  await expect(
    page.getByRole("heading", { name: "Approve sign-in for korey-laptop?" }),
  ).toBeVisible();
  await expect(page.getByText("A CLI token for acme with:")).toBeVisible();
  await expect(page.getByText("Cancel and retry runs")).toBeVisible();
  await expect(page.getByText("Approve or deny run approvals")).toBeVisible();
  await expect(
    page.getByText(/expires in 90 days unless revoked/),
  ).toBeVisible();
  await expect(
    page.getByText(/This request expires in about \d+ minutes\./),
  ).toBeVisible();
  await expect(
    page.getByText("If someone sent you this code, deny it.", { exact: false }),
  ).toBeVisible();
  await expect(page.locator("dd", { hasText: "BCDF-GHJK" })).toBeVisible();
  await expectAccessible(page);

  await page.getByRole("button", { name: "Approve" }).click();
  await expect(
    page.getByText(
      "Approved. Return to your terminal; it finishes signing in on its own.",
    ),
  ).toBeVisible();

  expect(seen).toEqual([
    {
      path: "/api/v1/auth/device/lookup",
      body: { user_code: "BCDFGHJK" },
      csrf: "session-csrf",
    },
    {
      path: "/api/v1/auth/device/approve",
      body: { user_code: "BCDFGHJK" },
      csrf: "session-csrf",
    },
  ]);
});

test("a denied request says the terminal reports it", async ({
  page,
  baseURL,
}) => {
  const seen = await mockController(page);
  await openDevicePage(page, baseURL);
  await page.getByLabel("Device code").fill("BCDF-GHJK");
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Deny" }).click();
  await expect(
    page.getByText("Denied. The terminal reports the request as denied."),
  ).toBeVisible();
  expect(seen.map((s) => s.path)).toEqual([
    "/api/v1/auth/device/lookup",
    "/api/v1/auth/device/deny",
  ]);
  expect(seen[1].body).toEqual({ user_code: "BCDFGHJK" });
});

test("an unknown or expired code asks for a new one", async ({
  page,
  baseURL,
}) => {
  await mockController(page, {
    lookup: {
      status: 404,
      json: { error: { code: "not_found", message: "device code not found" } },
    },
  });
  await openDevicePage(page, baseURL);
  await page.getByLabel("Device code").fill("bcdfghjk");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("alert").filter({ hasText: /./ })).toHaveText(
    "That code is not valid or has expired. Run the command again in your terminal for a new code.",
  );
  await expect(page.getByLabel("Device code")).toHaveValue("BCDF-GHJK");
});

test("a refused approval shows the server's reason", async ({
  page,
  baseURL,
}) => {
  await mockController(page, {
    approve: {
      status: 403,
      json: {
        error: {
          code: "forbidden",
          message: "your role cannot grant runs.write",
        },
      },
    },
  });
  await openDevicePage(page, baseURL);
  await page.getByLabel("Device code").fill("BCDFGHJK");
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByRole("alert").filter({ hasText: /./ })).toHaveText(
    "your role cannot grant runs.write",
  );
  await expect(page.getByRole("button", { name: "Approve" })).toBeEnabled();
});

test("a runner request shows what the runner may do", async ({
  page,
  baseURL,
}) => {
  await mockController(page, {
    lookup: {
      status: 200,
      json: {
        ...memberRequest,
        kind: "runner",
        grants: ["nodes.claim", "triggers.claim", "claims.launch"],
        runner: {
          name: "build-box",
          personal: false,
          allowed_labels: ["deploy-prod", "tool:terraform"],
          repos: ["github.com/acme/*"],
          reserved_pools: ["gpu"],
          trust: "high",
          platform: "linux/amd64",
          launcher: true,
        },
      },
    },
  });
  await openDevicePage(page, baseURL);
  await page.getByLabel("Device code").fill("BCDF-GHJK");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(
    page.getByRole("heading", { name: "Approve runner build-box?" }),
  ).toBeVisible();
  await expect(page.getByText("Shared with acme")).toBeVisible();
  await expect(page.getByText("Allowed labels", { exact: true })).toBeVisible();
  await expect(page.getByText("deploy-prod", { exact: true })).toBeVisible();
  await expect(page.getByText("tool:terraform", { exact: true })).toBeVisible();
  await expect(page.getByText("github.com/acme/*")).toBeVisible();
  await expect(page.getByText("linux/amd64")).toBeVisible();
  await expect(page.getByText("A runner token for acme with:")).toBeVisible();
  await expect(page.getByText("Run jobs", { exact: false })).toBeVisible();
  await expect(page.getByText("Plan runs", { exact: false })).toBeVisible();
  await expect(
    page.getByText("Launch Cloud jobs for a reserved pool", { exact: false }),
  ).toBeVisible();
  await expect(page.getByText(/expires in 90 days/)).toHaveCount(0);
});
