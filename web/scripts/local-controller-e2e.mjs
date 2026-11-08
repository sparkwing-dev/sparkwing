import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createServer } from "node:http";
import { promisify } from "node:util";
import { chromium } from "@playwright/test";

const cli = process.argv[2];
const run = promisify(execFile);
async function command(...args) {
  const { stdout } = await run(cli, [...args, "-o", "json"], { timeout: 45000 });
  return JSON.parse(stdout);
}
const open = () => command("dashboard", "open");
const browser = await chromium.launch({ headless: true });
const attacker = createServer((req, res) => {
  assert.equal(req.headers.authorization, undefined);
  assert.equal(req.headers.cookie, undefined);
  res.end("<html><body>second actor</body></html>");
});
await new Promise(resolve => attacker.listen(0, "127.0.0.1", resolve));
try {
  const context = await browser.newContext();
  const page = await context.newPage();
  const login = await open();
  const origin = new URL(login.url).origin;
  const exchange = page.waitForResponse(r => r.url() === `${origin}/login` && r.request().method() === "POST");
  await page.goto(login.url);
  assert.equal((await exchange).status(), 200);
  await page.waitForFunction(() => !!localStorage.getItem("sparkwing.localSession"));
  const token = await page.evaluate(() => localStorage.getItem("sparkwing.localSession"));
  assert.equal(new URL(page.url()).hash, "");
  assert.equal((await context.cookies()).length, 0);
  const api = (base, bearer) => page.request.get(`${base}/api/v1/runs`, { headers: { Authorization: `Bearer ${bearer}` } });
  assert.equal((await api(origin, token)).status(), 200);
  const replay = await page.request.post(`${origin}/login`, {
    headers: { Origin: origin }, data: { code: new URL(login.url).hash.slice(6) },
  });
  assert.equal(replay.status(), 401);

  const other = await context.newPage();
  await other.goto(`http://127.0.0.1:${attacker.address().port}`);
  assert.equal(await other.evaluate(() => localStorage.getItem("sparkwing.localSession")), null);
  assert.equal(await other.evaluate(async origin => {
    try { await fetch(`${origin}/api/v1/runs`); return "readable"; } catch { return "blocked"; }
  }, origin), "blocked");
  const separate = await browser.newContext();
  const stranger = await separate.newPage();
  await stranger.goto(origin);
  assert.equal(await stranger.evaluate(() => localStorage.getItem("sparkwing.localSession")), null);
  assert.equal((await stranger.request.get(`${origin}/api/v1/runs`)).status(), 401);
  await separate.close();

  const oldPID = (await command("daemon", "status")).pid;
  const restart = await command("daemon", "restart", "--force");
  assert.equal(restart.restarted, true);
  assert.notEqual(restart.pid, oldPID);
  const next = await open();
  const nextOrigin = new URL(next.url).origin;
  assert.equal((await api(nextOrigin, token)).status(), 401);
  await page.goto(next.url);
  await page.waitForFunction(old => {
    const now = localStorage.getItem("sparkwing.localSession");
    return !!now && now !== old;
  }, token);
  const fresh = await page.evaluate(() => localStorage.getItem("sparkwing.localSession"));
  assert.equal((await api(nextOrigin, fresh)).status(), 200);
  await page.getByRole("button", { name: "Log out", exact: true }).click();
  await page.waitForFunction(() => localStorage.getItem("sparkwing.localSession") === null);
  assert.equal((await api(nextOrigin, fresh)).status(), 401);
  await context.close();
  console.log("PASS dashboard: exchange, origin isolation, second browser, restart revocation, logout");
} finally {
  await browser.close();
  await new Promise(resolve => attacker.close(resolve));
}
