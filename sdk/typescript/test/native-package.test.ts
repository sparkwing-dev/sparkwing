import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

test("package preflight rejects missing, truncated and mislabeled Windows bridges", () => {
  const root = mkdtempSync(join(tmpdir(), "sdk-package-"));
  writeFileSync(join(root, "package.json"), JSON.stringify({ name: "@sparkwing/sdk", version: "0.71.0", type: "module" }));
  writeFileSync(
    join(root, "package-lock.json"),
    JSON.stringify({ version: "0.71.0", packages: { "": { name: "@sparkwing/sdk", version: "0.71.0" } } }),
  );
  mkdirSync(join(root, "native"));
  const nativeCheck = join(root, "native", "check-package.mjs");
  copyFileSync(new URL("../native/check-package.mjs", import.meta.url), nativeCheck);
  copyFileSync(new URL("../native/verification.mjs", import.meta.url), join(root, "native", "verification.mjs"));
  function bridge(arch: string, machine: number, flags = 0x2002): Buffer {
    const bytes = Buffer.alloc(512);
    bytes.write("MZ");
    bytes.writeUInt32LE(128, 0x3c);
    bytes.write("PE\0\0", 128);
    bytes.writeUInt16LE(machine, 132);
    bytes.writeUInt16LE(1, 134);
    bytes.writeUInt16LE(240, 148);
    bytes.writeUInt16LE(flags, 150);
    bytes.writeUInt16LE(0x20b, 152);
    mkdirSync(join(root, "native", `win32-${arch}`), { recursive: true });
    writeFileSync(join(root, "native", `win32-${arch}`, "protocol.node"), bytes);
    return bytes;
  }
  const run = () => spawnSync(process.execPath, [nativeCheck], { encoding: "utf8" });
  try {
    assert.match(run().stderr, /Windows x64.*before packaging/);
    bridge("x64", 0x8664);
    assert.match(run().stderr, /Windows arm64.*before packaging/);
    bridge("arm64", 0x8664);
    assert.notEqual(run().status, 0, "an x64 DLL must not ship under the arm64 name");
    bridge("arm64", 0xaa64, 2);
    assert.notEqual(run().status, 0, "an executable is not a loadable addon");
    bridge("arm64", 0xaa64);
    assert.notEqual(run().status, 0, "a fabricated PE header is not native Windows verification");
    assert.match(run().stderr, /native verification/);
    for (const bytes of [Buffer.alloc(0), Buffer.from("not an addon"), bridge("arm64", 0xaa64).subarray(0, 160)]) {
      writeFileSync(join(root, "native/win32-arm64/protocol.node"), bytes);
      assert.notEqual(run().status, 0, "a truncated bridge must not pass preflight");
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("package preflight requires npm's engine version stamp in both manifests", () => {
  const root = mkdtempSync(join(tmpdir(), "sdk-version-"));
  const native = join(root, "native");
  mkdirSync(native);
  copyFileSync(new URL("../native/check-package.mjs", import.meta.url), join(native, "check-package.mjs"));
  copyFileSync(new URL("../native/verification.mjs", import.meta.url), join(native, "verification.mjs"));
  const write = (version: string, lockVersion = version, rootVersion = lockVersion) => {
    writeFileSync(join(root, "package.json"), JSON.stringify({ name: "@sparkwing/sdk", version, type: "module" }));
    writeFileSync(
      join(root, "package-lock.json"),
      JSON.stringify({
        name: "@sparkwing/sdk",
        version: lockVersion,
        lockfileVersion: 3,
        packages: { "": { name: "@sparkwing/sdk", version: rootVersion } },
      }),
    );
  };
  const run = () => spawnSync(process.execPath, [join(native, "check-package.mjs")], { encoding: "utf8" });
  try {
    for (const versions of [["0.0.0"], ["0.71.0", "0.70.0"], ["0.71.0", "0.71.0", "0.70.0"]]) {
      write(versions[0]!, versions[1], versions[2]);
      const result = run();
      assert.notEqual(result.status, 0);
      assert.match(result.stderr, /engine.*version|version.*engine/);
    }
    write("0.0.0");
    const npm = spawnSync("npm", ["version", "0.71.0", "--no-git-tag-version", "--ignore-scripts", "--offline"], {
      cwd: root,
      encoding: "utf8",
      shell: process.platform === "win32",
      env: { ...process.env, npm_config_cache: join(root, "cache") },
    });
    assert.equal(npm.status, 0, npm.stderr);
    assert.equal(JSON.parse(readFileSync(join(root, "package.json"), "utf8")).version, "0.71.0");
    const lock = JSON.parse(readFileSync(join(root, "package-lock.json"), "utf8"));
    assert.equal(lock.version, "0.71.0");
    assert.equal(lock.packages[""].version, "0.71.0");
    assert.match(run().stderr, /Windows x64.*before packaging/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("local pack installs on Unix and refuses npm publication", { skip: process.platform === "win32" }, async () => {
  const root = mkdtempSync(join(tmpdir(), "sdk-local-pack-"));
  const source = new URL("../", import.meta.url);
  try {
    const packed = spawnSync(process.execPath, [new URL("native/release.mjs", source).pathname, "local-pack", "--version", "0.71.0", "--destination", root], {
      encoding: "utf8",
    });
    assert.equal(packed.status, 0, packed.stderr);
    const archive = join(root, "sparkwing-sdk-0.71.0.tgz");
    writeFileSync(join(root, "package.json"), JSON.stringify({ private: true, type: "module", dependencies: { "@sparkwing/sdk": `file:${archive}` } }));
    const installed = spawnSync("npm", ["install", "--offline", "--ignore-scripts", "--no-audit", "--no-fund"], { cwd: root, encoding: "utf8" });
    assert.equal(installed.status, 0, installed.stderr);
    const manifest = JSON.parse(readFileSync(join(root, "node_modules/@sparkwing/sdk/package.json"), "utf8"));
    assert.equal(manifest.version, "0.71.0");
    assert.equal(manifest.private, true);
    const { createServer } = await import("node:http");
    const { spawn } = await import("node:child_process");
    let writes = 0;
    const server = createServer((request, response) => {
      if (request.method === "PUT") writes++;
      response.writeHead(404, { "Content-Type": "application/json" }).end('{"error":"not found"}');
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    try {
      const address = server.address();
      assert.ok(address && typeof address === "object");
      writeFileSync(join(root, ".npmrc"), `//127.0.0.1:${address.port}/:_authToken=fixture\n`);
      const child = spawn("npm", ["publish", archive, "--ignore-scripts", "--force", "--registry", `http://127.0.0.1:${address.port}/`], { cwd: root });
      let stderr = "";
      child.stdout.resume();
      child.stderr.on("data", (chunk) => { stderr += chunk; });
      const status = await new Promise<number | null>((resolve, reject) => { child.on("error", reject); child.on("close", resolve); });
      assert.notEqual(status, 0);
      assert.match(stderr, /EPRIVATE/);
      assert.equal(writes, 0);
    } finally {
      await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
