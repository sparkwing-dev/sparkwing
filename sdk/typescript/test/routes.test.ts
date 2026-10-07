import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { after, before, test } from "node:test";
import { NodeRoutes, RouteError, routeConfigFromEnv } from "../src/routes.ts";

let server: Server;
let base = "";
const seen: Array<{ url: string; auth: string; protocol: string }> = [];
const outputBytes = JSON.stringify({ digest: "sha-1" });
const outputSum = createHash("sha256").update(outputBytes).digest("hex");

before(async () => {
  server = createServer((req, res) => {
    seen.push({ url: req.url ?? "", auth: req.headers.authorization ?? "", protocol: String(req.headers["sparkwing-node-protocol"] ?? "") });
    const json = (status: number, body: unknown) => res.writeHead(status, { "content-type": "application/json" }).end(JSON.stringify(body));
    if (req.url === "/blob/build") {
      res.writeHead(200).end(outputBytes);
      return;
    }
    if (req.url === "/blob/tampered") {
      res.writeHead(200).end(JSON.stringify({ digest: "evil" }));
      return;
    }
    if (req.headers.authorization !== "Bearer node-token") {
      res.writeHead(401).end("no");
      return;
    }
    switch (req.url) {
      case "/node/v1/secrets/API_KEY":
        return json(200, { value: "s3cret", masked: true });
      case "/node/v1/nodes/build/output":
        return json(200, { url: "/blob/build", sha256: outputSum, size: Buffer.byteLength(outputBytes) });
      case "/node/v1/nodes/tampered/output":
        return json(200, { url: "/blob/tampered", sha256: outputSum });
      case "/node/v1/nodes/running/output":
        return json(409, { error: "node is not done" });
      default:
        res.writeHead(404).end("not found");
    }
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}/`;
});

after(() => new Promise<void>((resolve) => server.close(() => resolve())));

test("secret and output call the node routes with the bearer and protocol header", async () => {
  const routes = new NodeRoutes({ baseUrl: base, token: "node-token" });
  assert.equal(await routes.secret("API_KEY"), "s3cret");
  assert.deepEqual(await routes.output("build"), { digest: "sha-1" });
  const routeCalls = seen.filter((s) => s.url.startsWith("/node/v1/"));
  assert.ok(routeCalls.every((s) => s.auth === "Bearer node-token" && s.protocol === "1"), JSON.stringify(routeCalls));
  assert.equal(seen.find((s) => s.url === "/blob/build")?.auth, "", "the signed URL gets no bearer");
});

test("an output whose bytes do not match the grant's digest is refused", async () => {
  const routes = new NodeRoutes({ baseUrl: base, token: "node-token" });
  await assert.rejects(routes.output("tampered"), /sha256 .* the grant says/);
});

test("a refused, missing or unfinished route fails loud with its status", async () => {
  const routes = new NodeRoutes({ baseUrl: base, token: "node-token" });
  await assert.rejects(routes.secret("MISSING"), (err: unknown) => err instanceof RouteError && err.status === 404);
  await assert.rejects(routes.output("running"), (err: unknown) => err instanceof RouteError && err.status === 409);
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
