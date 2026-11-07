import assert from "node:assert/strict";
import test from "node:test";
import { docker, services } from "../src/index.ts";
import { registerHelpers } from "../src/helpers/host.ts";
import type { HostServices } from "../src/routes.ts";
import { fakeContext } from "./helper-context.ts";

test("Docker and services relay options and release host resources after callback failure", async () => {
  const { ctx } = fakeContext(() => {
    throw new Error("SDK spawned a command");
  });
  const calls: { operation: string; args: readonly unknown[] }[] = [];
  registerHelpers(ctx, {
    async helper(operation: string, args: readonly unknown[]) {
      calls.push({ operation, args });
      if (operation === "services.Start") return [{ name: "db", hostPort: 1234, cleanup_id: "1" }];
      if (operation === "docker.Build") return { image: "app:latest", digests: {}, registries: [] };
      return null;
    },
  } as unknown as HostServices);
  assert.deepEqual(await docker.build(ctx, { image: "app", tags: ["latest"] }), { image: "app:latest", digests: {}, registries: [] });
  await docker.login(ctx, "registry", "user", "secret");
  const failure = new Error("body failed");
  await assert.rejects(
    services.withServicesAddrs(ctx, [{ image: "postgres", port: 5432, readyTimeoutMs: 1500 }], async (_ctx, addrs) => {
      assert.equal(addrs[0]?.hostPort, 1234);
      throw failure;
    }),
    failure,
  );
  assert.deepEqual(
    calls.map((call) => call.operation),
    ["docker.Build", "docker.Login", "services.Start", "services.Stop"],
  );
  assert.deepEqual(calls[2]?.args, [[{ image: "postgres", port: 5432, readyTimeout: 1500000000 }]]);
});
test("Docker reconstructs host error classes", async () => {
  const { ctx } = fakeContext(() => "");
  registerHelpers(ctx, {
    async helper() {
      throw Object.assign(new Error("missing docker"), { code: "docker: binary not available on PATH" });
    },
  } as unknown as HostServices);
  await assert.rejects(docker.buildxPlatforms(ctx), docker.DockerUnavailableError);
});

test("services preserve callback and host cleanup failures", async () => {
  const { ctx } = fakeContext(() => "");
  const body = new Error("body failed");
  const cleanup = new Error("cleanup failed");
  registerHelpers(ctx, {
    async helper(operation: string) {
      if (operation === "services.Start") return [{ name: "db", hostPort: 5432, cleanup_id: "1" }];
      throw cleanup;
    },
  } as unknown as HostServices);
  await assert.rejects(
    services.withServices(ctx, [{ image: "postgres" }], () => {
      throw body;
    }),
    (error) => {
      assert.ok(error instanceof AggregateError);
      assert.deepEqual(error.errors, [body, cleanup]);
      return true;
    },
  );
});

test("Docker capture preserves binary bytes when the host command fails", async () => {
  const { ctx } = fakeContext(() => "");
  const bytes = Buffer.from([255, 0, 128, 10]);
  const cause = Object.assign(new Error("container failed"), { result: { stdout: bytes.toString("base64"), stderr: "" } });
  registerHelpers(ctx, {
    async helper() {
      throw cause;
    },
  } as unknown as HostServices);
  let captured = Buffer.alloc(0);
  await assert.rejects(
    docker.run(ctx, {
      image: "fixture",
      stdout: (chunk) => {
        captured = Buffer.from(chunk);
      },
    }),
    cause,
  );
  assert.deepEqual(captured, bytes);
});
