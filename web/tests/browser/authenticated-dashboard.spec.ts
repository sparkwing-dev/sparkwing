import { expect, test } from "@playwright/test";
import { mkdtemp, readFile, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { startAuthenticatedDashboard } from "./authenticated-server";

type ControllerState = {
  created: boolean;
  login_calls: number;
  session_headers: string[];
  logout_sessions: string[];
  proxy_authorizations: string[];
  proxy_cookies: string[];
  proxy_csrf_headers: string[];
  mutation_bodies: string[];
  active_sessions: string[];
};

test("auth-disabled bootstrap authenticates the dashboard without exposing its service bearer", async ({
  page,
  context,
}) => {
  test.setTimeout(120_000);
  const dashboard = await startAuthenticatedDashboard();
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
    await expect(page.getByRole("heading", { name: "Create first admin" })).toBeVisible();
    const bootstrap = page.locator('form[action="/login/bootstrap"]');
    await expect(bootstrap).toHaveAttribute("method", /post/i);
    await expect(bootstrap.locator('input[name="username"]')).toHaveAttribute(
      "autocomplete",
      "username",
    );
    await expect(bootstrap.locator('input[name="password"]')).toHaveAttribute(
      "type",
      "password",
    );
    await expect(bootstrap.locator('input[name="next"]')).toHaveValue(
      "/runs?run=auth-run&tab=logs",
    );

    const preauthCookies = await context.cookies(dashboard.origin);
    const preauthCSRF = preauthCookies.find(
      (cookie) => cookie.name === "sw_csrf",
    );
    expect(preauthCSRF).toMatchObject({ httpOnly: false, sameSite: "Strict" });
    await expect(bootstrap.locator('input[name="csrf_token"]')).toHaveValue(
      preauthCSRF?.value ?? "missing",
    );

    const forgedBootstrap = await page.request.post(
      `${dashboard.origin}/login/bootstrap`,
      {
        failOnStatusCode: false,
        form: {
          username: "admin",
          password: "correct-horse",
          next: "/runs",
          csrf_token: "forged-token",
        },
        headers: { Origin: dashboard.origin },
      },
    );
    expect(forgedBootstrap.status()).toBe(403);
    let state = (await (
      await fetch(`${dashboard.controller_origin}/__fixture/state`)
    ).json()) as ControllerState;
    expect(state.created).toBe(false);
    expect(state.login_calls).toBe(0);

    const unauthenticatedAPI = await page.request.get(
      `${dashboard.origin}/api/v1/runs`,
      { failOnStatusCode: false },
    );
    expect(unauthenticatedAPI.status()).toBe(401);

    await bootstrap.locator('input[name="username"]').fill("admin");
    await bootstrap.locator('input[name="password"]').fill("correct-horse");
    await bootstrap
      .getByRole("button", { name: "Create admin and sign in" })
      .click();
    await expect(page).toHaveURL(
      `${dashboard.origin}/runs?run=auth-run&tab=logs`,
    );
    await expect(
      page.getByRole("button", { name: "Log out", exact: true }),
    ).toBeVisible();

    const cookies = await context.cookies(dashboard.origin);
    const session = cookies.find((cookie) => cookie.name === "sw_session");
    const csrf = cookies.find((cookie) => cookie.name === "sw_csrf");
    expect(session).toMatchObject({
      value: "session-1",
      httpOnly: true,
      sameSite: "Strict",
    });
    expect(csrf).toMatchObject({
      value: "csrf-token",
      httpOnly: false,
      sameSite: "Strict",
    });

    const html = await (await page.request.get(`${dashboard.origin}/`)).text();
    expect(html).not.toContain(dashboard.service_token);
    expect(html).toContain('window.__SPARKWING_TOKEN__=""');
    expect(
      await page.evaluate(() => {
        const runtime = window as unknown as {
          __SPARKWING_TOKEN__?: string;
          __SPARKWING_API_URL__?: string;
          __SPARKWING_REQUIRE_LOGIN__?: string;
        };
        return {
          token: runtime.__SPARKWING_TOKEN__,
          apiURL: runtime.__SPARKWING_API_URL__,
          requireLogin: runtime.__SPARKWING_REQUIRE_LOGIN__,
        };
      }),
    ).toEqual({ token: "", apiURL: "", requireLogin: "true" });

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

    await page.evaluate(() => {
      document.cookie = "sw_csrf=csrf-token; Path=/; SameSite=Strict";
    });
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
    state = (await (
      await fetch(`${dashboard.controller_origin}/__fixture/state`)
    ).json()) as ControllerState;
    const sessionCallsBeforeAssets = state.session_headers.length;
    for (let i = 0; i < 3; i += 1) {
      expect((await page.request.get(immutableAsset!)).status()).toBe(200);
    }
    state = (await (
      await fetch(`${dashboard.controller_origin}/__fixture/state`)
    ).json()) as ControllerState;
    expect(state.session_headers).toHaveLength(sessionCallsBeforeAssets);

    const proxyStatus = await page.evaluate(
      async () =>
        (await fetch("/api/v1/runs", { headers: { Accept: "application/json" } }))
          .status,
    );
    expect(proxyStatus).toBe(200);
    await expect.poll(() => browserAuthorizations.length).toBeGreaterThan(0);
    expect(browserAuthorizations).toEqual(
      Array.from({ length: browserAuthorizations.length }, () => null),
    );

    const mutationBody = '{"action":"cancel"}';
    const crossOriginMutation = await page.request.post(
      `${dashboard.origin}/api/v1/runs/cancel-me/cancel`,
      {
        failOnStatusCode: false,
        data: mutationBody,
        headers: {
          "Content-Type": "text/plain",
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
        data: mutationBody,
        headers: {
          "Content-Type": "text/plain",
          Origin: dashboard.origin,
          "X-CSRF-Token": "attacker-token",
        },
      },
    );
    expect(mismatchedMutation.status()).toBe(403);
    state = (await (
      await fetch(`${dashboard.controller_origin}/__fixture/state`)
    ).json()) as ControllerState;
    expect(state.mutation_bodies).toEqual([]);

    const legitimateMutation = await page.evaluate(async (body) => {
      const encoded = document.cookie
        .split(";")
        .map((value) => value.trim())
        .find((value) => value.startsWith("sw_csrf="))
        ?.slice("sw_csrf=".length);
      return (
        await fetch("/api/v1/runs/cancel-me/cancel", {
          method: "POST",
          headers: {
            "Content-Type": "text/plain",
            "X-CSRF-Token": decodeURIComponent(encoded ?? ""),
          },
          body,
        })
      ).status;
    }, mutationBody);
    expect(legitimateMutation).toBe(204);

    state = (await (
      await fetch(`${dashboard.controller_origin}/__fixture/state`)
    ).json()) as ControllerState;
    expect(state.created).toBe(true);
    expect(state.login_calls).toBe(1);
    expect(state.session_headers).toContain("Session session-1");
    expect(state.proxy_authorizations.length).toBeGreaterThan(0);
    expect(
      state.proxy_authorizations.every(
        (authorization) => authorization === `Bearer ${dashboard.service_token}`,
      ),
    ).toBe(true);
    expect(state.proxy_cookies.every((cookie) => cookie === "")).toBe(true);
    expect(state.proxy_csrf_headers.every((header) => header === "")).toBe(
      true,
    );
    expect(state.mutation_bodies).toEqual([mutationBody]);

    const logout = page.locator('form[action="/logout"]');
    await expect(logout).toHaveAttribute("method", /post/i);
    await expect(logout.locator('input[name="csrf_token"]')).toHaveValue(
      "csrf-token",
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
    state = (await (
      await fetch(`${dashboard.controller_origin}/__fixture/state`)
    ).json()) as ControllerState;
    expect(state.logout_sessions).toEqual([]);

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
    expect(signedOutCSRF?.value).not.toBe("csrf-token");
    await expect(
      page.locator('form[action="/login"] input[name="csrf_token"]'),
    ).toHaveValue(signedOutCSRF?.value ?? "missing");

    const copiedBearer = await page.request.get(
      `${dashboard.origin}/api/v1/runs`,
      {
        failOnStatusCode: false,
        headers: { Authorization: `Bearer ${dashboard.service_token}` },
      },
    );
    expect(copiedBearer.status()).toBe(401);

    const copiedSession = await page.request.get(
      `${dashboard.origin}/api/v1/runs`,
      {
        failOnStatusCode: false,
        headers: {
          Accept: "application/json",
          Cookie: "sw_session=session-1; sw_csrf=csrf-token",
        },
      },
    );
    expect(copiedSession.status()).toBe(401);

    await page.goto(
      `${dashboard.origin}/login?next=%2Fcluster%3Fview%3Dservices%26tab%3Dnodes`,
    );
    const login = page.locator('form[action="/login"]');
    await expect(login.locator('input[name="next"]')).toHaveValue(
      "/cluster?view=services&tab=nodes",
    );
    const loginCSRF = (await context.cookies(dashboard.origin)).find(
      (cookie) => cookie.name === "sw_csrf",
    );
    await expect(login.locator('input[name="csrf_token"]')).toHaveValue(
      loginCSRF?.value ?? "missing",
    );
    await login.locator('input[name="username"]').fill("admin");
    await login.locator('input[name="password"]').fill("correct-horse");
    await login.getByRole("button", { name: "Sign in", exact: true }).click();
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

    const revoked = await fetch(
      `${dashboard.controller_origin}/__fixture/revoke?session_id=session-2`,
      { method: "POST" },
    );
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

    state = (await (
      await fetch(`${dashboard.controller_origin}/__fixture/state`)
    ).json()) as ControllerState;
    expect(state.login_calls).toBe(2);
    expect(state.logout_sessions).toEqual(["session-1"]);
    expect(state.active_sessions).toEqual([]);
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
