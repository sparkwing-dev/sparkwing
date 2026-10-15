import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

test("package preflight rejects missing, truncated and mislabeled Windows bridges", () => {
  const root = mkdtempSync(join(tmpdir(), "sdk-package-"));
  const check = join(root, "check-package.mjs");
  copyFileSync(new URL("../native/check-package.mjs", import.meta.url), check);
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
    mkdirSync(join(root, `win32-${arch}`), { recursive: true });
    writeFileSync(join(root, `win32-${arch}`, "protocol.node"), bytes);
    return bytes;
  }
  const run = () => spawnSync(process.execPath, [check], { encoding: "utf8" });
  try {
    assert.match(run().stderr, /Windows x64.*before packaging/);
    bridge("x64", 0x8664);
    assert.match(run().stderr, /Windows arm64.*before packaging/);
    bridge("arm64", 0x8664);
    assert.notEqual(run().status, 0, "an x64 DLL must not ship under the arm64 name");
    bridge("arm64", 0xaa64, 2);
    assert.notEqual(run().status, 0, "an executable is not a loadable addon");
    bridge("arm64", 0xaa64);
    assert.equal(run().status, 0);
    for (const bytes of [Buffer.alloc(0), Buffer.from("not an addon"), bridge("arm64", 0xaa64).subarray(0, 160)]) {
      writeFileSync(join(root, "win32-arm64/protocol.node"), bytes);
      assert.notEqual(run().status, 0, "a truncated bridge must not pass preflight");
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
