import { type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

type Mock = {
  identities?: { provider: string; email: string; created_at: number }[];
  unlink?: { status: number; json?: Record<string, unknown> };
};

async function mockController(page: Page, mock: Mock = {}) {
  const seen = { unlinks: [] as string[] };
  let identities = mock.identities ?? [
    { provider: "google", email: "ada@example.com", created_at: 1_790_000_000 },
  ];
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/v1/capabilities") {
      await route.fulfill({
        json: {
          mode: "cluster",
          teams: { enabled: true },
          auth: { providers: ["google", "github"] },
        },
      });
    } else if (path === "/api/v1/me") {
      const team = { slug: "ada", display_name: "Ada's space", role: "owner" };
      await route.fulfill({
        json: {
          user: { id: "u1", email: "ada@example.com", name: "Ada" },
          active_team: team,
          memberships: [team],
          invitations: [],
        },
      });
    } else if (path === "/api/v1/me/identities") {
      await route.fulfill({
        json: { identities, providers: ["google", "github"] },
      });
    } else if (
      path.startsWith("/api/v1/me/identities/") &&
      request.method() === "DELETE"
    ) {
      const provider = path.split("/").pop() ?? "";
      seen.unlinks.push(provider);
      if (mock.unlink) {
        await route.fulfill(mock.unlink);
        return;
      }
      identities = identities.filter((id) => id.provider !== provider);
      await route.fulfill({ status: 204 });
    } else {
      await route.fulfill({ json: {} });
    }
  });
  return seen;
}

test("the page lists each provider and explains how accounts work", async ({
  page,
  baseURL,
}) => {
  await page
    .context()
    .addCookies([{ name: "sw_csrf", value: "session-csrf", url: baseURL }]);
  await mockController(page);
  await page.goto("/account/sign-ins");

  await expect(
    page.getByRole("heading", { name: "Linked sign-ins" }),
  ).toBeVisible();
  await expect(page.getByText("Linked · ada@example.com")).toBeVisible();
  await expect(page.getByText("Not linked")).toBeVisible();
  const link = page.getByRole("button", { name: "Link GitHub" });
  await expect(link).toBeEnabled();
  await expect(
    page.locator("form:has(button:text('Link GitHub'))"),
  ).toHaveAttribute("action", "/auth/github/link");
  await expect(page.getByRole("button", { name: "Unlink" })).toBeDisabled();

  const explain = page.locator("#how-accounts-work");
  await expect(explain).toContainText(
    "Your account holds your sign-in methods and your team memberships.",
  );
  await expect(explain).toContainText(
    "Runs, secrets, credits and machines belong to teams, not to accounts.",
  );
  await expect(explain).toContainText(
    "It doesn't bring anything over from another Sparkwing account.",
  );
  await expect(explain).toContainText(
    "We don't currently offer combining accounts.",
  );
});

test("a sign-in linked to another account is refused with the same guidance", async ({
  page,
}) => {
  await mockController(page);
  await page.goto(
    "/account/sign-ins?refused=identity_linked_elsewhere&provider=github",
  );
  const alert = page.getByRole("alert").filter({ hasText: /./ });
  await expect(alert).toContainText(
    "That GitHub sign-in is already linked to another Sparkwing account, so nothing changed.",
  );
  await expect(alert).toContainText("Keep both.");
  await expect(alert).toContainText("Delete the other one.");
  await expect(
    alert.getByRole("link", { name: "How accounts work" }),
  ).toHaveAttribute("href", "#how-accounts-work");
  await expect(page).toHaveURL(/\/account\/sign-ins$/);
});

test("unlinking needs another method and asks first", async ({ page }) => {
  const seen = await mockController(page, {
    identities: [
      { provider: "google", email: "ada@example.com", created_at: 1 },
      { provider: "github", email: "octo@example.org", created_at: 2 },
    ],
  });
  await page.goto("/account/sign-ins");
  const github = page.locator("li", { hasText: "octo@example.org" });
  let asked = "";
  page.once("dialog", (dialog) => {
    asked = dialog.message();
    return dialog.accept();
  });
  await github.getByRole("button", { name: "Unlink" }).click();
  await expect(page.getByRole("button", { name: "Link GitHub" })).toBeVisible();
  expect(asked).toContain(
    "GitHub App installations you connected stay connected to their teams.",
  );
  expect(seen.unlinks).toEqual(["github"]);
  await expect(page.getByRole("button", { name: "Unlink" })).toBeDisabled();
});

test("an unlink from a stale sign-in asks for a fresh one", async ({
  page,
}) => {
  await mockController(page, {
    identities: [
      { provider: "google", email: "ada@example.com", created_at: 1 },
      { provider: "github", email: "octo@example.org", created_at: 2 },
    ],
    unlink: {
      status: 403,
      json: { error: "reauth_required", message: "sign in again" },
    },
  });
  await page.goto("/account/sign-ins");
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .locator("li", { hasText: "octo@example.org" })
    .getByRole("button", { name: "Unlink" })
    .click();
  await expect(page.getByRole("alert").filter({ hasText: /./ })).toContainText(
    "Linking and unlinking need a recent sign-in.",
  );
});
