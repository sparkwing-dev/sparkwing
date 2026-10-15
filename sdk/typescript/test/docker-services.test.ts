import assert from "node:assert/strict";
import test from "node:test";
import * as fs from "node:fs/promises";
import { delimiter } from "node:path";
import { docker, services } from "../src/index.ts";
import { runScope } from "../src/scope.ts";
import { registerCommandContext } from "../src/command.ts";
import { fakeContext, commandFailure } from "./helper-context.ts";

test("Docker build and push preserve local and registry refs and use only manifest digests", async () => {
  const digest = "sha256:" + "a".repeat(64);
  const fake = fakeContext(({ argv }) => (argv[1] === "push" ? { stderr: `latest: digest: ${digest} size: 1234\n` } : ""));
  const result = await docker.buildAndPush(fake.ctx, { image: "app", tags: ["latest", "v1"], registries: ["registry.test"] });
  assert.equal(result.image, "registry.test/app:latest");
  assert.deepEqual(result.registries, ["registry.test"]);
  assert.deepEqual(result.digests, { "registry.test/app:latest": digest });
  assert.deepEqual(fake.calls[0]!.argv, ["docker", "build", "-f", "Dockerfile", "-t", "app:latest", "-t", "app:v1", "."]);
  assert.equal(
    fake.calls.some((call) => call.argv[1] === "inspect"),
    false,
  );
});

test("Docker push tries remaining registries but propagates all errors", async () => {
  const fake = fakeContext(({ argv }) => {
    if (argv[1] === "push" && argv[2]?.startsWith("bad/")) throw commandFailure("registry refused");
    return "";
  });
  await assert.rejects(docker.push(fake.ctx, "app:built", ["latest"], ["bad", "good"]), AggregateError);
  assert.deepEqual(fake.calls.at(-1)!.argv, ["docker", "push", "good/app:latest"]);
});

test("Docker login masks first and passes its secret through stdin without argv or exec logs", async (t) => {
  await fs.mkdir("/tmp/sw-astra-W5/helpers", { recursive: true });
  const root = await fs.mkdtemp("/tmp/sw-astra-W5/helpers/login-");
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  if (process.platform === "win32") await fs.copyFile(process.execPath, root + "/docker.exe");
  else await fs.symlink(process.execPath, root + "/docker");
  await fs.writeFile(
    root + "/login",
    `const fs = require('node:fs'); const input = fs.readFileSync(0, 'utf8'); fs.writeFileSync('received.json', JSON.stringify({ input, args: process.argv.slice(2) }));`,
  );
  const fake = fakeContext(() => "");
  const events: unknown[] = [];
  registerCommandContext(
    fake.ctx,
    (event, attrs) => {
      assert.deepEqual(fake.messages[0], { mask: "private-value" });
      events.push({ event, attrs });
    },
    { PATH: root + delimiter + (process.env.PATH ?? "") },
  );
  await runScope({ phase: "execute", root }, () => docker.login(fake.ctx, "registry.test", "user", "private-value"));
  const received = JSON.parse(await fs.readFile(root + "/received.json", "utf8"));
  assert.equal(received.input, "private-value");
  assert.deepEqual(received.args, ["--username", "user", "--password-stdin", "registry.test"]);
  assert.equal(JSON.stringify(events).includes("private-value"), false);
});

test("Docker buildx filters variants, refuses unsupported targets and requires the plugin", async () => {
  const fake = fakeContext(({ argv }) =>
    argv[2] === "inspect" ? "Platforms: linux/amd64*, linux/arm64/v8\nPlatforms: linux/amd64, linux/386" : "",
  );
  assert.deepEqual(await docker.buildxPlatforms(fake.ctx), ["linux/amd64", "linux/arm64/v8", "linux/386"]);
  assert.deepEqual(await docker.filterBuildxPlatforms(fake.ctx, ["linux/arm64", "freebsd/amd64", "linux/amd64"]), [
    "linux/arm64",
    "linux/amd64",
  ]);
  await assert.rejects(
    docker.build(fake.ctx, { image: "app", tags: ["v1"], platforms: ["freebsd/amd64"] }),
    docker.PlatformUnsupportedError,
  );
  const missing = fakeContext(() => {
    throw commandFailure("buildx is missing");
  });
  await assert.rejects(docker.buildxPlatforms(missing.ctx), docker.BuildxRequiredError);
  const unavailable = fakeContext(() => {
    throw Object.assign(new Error("spawn docker ENOENT"), { code: "ENOENT" });
  });
  await assert.rejects(docker.build(unavailable.ctx, { image: "app", tags: ["v1"] }), docker.DockerUnavailableError);
});

test("Docker buildx pushes only remote tags and reads the pushed manifest metadata", async () => {
  const digest = "sha256:" + "b".repeat(64);
  const fake = fakeContext(async ({ argv }) => {
    if (argv[2] === "inspect") return "Platforms: linux/amd64";
    if (argv[2] === "build") {
      const index = argv.indexOf("--metadata-file");
      await fs.writeFile(argv[index + 1]!, JSON.stringify({ "containerimage.digest": digest }));
    }
    return "";
  });
  const result = await docker.buildAndPush(fake.ctx, {
    image: "app",
    tags: ["v1"],
    platforms: ["linux/amd64"],
    registries: ["registry.test"],
  });
  assert.deepEqual(result.digests, { "registry.test/app:v1": digest });
  const build = fake.calls.find((call) => call.argv[2] === "build")!.argv;
  assert.equal(build.includes("--push"), true);
  assert.equal(build.includes("app:v1"), false);
  assert.equal(build.includes("registry.test/app:v1"), true);
});

test("local multi-platform Docker builds request a loaded image and propagate unsupported image-store errors", async () => {
  const fake = fakeContext(({ argv }) => (argv[2] === "inspect" ? "Platforms: linux/amd64, linux/arm64" : ""));
  await docker.build(fake.ctx, { image: "app", tags: ["v1"], platforms: ["linux/amd64", "linux/arm64"] });
  assert.equal(fake.calls.find((call) => call.argv[2] === "build")!.argv.includes("--load"), true);
  const failure = commandFailure("Docker image store cannot load multiple platforms");
  const unavailable = fakeContext(({ argv }) => {
    if (argv[2] === "inspect") return "Platforms: linux/amd64, linux/arm64";
    if (argv[2] === "build") throw failure;
    return "";
  });
  await assert.rejects(
    docker.build(unavailable.ctx, { image: "app", tags: ["v1"], platforms: ["linux/amd64", "linux/arm64"] }),
    (error) => error === failure,
  );
});

test("Docker run uses API file transfer and registered cleanup", async (t) => {
  await fs.mkdir("/tmp/sw-astra-W5/helpers", { recursive: true });
  const root = await fs.mkdtemp("/tmp/sw-astra-W5/helpers/docker-");
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const fake = fakeContext(() => "");
  await runScope({ phase: "execute", root }, () =>
    docker.run(fake.ctx, {
      image: "node:22",
      inputDir: "input",
      outputDir: "/work/result",
      outputTo: "output",
      volumes: { cache: "/cache" },
      cmd: ["node", "task.js"],
    }),
  );
  const name = fake.registrations[0]![3]!;
  assert.deepEqual(fake.calls.find((call) => call.argv[1] === "cp")!.argv, ["docker", "cp", root + "/input/.", name + ":/work"]);
  assert.deepEqual(fake.calls.filter((call) => call.argv[1] === "cp")[1]!.argv, [
    "docker",
    "cp",
    name + ":/work/result/.",
    root + "/output",
  ]);
  assert.deepEqual(fake.calls.at(-1)!.argv, ["docker", "rm", "-f", name]);
  assert.deepEqual(fake.unregistered, ["cleanup-1"]);
  assert.equal(
    fake.calls.some((call) => call.argv.includes("--mount")),
    false,
  );
});

test("Docker output failure and cancellation remove the container without hiding the first error", async () => {
  for (const cancel of [false, true]) {
    const controller = new AbortController();
    const original = new Error(cancel ? "cancelled body" : "input copy failed");
    const fake = fakeContext(({ argv }) => {
      if (argv[1] === "start") {
        if (cancel) controller.abort(original);
        throw original;
      }
      return "";
    }, controller.signal);
    await assert.rejects(docker.run(fake.ctx, { image: "node:22" }), (error) => error === original);
    assert.equal(fake.calls.at(-1)!.argv[1], "rm");
    assert.equal(fake.calls.at(-1)!.signal.aborted, false);
    assert.deepEqual(fake.unregistered, ["cleanup-1"]);
  }
});

test("Docker cleanup failures keep host cleanup armed and preserve a body failure", async () => {
  const original = new Error("body failure");
  const fake = fakeContext(({ argv }) => {
    if (argv[1] === "start") throw original;
    if (argv[1] === "rm") throw new Error("daemon unavailable");
    return "";
  });
  await assert.rejects(docker.run(fake.ctx, { image: "node:22" }), (error) => error === original);
  assert.deepEqual(fake.unregistered, []);
  assert.equal(fake.messages.length, 1);
});

test("Docker run forwards raw output bytes through the command helper", async (t) => {
  await fs.mkdir("/tmp/sw-astra-W5/helpers", { recursive: true });
  const root = await fs.mkdtemp("/tmp/sw-astra-W5/helpers/stream-");
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  if (process.platform === "win32") await fs.copyFile(process.execPath, root + "/docker.exe");
  else await fs.symlink(process.execPath, root + "/docker");
  await fs.writeFile(
    root + "/start",
    `const fs = require('node:fs'); fs.writeSync(1, Buffer.from([0, 255, 10])); fs.writeSync(2, Buffer.from([1, 254, 10]));`,
  );
  await fs.writeFile(root + "/rm", "process.exit(0)");
  const fake = fakeContext(() => "");
  registerCommandContext(fake.ctx, () => {}, { PATH: root + delimiter + (process.env.PATH ?? "") });
  const stdout: Uint8Array[] = [],
    stderr: Uint8Array[] = [];
  await runScope({ phase: "execute", root }, () =>
    docker.run(fake.ctx, { image: "app", stdout: (bytes) => stdout.push(bytes), stderr: (bytes) => stderr.push(bytes) }),
  );
  assert.deepEqual(Buffer.concat(stdout), Buffer.from([0, 255, 10]));
  assert.deepEqual(Buffer.concat(stderr), Buffer.from([1, 254, 10]));
  assert.deepEqual(fake.unregistered, ["cleanup-1"]);
});

test("Docker missing requested output fails and still removes its owned container", async (t) => {
  await fs.mkdir("/tmp/sw-astra-W5/helpers", { recursive: true });
  const root = await fs.mkdtemp("/tmp/sw-astra-W5/helpers/output-");
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const missing = new Error("requested output path does not exist");
  const fake = fakeContext(({ argv }) => {
    if (argv[1] === "cp") throw missing;
    return "";
  });
  await runScope({ phase: "execute", root }, async () => {
    await assert.rejects(docker.run(fake.ctx, { image: "app", outputDir: "/missing", outputTo: "out" }), (error) => error === missing);
  });
  assert.equal(fake.calls.at(-1)!.argv[1], "rm");
  assert.deepEqual(fake.unregistered, ["cleanup-1"]);
});

test("service scopes allocate Docker ports, wait inside each container and clean up in reverse order", async () => {
  const databaseID = "a".repeat(64),
    redisID = "b".repeat(64);
  const fake = fakeContext(({ argv }) =>
    argv[1] === "port" ? "127.0.0.1:32123\n" : argv[1] === "create" ? (argv.includes("database") ? databaseID : redisID) : "",
  );
  let invoked = 0;
  await services.withServicesAddrs(
    fake.ctx,
    [
      { image: "postgres:17", name: "database", port: 5432, hostPort: services.autoPort, readyCmd: "pg_isready" },
      { image: "redis:7", name: "redis", port: 6379, readyCmd: "redis-cli ping" },
    ],
    (ctx, addrs) => {
      invoked++;
      assert.equal(ctx, fake.ctx);
      assert.deepEqual(addrs, [
        { name: "database", hostPort: 32123 },
        { name: "redis", hostPort: 6379 },
      ]);
    },
  );
  assert.equal(invoked, 1);
  assert.equal(fake.calls[0]!.argv.includes("127.0.0.1::5432"), true);
  assert.deepEqual(
    fake.calls.filter((call) => call.argv[1] === "exec").map((call) => call.argv),
    [
      ["docker", "exec", databaseID, "sh", "-c", "pg_isready"],
      ["docker", "exec", redisID, "sh", "-c", "redis-cli ping"],
    ],
  );
  assert.deepEqual(
    fake.calls.filter((call) => call.argv[1] === "rm").map((call) => call.argv.at(-1)),
    [redisID, databaseID],
  );
  assert.deepEqual(fake.unregistered, ["cleanup-2", "cleanup-1"]);
});

test("empty service scopes call once without Docker", async () => {
  const fake = fakeContext(() => {
    throw new Error("Docker must not be called");
  });
  let called = 0;
  await services.withServices(fake.ctx, [], () => {
    called++;
  });
  assert.equal(called, 1);
  assert.equal(fake.calls.length, 0);
});

test("service callback failures and cancellation preserve first error while cleaning all services", async () => {
  for (const cancel of [false, true]) {
    const controller = new AbortController(),
      original = new Error("callback failed");
    const fake = fakeContext(({ argv }) => (argv[1] === "create" ? "c".repeat(64) : ""), controller.signal);
    await assert.rejects(
      services.withServices(fake.ctx, [{ image: "db", name: "db", port: 1234, readyCmd: "ready" }], () => {
        if (cancel) controller.abort(original);
        throw original;
      }),
      (error) => error === original,
    );
    assert.equal(fake.calls.at(-1)!.argv[1], "rm");
    assert.equal(fake.calls.at(-1)!.signal.aborted, false);
  }
});

test("service cancellation survives a normally returning callback and cleanup failures", async () => {
  for (const cleanupFails of [false, true]) {
    const controller = new AbortController();
    const cancelled = new Error("service scope cancelled");
    const id = "a".repeat(64);
    const fake = fakeContext(({ argv }) => {
      if (argv[1] === "create") return id;
      if (argv[1] === "rm" && cleanupFails) throw new Error("removal failed");
      return "";
    }, controller.signal);
    await assert.rejects(
      services.withServices(fake.ctx, [{ image: "db", port: 1234, readyCmd: "ready" }], () => {
        controller.abort(cancelled);
      }),
      (error) => error === cancelled,
    );
    assert.deepEqual(fake.calls.at(-1)!.argv, ["docker", "rm", "-f", id]);
    assert.equal(fake.calls.at(-1)!.signal.aborted, false);
    assert.deepEqual(fake.unregistered, cleanupFails ? [] : ["cleanup-1"]);
  }
});

test("service readiness expiry removes started containers and never invokes the callback", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let invoked = false;
  const fake = fakeContext(({ argv }) => {
    if (argv[1] === "create") return "a".repeat(64);
    if (argv[1] === "exec") {
      t.mock.timers.tick(50);
      throw commandFailure("not ready");
    }
    return "";
  });
  await assert.rejects(
    services.withServices(fake.ctx, [{ image: "db", name: "db", port: 1234, readyCmd: "ready", readyTimeoutMs: 50 }], () => {
      invoked = true;
    }),
    /50ms/,
  );
  assert.equal(invoked, false);
  assert.equal(fake.calls.at(-1)!.argv[1], "rm");
  assert.deepEqual(fake.unregistered, ["cleanup-1"]);
});

test("service readiness zero retains the default timeout", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const fake = fakeContext(({ argv }) => {
    if (argv[1] === "create") return "a".repeat(64);
    if (argv[1] === "exec") {
      t.mock.timers.tick(services.defaultReadyTimeoutMs);
      throw commandFailure("not ready");
    }
    return "";
  });
  await assert.rejects(
    services.withServices(fake.ctx, [{ image: "db", port: 1234, readyCmd: "ready", readyTimeoutMs: 0 }], () => {}),
    /30000ms/,
  );
});

test("services without a readiness command wait the retained two seconds using the caller signal", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const fake = fakeContext(({ argv }) => (argv[1] === "create" ? "a".repeat(64) : ""));
  let called = false;
  const work = services.withServices(fake.ctx, [{ image: "db", port: 1234 }], () => {
    called = true;
  });
  await new Promise<void>((resolve) => setImmediate(resolve));
  t.mock.timers.tick(1999);
  assert.equal(called, false);
  t.mock.timers.tick(1);
  await work;
  assert.equal(called, true);
  assert.equal(fake.calls.at(-1)!.argv[1], "rm");
});

test("service create collisions never register or remove another container", async () => {
  const collision = new Error("container name already exists");
  const fake = fakeContext(() => {
    throw collision;
  });
  await assert.rejects(
    services.withServices(fake.ctx, [{ image: "db", name: "existing", port: 1234 }], () => {}),
    (error) => error === collision,
  );
  assert.equal(fake.registrations.length, 0);
  assert.equal(fake.calls.length, 1);
  assert.equal(fake.calls[0]!.argv[1], "create");
});

test("service startup failure cleans earlier services and stops later startup", async () => {
  const original = new Error("cannot start");
  const fake = fakeContext(({ argv }) => {
    if (argv[1] === "create") {
      if (argv.includes("second")) throw original;
      return "a".repeat(64);
    }
    return "";
  });
  await assert.rejects(
    services.withServices(
      fake.ctx,
      [
        { image: "db", name: "first", port: 1234, readyCmd: "ready" },
        { image: "db", name: "second", port: 1234, readyCmd: "ready" },
        { image: "db", name: "third", port: 1234, readyCmd: "ready" },
      ],
      () => {
        throw new Error("must not execute");
      },
    ),
    (error) => error === original,
  );
  assert.deepEqual(
    fake.calls.filter((call) => call.argv[1] === "rm").map((call) => call.argv.at(-1)),
    ["a".repeat(64)],
  );
  assert.equal(
    fake.calls.some((call) => call.argv.includes("third")),
    false,
  );
});

test("Docker and services reject planning before side effects", async () => {
  const fake = fakeContext(() => {
    throw new Error("must not run");
  });
  await runScope({ phase: "plan", root: "/repo" }, async () => {
    await assert.rejects(docker.build(fake.ctx, { image: "app", tags: ["v1"] }), /planning/);
    await assert.rejects(docker.login(fake.ctx, "registry", "user", "password"), /planning/);
    await assert.rejects(docker.run(fake.ctx, { image: "app" }), /planning/);
    await assert.rejects(
      services.withServices(fake.ctx, [], () => {}),
      /planning/,
    );
  });
  assert.equal(fake.calls.length, 0);
  assert.equal(fake.registrations.length, 0);
});

test("Docker image tags retain commit and content identity plus dirty and production suffixes", async (t) => {
  await fs.mkdir("/tmp/sw-astra-W5/helpers", { recursive: true });
  const root = await fs.mkdtemp("/tmp/sw-astra-W5/helpers/tags-");
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await fs.mkdir(root + "/.git");
  await fs.writeFile(root + "/input", "image content");
  const fake = fakeContext(({ argv }) => {
    if (argv[1] === "rev-parse") return "0123456789abcdef";
    if (argv[1] === "symbolic-ref") return "feature";
    if (argv[1] === "status") return " M input";
    if (argv[1] === "ls-files") return "input\0";
    throw new Error("unexpected command");
  });
  const tags = await docker.computeTagsIn(fake.ctx, root);
  assert.equal(tags.commit, "0123456789ab");
  assert.equal(tags.branch, "feature");
  assert.equal(tags.dirty, true);
  assert.match(tags.content, /^[a-f0-9]{12}$/);
  assert.equal(tags.deployTag(), `commit-${tags.commit}-files-${tags.content}-dirty`);
  assert.equal(tags.prodTag(), tags.deployTag() + "-prod");
  assert.deepEqual(tags.all(), [tags.deployTag(), tags.prodTag()]);
});
