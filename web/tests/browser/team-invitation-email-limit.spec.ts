import { expect, test } from "./fixtures";

const note =
  "Your account has sent 100 invitation emails in the last 24 hours, the most Sparkwing sends for one account. Share the invitation link yourself. Your account can send invitation emails again after 14:05 UTC on Nov 26, 2026.";

test("an invitation past the account email limit shows when to try again", async ({
  page,
}) => {
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/v1/capabilities") {
      await route.fulfill({
        json: { mode: "cluster", teams: { enabled: true } },
      });
    } else if (path === "/api/v1/me") {
      const team = { slug: "acme", display_name: "Acme", role: "owner" };
      await route.fulfill({
        json: {
          user: { id: "u1", email: "a@example.com", name: "A" },
          active_team: team,
          memberships: [team],
          invitations: [],
        },
      });
    } else if (path === "/api/v1/team/members") {
      await route.fulfill({
        json: {
          members: [
            { user_id: "u1", email: "a@example.com", name: "A", role: "owner" },
          ],
        },
      });
    } else if (
      path === "/api/v1/team/invitations" &&
      request.method() === "POST"
    ) {
      await route.fulfill({
        status: 201,
        json: {
          id: "inv1",
          accept_url: "https://console.example/invitations?id=inv1",
          email_sent: false,
          email_note: note,
        },
      });
    } else if (path === "/api/v1/team/invitations") {
      await route.fulfill({ json: { invitations: [] } });
    } else {
      await route.fulfill({ json: {} });
    }
  });
  await page.goto("/team");
  await page.getByRole("textbox", { name: "Email" }).fill("guest@example.com");
  await page.getByRole("button", { name: "Invite" }).click();

  await expect(page.getByText(note)).toBeVisible();
  await expect(
    page.getByText("No email was sent. Send this link to"),
  ).toBeVisible();
  await expect(page.getByLabel("Invitation link", { exact: true })).toHaveText(
    "https://console.example/invitations?id=inv1",
  );
});
