import assert from "node:assert/strict";
import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { after, before, test } from "node:test";
import { NodeRoutes, RouteError, routeConfigFromEnv } from "../src/routes.ts";

let server: Server;
let base = "";
const seen: Array<{ method: string; url: string; auth: string; body: string }> = [];

async function bodyOf(req: IncomingMessage): Promise<string> {
  let s = "";
  for await (const chunk of req) s += chunk;
  return s;
}

before(async () => {
  server = createServer(async (req, res) => {
    const body = await bodyOf(req);
    seen.push({ method: req.method ?? "", url: req.url ?? "", auth: req.headers.authorization ?? "", body });
    if (req.headers.authorization !== "Bearer node-token") {
      res.writeHead(401).end("no");
      return;
    }
    if (req.method === "GET" && req.url === "/node/v1/secrets/API_KEY") {
      res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ value: "s3cret" }));
    } else if (req.method === "GET" && req.url === "/node/v1/outputs/build") {
      res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ digest: "sha-1" }));
    } else if (req.method === "PUT" && req.url === "/node/v1/outputs") {
      res.writeHead(204).end();
    } else {
      res.writeHead(404).end("not found");
    }
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}/`;
});

after(() => new Promise<void>((resolve) => server.close(() => resolve())));

test("secret, output and putOutput call the node routes with the bearer", async () => {
  const routes = new NodeRoutes({ baseUrl: base, token: "node-token" });
  assert.equal(await routes.secret("API_KEY"), "s3cret");
  assert.deepEqual(await routes.output("build"), { digest: "sha-1" });
  await routes.putOutput({ ok: true });
  const put = seen.find((s) => s.method === "PUT");
  assert.equal(put?.body, '{"ok":true}');
  assert.ok(seen.every((s) => s.auth === "Bearer node-token"));
});

test("a refused or missing route fails loud with its status", async () => {
  const routes = new NodeRoutes({ baseUrl: base, token: "node-token" });
  await assert.rejects(routes.secret("MISSING"), (err: unknown) => err instanceof RouteError && err.status === 404);
  const anon = new NodeRoutes({ baseUrl: base });
  await assert.rejects(anon.secret("API_KEY"), (err: unknown) => err instanceof RouteError && err.status === 401);
});

test("the route config comes from the node environment and requires a URL", () => {
  assert.deepEqual(routeConfigFromEnv({ SPARKWING_CONTROLLER_URL: "http://h", SPARKWING_AGENT_TOKEN: "t" }), {
    baseUrl: "http://h",
    token: "t",
  });
  assert.throws(() => routeConfigFromEnv({}), /SPARKWING_CONTROLLER_URL is not set/);
});
