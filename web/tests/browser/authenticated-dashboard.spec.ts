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
};

test("authenticates the real dashboard without exposing its service bearer", async ({
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
    await page.goto(`${dashboard.origin}/runs?run=auth-run`);
    await expect(page).toHaveURL(/\/login\?next=/);
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
      "/runs?run=auth-run",
    );

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
    await expect(page).toHaveURL(`${dashboard.origin}/runs?run=auth-run`);
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

    let state = (await (
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

    const logout = page.locator('form[action="/logout"]');
    await expect(logout).toHaveAttribute("method", /post/i);
    await logout.getByRole("button", { name: "Log out", exact: true }).click();
    await expect(page).toHaveURL(`${dashboard.origin}/login`);
    await expect(
      page.getByRole("button", { name: "Sign in", exact: true }),
    ).toBeVisible();
    expect(
      (await context.cookies(dashboard.origin)).filter((cookie) =>
        ["sw_session", "sw_csrf"].includes(cookie.name),
      ),
    ).toEqual([]);

    const copiedBearer = await page.request.get(
      `${dashboard.origin}/api/v1/runs`,
      {
        failOnStatusCode: false,
        headers: { Authorization: `Bearer ${dashboard.service_token}` },
      },
    );
    expect(copiedBearer.status()).toBe(401);

    await page.goto(`${dashboard.origin}/login?next=/cluster`);
    const login = page.locator('form[action="/login"]');
    await expect(login.locator('input[name="next"]')).toHaveValue("/cluster");
    await login.locator('input[name="username"]').fill("admin");
    await login.locator('input[name="password"]').fill("correct-horse");
    await login.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(page).toHaveURL(`${dashboard.origin}/cluster`);
    await expect(
      page.getByRole("button", { name: "Log out", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Log out", exact: true }).click();
    await expect(page).toHaveURL(`${dashboard.origin}/login`);

    state = (await (
      await fetch(`${dashboard.controller_origin}/__fixture/state`)
    ).json()) as ControllerState;
    expect(state.login_calls).toBe(2);
    expect(state.logout_sessions).toEqual(["session-1", "session-2"]);
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
        buildTimeoutMs: 200,
        temporaryParent: parent,
      }),
    ).rejects.toThrow("timed out after 200ms");
    expect(Date.now() - startedAt).toBeLessThan(5_000);

    const pid = Number(await readFile(pidFile, "utf8"));
    expect(() => process.kill(pid, 0)).toThrow();
    expect(await readdir(parent)).toEqual(["build.pid"]);
  } finally {
    await rm(parent, { recursive: true, force: true });
  }
});
