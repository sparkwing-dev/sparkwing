import { test } from "node:test";
import assert from "node:assert/strict";
import { decideRelease, sdkDigest } from "./typescript-release.mjs";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";

const previous = { language: "typescript", version: "v0.1.0", protocol: 2, source_digest: "a" };
test("unchanged SDK keeps its version across engine releases and an unused version bump", () => {
  for (const version of ["0.1.0", "0.2.0"]) {
    assert.deepEqual(decideRelease(version, "a", [previous]), { publish: false, entry: previous });
  }
});
test("first publication and changed source select the independent package version", () => {
  assert.equal(decideRelease("0.1.0", "a", []).publish, true);
  assert.deepEqual(decideRelease("0.2.0", "b", [previous]), {
    publish: true, entry: { ...previous, version: "v0.2.0", source_digest: "b" },
  });
});
test("different source cannot reuse a version", () => {
  assert.throws(() => decideRelease("0.1.0", "b", [previous]), /already names different source/);
  for (const version of ["0.0.0", "1.0.0", "v0.1.0", "0.01.0"]) {
    assert.throws(() => decideRelease(version, "a", []), /independent SDK version/);
  }
});
test("another language cannot suppress TypeScript publication", () => {
  assert.equal(decideRelease("0.1.0", "a", [{ ...previous, language: "go" }]).publish, true);
});
test("digest covers source and bridge build inputs, excludes engine and version changes", () => {
  const root = mkdtempSync(join(tmpdir(), "typescript-release-"));
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("GIT_")));
  const git = (...args) => execFileSync("git", ["-C", root, ...args], { env });
  const put = (path, body) => writeFileSync(join(root, path), body);
  try {
    mkdirSync(join(root, "sdk/typescript/native"), { recursive: true });
    mkdirSync(join(root, ".github/workflows"), { recursive: true });
    put("sdk/typescript/package.json", '{"version":"0.1.0"}');
    put("sdk/typescript/native/protocol.c", "bridge");
    put(".github/workflows/release.yaml", "engine: one\n  typescript-native: node22\n  typescript-package: pack\n  release: engine");
    git("init", "--quiet");
    git("add", ".");
    const first = sdkDigest(root);
    put("policy.json", '{"sdks":[]}');
    execFileSync(process.execPath, [fileURLToPath(new URL("./typescript-release.mjs", import.meta.url)), root, join(root, "policy.json"), join(root, "entry.json")], {
      env: { ...env, GITHUB_OUTPUT: join(root, "outputs") },
    });
    assert.deepEqual(JSON.parse(readFileSync(join(root, "entry.json"))), [{ ...previous, source_digest: first }]);
    assert.equal(readFileSync(join(root, "outputs"), "utf8"), "publish=true\nversion=0.1.0\n");
    put("sdk/typescript/package.json", '{"version":"0.2.0"}');
    put(".github/workflows/release.yaml", "engine: two\n  typescript-native: node22\n  typescript-package: pack\n  release: engine2");
    assert.equal(sdkDigest(root), first);
    put("sdk/typescript/native/protocol.c", "changed bridge");
    assert.notEqual(sdkDigest(root), first);
    const changed = sdkDigest(root);
    put(".github/workflows/release.yaml", "engine: two\n  typescript-native: node24\n  typescript-package: pack\n  release: engine2");
    assert.notEqual(sdkDigest(root), changed);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test("reverted source and revoked SDK require a fresh version", () => {
  const latest = { ...previous, version: "v0.2.0", source_digest: "b" };
  assert.throws(() => decideRelease("0.1.0", "a", [previous, latest]), /newer/);
  assert.equal(decideRelease("0.3.0", "a", [previous, latest]).publish, true);
  assert.throws(() => decideRelease("0.1.0", "a", [{ ...previous, revoked: { reason: "unsafe" } }]), /newer/);
});
