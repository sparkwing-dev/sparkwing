import assert from "node:assert/strict";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { after, before, test } from "node:test";
import { NodeRoutes, RouteError, routeConfigFromEnv } from "../src/routes.ts";

let server: Server;
let base = "";
const seen: Array<{ url: string; auth: string; protocol: string }> = [];

before(async () => {
  server = createServer((req, res) => {
    seen.push({
      url: req.url ?? "",
      auth: req.headers.authorization ?? "",
      protocol: String(req.headers["sparkwing-node-protocol"] ?? ""),
    });
    const json = (status: number, body: unknown) => res.writeHead(status, { "content-type": "application/json" }).end(JSON.stringify(body));
    if (req.url === "/elsewhere") return json(200, { value: "leaked" });
    if (req.headers.authorization !== "Bearer node-token") return json(401, { error: "bearer unknown", code: "unauthorized" });
    switch (req.url) {
      case "/node/v1/secrets/API_KEY":
        return json(200, { value: "s3cret", masked: true });
      case "/node/v1/secrets/MOVED":
        return res.writeHead(302, { location: "/elsewhere" }).end();
      case "/node/v1/nodes/build/output":
        return json(200, { output: { digest: "sha-1" } });
      case "/node/v1/nodes/build%2Flinux/output":
        return json(200, { output: "linux" });
      case "/node/v1/nodes/empty/output":
        return json(200, { output: null });
      case "/node/v1/nodes/shapeless/output":
        return json(200, {});
      case "/node/v1/nodes/running/output":
        return json(409, { error: "node is not done", code: "not_ready" });
      default:
        return json(404, { error: "no such secret", code: "secret_not_found" });
    }
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

after(() => new Promise<void>((resolve) => server.close(() => resolve())));

test("secret and output call the node routes with the bearer and protocol header", async () => {
  const routes = new NodeRoutes({ baseUrl: base, token: "node-token" });
  assert.equal(await routes.secret("API_KEY"), "s3cret");
  assert.deepEqual(await routes.output("build"), { digest: "sha-1" });
  assert.equal(await routes.output("build/linux"), "linux", "a / in a node id is escaped");
  assert.ok(
    seen.every((s) => s.auth === "Bearer node-token" && s.protocol === "1"),
    JSON.stringify(seen),
  );
});

test("a node that recorded no output reads as null, and a reply without the field fails", async () => {
  const routes = new NodeRoutes({ baseUrl: base, token: "node-token" });
  assert.equal(await routes.output("empty"), null);
  await assert.rejects(routes.output("shapeless"), /without an output field/);
});

test("a route error carries its status and code", async () => {
  const routes = new NodeRoutes({ baseUrl: base, token: "node-token" });
  await assert.rejects(
    routes.secret("MISSING"),
    (err: unknown) => err instanceof RouteError && err.status === 404 && err.code === "secret_not_found",
  );
  await assert.rejects(
    routes.output("running"),
    (err: unknown) => err instanceof RouteError && err.status === 409 && err.code === "not_ready",
  );
  const stranger = new NodeRoutes({ baseUrl: base, token: "wrong" });
  await assert.rejects(stranger.secret("API_KEY"), (err: unknown) => err instanceof RouteError && err.code === "unauthorized");
});

test("a redirect is refused rather than followed with the bearer", async () => {
  const routes = new NodeRoutes({ baseUrl: base, token: "node-token" });
  await assert.rejects(routes.secret("MOVED"), (err: unknown) => err instanceof RouteError && err.status === 302);
  assert.ok(!seen.some((s) => s.url === "/elsewhere"));
});

test("the route config comes from the node environment and must name loopback", () => {
  assert.deepEqual(
    routeConfigFromEnv({
      SPARKWING_NODE_URL: "http://127.0.0.1:41873",
      SPARKWING_NODE_TOKEN: "t",
    }),
    {
      baseUrl: "http://127.0.0.1:41873",
      token: "t",
    },
  );
  assert.equal(routeConfigFromEnv({}), undefined, "a process bound to no node has no routes");
  for (const url of [
    "http://example.com:80",
    "https://127.0.0.1:1",
    "http://127.0.0.1",
    "http://u:p@127.0.0.1:1",
    "http://127.0.0.1:1/x",
  ]) {
    assert.throws(
      () =>
        routeConfigFromEnv({
          SPARKWING_NODE_URL: url,
          SPARKWING_NODE_TOKEN: "t",
        }),
      /loopback|must be http/,
      url,
    );
  }
  assert.throws(() => routeConfigFromEnv({ SPARKWING_NODE_URL: "http://[::1]:1" }), /SPARKWING_NODE_TOKEN is empty/);
});
