import { expect, test } from "@playwright/test";
import { mkdtemp, readFile, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { startAuthenticatedDashboard } from "./authenticated-server";

type FixtureState = { sessions: number };

async function fixtureState(controlOrigin: string): Promise<FixtureState> {
  return (await (
    await fetch(`${controlOrigin}/__fixture/state`)
  ).json()) as FixtureState;
}

test("the controller signs a browser in and authenticates the dashboard's calls by cookie", async ({
  page,
  context,
}) => {
  test.setTimeout(120_000);
  const dashboard = await startAuthenticatedDashboard();
  const cspViolations: string[] = [];
  page.on("console", (message) => {
    if (
      message.type() === "error" &&
      /content security policy/i.test(message.text())
    ) {
      cspViolations.push(message.text());
    }
  });
  const browserAuthorizations: (string | null)[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (request.method() === "GET" && url.pathname === "/api/v1/runs") {
      void request
        .headerValue("authorization")
        .then((value) => browserAuthorizations.push(value));
    }
  });

  try {
    await page.goto(`${dashboard.origin}/runs?run=auth-run&tab=logs`);
    await expect(page).toHaveURL(/\/login\?next=/);
    expect(new URL(page.url()).searchParams.get("next")).toBe(
      "/runs?run=auth-run&tab=logs",
    );
    const login = page.locator('form[action="/login"]');
    await expect(login).toHaveAttribute("method", /post/i);
    await expect(login.locator('input[name="username"]')).toHaveAttribute(
      "autocomplete",
      "username",
    );
    await expect(login.locator('input[name="password"]')).toHaveAttribute(
      "type",
      "password",
    );
    await expect(login.locator('input[name="next"]')).toHaveValue(
      "/runs?run=auth-run&tab=logs",
    );

    const preauthCSRF = (await context.cookies(dashboard.origin)).find(
      (cookie) => cookie.name === "sw_csrf",
    );
    expect(preauthCSRF).toMatchObject({ httpOnly: false, sameSite: "Strict" });
    await expect(login.locator('input[name="csrf_token"]')).toHaveValue(
      preauthCSRF?.value ?? "missing",
    );

    const forgedLogin = await page.request.post(`${dashboard.origin}/login`, {
      failOnStatusCode: false,
      form: {
        username: "admin",
        password: "correct-horse",
        next: "/runs",
        csrf_token: "forged-token",
      },
      headers: { Origin: dashboard.origin },
    });
    expect(forgedLogin.status()).toBe(403);
    expect((await fixtureState(dashboard.control_origin)).sessions).toBe(0);

    const unauthenticatedAPI = await page.request.get(
      `${dashboard.origin}/api/v1/runs`,
      { failOnStatusCode: false },
    );
    expect(unauthenticatedAPI.status()).toBe(401);

    await login.locator('input[name="username"]').fill("admin");
    await login.locator('input[name="password"]').fill("correct-horse");
    await login.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(page).toHaveURL(
      `${dashboard.origin}/runs?run=auth-run&tab=logs`,
    );
    await expect(
      page.getByRole("button", { name: "Log out", exact: true }),
    ).toBeVisible();

    const cookies = await context.cookies(dashboard.origin);
    const session = cookies.find((cookie) => cookie.name === "sw_session");
    const csrf = cookies.find((cookie) => cookie.name === "sw_csrf");
    expect(session).toMatchObject({ httpOnly: true, sameSite: "Lax" });
    expect(csrf).toMatchObject({ httpOnly: false, sameSite: "Strict" });
    expect(csrf?.value).not.toBe(preauthCSRF?.value);

    const indexResponse = await page.request.get(`${dashboard.origin}/`);
    const html = await indexResponse.text();
    expect(html).not.toContain(dashboard.admin_token);
    expect(html).not.toContain("__SPARKWING_TOKEN__");
    const headers = indexResponse.headers();
    expect(headers["content-security-policy"]).toContain("default-src 'self'");
    expect(headers["content-security-policy"]).toContain(
      "frame-ancestors 'none'",
    );
    expect(headers["content-security-policy"]).toContain("object-src 'none'");
    expect(headers["x-frame-options"]).toBe("DENY");
    expect(headers["x-content-type-options"]).toBe("nosniff");
    expect(headers["referrer-policy"]).toBe("same-origin");
    expect(
      await page.evaluate(() => {
        const runtime = window as unknown as {
          __SPARKWING_TOKEN__?: string;
          __SPARKWING_REQUIRE_LOGIN__?: string;
        };
        return {
          token: runtime.__SPARKWING_TOKEN__,
          requireLogin: runtime.__SPARKWING_REQUIRE_LOGIN__,
        };
      }),
    ).toEqual({ token: undefined, requireLogin: "true" });
    expect(cspViolations).toEqual([]);

    const malformedCookieErrors: string[] = [];
    const captureMalformedCookieError = (error: Error) => {
      malformedCookieErrors.push(error.message);
    };
    page.on("pageerror", captureMalformedCookieError);
    await page.evaluate(() => {
      document.cookie = "sw_csrf=%E0%A4%A; Path=/; SameSite=Strict";
    });
    await page.reload();
    await expect(
      page.getByRole("link", { name: "sparkwing", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Log out", exact: true }),
    ).toHaveCount(0);
    expect(malformedCookieErrors).toEqual([]);
    page.off("pageerror", captureMalformedCookieError);

    await page.evaluate((token) => {
      document.cookie = `sw_csrf=${token}; Path=/; SameSite=Strict`;
    }, csrf?.value ?? "");
    await page.reload();
    await expect(
      page.getByRole("button", { name: "Log out", exact: true }),
    ).toBeVisible();

    const immutableAsset = await page.evaluate(() =>
      performance
        .getEntriesByType("resource")
        .map((entry) => entry.name)
        .find((name) => new URL(name).pathname.startsWith("/_next/static/")),
    );
    expect(immutableAsset).toBeTruthy();
    const signedOutAsset = await fetch(immutableAsset!);
    expect(signedOutAsset.status).toBe(200);

    const runsStatus = await page.evaluate(
      async () =>
        (await fetch("/api/v1/runs", { headers: { Accept: "application/json" } }))
          .status,
    );
    expect(runsStatus).toBe(200);
    await expect.poll(() => browserAuthorizations.length).toBeGreaterThan(0);
    expect(browserAuthorizations).toEqual(
      Array.from({ length: browserAuthorizations.length }, () => null),
    );

    const crossOriginMutation = await page.request.post(
      `${dashboard.origin}/api/v1/runs/cancel-me/cancel`,
      {
        failOnStatusCode: false,
        data: "{}",
        headers: {
          "Content-Type": "application/json",
          Origin: "https://attacker.example.com",
          "X-CSRF-Token": csrf?.value ?? "missing",
        },
      },
    );
    expect(crossOriginMutation.status()).toBe(403);
    const mismatchedMutation = await page.request.post(
      `${dashboard.origin}/api/v1/runs/cancel-me/cancel`,
      {
        failOnStatusCode: false,
        data: "{}",
        headers: {
          "Content-Type": "application/json",
          Origin: dashboard.origin,
          "X-CSRF-Token": "attacker-token",
        },
      },
    );
    expect(mismatchedMutation.status()).toBe(403);

    const legitimateMutation = await page.evaluate(async () => {
      const encoded = document.cookie
        .split(";")
        .map((value) => value.trim())
        .find((value) => value.startsWith("sw_csrf="))
        ?.slice("sw_csrf=".length);
      return (
        await fetch("/api/v1/runs/cancel-me/cancel", {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "X-CSRF-Token": decodeURIComponent(encoded ?? ""),
          },
          body: "{}",
        })
      ).status;
    });
    expect(legitimateMutation).toBe(404);

    const logout = page.locator('form[action="/logout"]');
    await expect(logout).toHaveAttribute("method", /post/i);
    await expect(logout.locator('input[name="csrf_token"]')).toHaveValue(
      csrf?.value ?? "missing",
    );

    const forgedLogout = await page.request.post(`${dashboard.origin}/logout`, {
      failOnStatusCode: false,
      form: { csrf_token: "forged-token" },
      headers: { Origin: dashboard.origin },
    });
    expect(forgedLogout.status()).toBe(403);
    expect(
      await page.evaluate(
        async () =>
          (await fetch("/api/v1/runs", { headers: { Accept: "application/json" } }))
            .status,
      ),
    ).toBe(200);
    expect((await fixtureState(dashboard.control_origin)).sessions).toBe(1);

    await logout.getByRole("button", { name: "Log out", exact: true }).click();
    await expect(page).toHaveURL(`${dashboard.origin}/login`);
    await expect(
      page.getByRole("button", { name: "Sign in", exact: true }),
    ).toBeVisible();
    const signedOutCookies = await context.cookies(dashboard.origin);
    expect(
      signedOutCookies.filter((cookie) => cookie.name === "sw_session"),
    ).toEqual([]);
    const signedOutCSRF = signedOutCookies.find(
      (cookie) => cookie.name === "sw_csrf",
    );
    expect(signedOutCSRF?.value).toBeTruthy();
    expect(signedOutCSRF?.value).not.toBe(csrf?.value);
    await expect(
      page.locator('form[action="/login"] input[name="csrf_token"]'),
    ).toHaveValue(signedOutCSRF?.value ?? "missing");
    expect((await fixtureState(dashboard.control_origin)).sessions).toBe(0);

    const forgedBearer = await page.request.get(
      `${dashboard.origin}/api/v1/runs`,
      {
        failOnStatusCode: false,
        headers: { Authorization: "Bearer swu_not-a-token-anyone-minted-000000" },
      },
    );
    expect(forgedBearer.status()).toBe(401);

    const copiedSession = await page.request.get(
      `${dashboard.origin}/api/v1/runs`,
      {
        failOnStatusCode: false,
        headers: {
          Accept: "application/json",
          Cookie: `sw_session=${session?.value}; sw_csrf=${csrf?.value}`,
        },
      },
    );
    expect(copiedSession.status()).toBe(401);

    await page.goto(
      `${dashboard.origin}/login?next=%2Fcluster%3Fview%3Dservices%26tab%3Dnodes`,
    );
    const again = page.locator('form[action="/login"]');
    await expect(again.locator('input[name="next"]')).toHaveValue(
      "/cluster?view=services&tab=nodes",
    );
    const loginCSRF = (await context.cookies(dashboard.origin)).find(
      (cookie) => cookie.name === "sw_csrf",
    );
    await expect(again.locator('input[name="csrf_token"]')).toHaveValue(
      loginCSRF?.value ?? "missing",
    );
    await again.locator('input[name="username"]').fill("admin");
    await again.locator('input[name="password"]').fill("correct-horse");
    await again.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(page).toHaveURL(
      `${dashboard.origin}/cluster?view=services&tab=nodes`,
    );
    await expect(
      page.getByRole("button", { name: "Log out", exact: true }),
    ).toBeVisible();

    await page.goto(
      `${dashboard.origin}/login?next=${encodeURIComponent("https://attacker.example")}`,
    );
    await expect(page).toHaveURL(`${dashboard.origin}/`);

    const revoked = await fetch(`${dashboard.control_origin}/__fixture/revoke`, {
      method: "POST",
    });
    expect(revoked.status).toBe(204);
    await page.goto(`${dashboard.origin}/cluster`);
    await expect(page).toHaveURL(/\/login\?next=/);
    const revokedCookies = await context.cookies(dashboard.origin);
    expect(
      revokedCookies.filter((cookie) => cookie.name === "sw_session"),
    ).toEqual([]);
    expect(
      revokedCookies.find((cookie) => cookie.name === "sw_csrf")?.value,
    ).toBeTruthy();
    expect((await fixtureState(dashboard.control_origin)).sessions).toBe(0);
  } finally {
    await dashboard.close();
  }
});

test("bounds a failed fixture build and removes its temporary output", async () => {
  const parent = await mkdtemp(join(tmpdir(), "sparkwing-auth-build-test-"));
  const pidFile = join(parent, "build.pid");
  try {
    const startedAt = Date.now();
    await expect(
      startAuthenticatedDashboard({
        buildCommand: process.execPath,
        buildArgs: [
          "-e",
          'require("node:fs").writeFileSync(process.argv[1],String(process.pid));setInterval(()=>{},1000)',
          pidFile,
        ],
        buildTimeoutMs: 2_000,
        temporaryParent: parent,
      }),
    ).rejects.toThrow("timed out after 2000ms");
    expect(Date.now() - startedAt).toBeLessThan(5_000);

    const pid = Number(await readFile(pidFile, "utf8"));
    await expect
      .poll(
        () => {
          try {
            process.kill(pid, 0);
            return false;
          } catch {
            return true;
          }
        },
        { timeout: 2_000 },
      )
      .toBe(true);
    expect(await readdir(parent)).toEqual(["build.pid"]);
  } finally {
    await rm(parent, { recursive: true, force: true });
  }
});
