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
  for (const version of ["0.0.0", "0.1.0", "0.2.0"]) {
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
    put("policy.json", JSON.stringify({ sdks: JSON.parse(readFileSync(join(root, "entry.json"))) }));
    put("outputs", "");
    const retained = execFileSync(process.execPath, [fileURLToPath(new URL("./typescript-release.mjs", import.meta.url)), root, join(root, "policy.json"), join(root, "entry.json"), root], {
      env: { ...env, GITHUB_OUTPUT: join(root, "outputs") },
    });
    assert.equal(JSON.parse(retained).publish, false);
    assert.equal(readFileSync(join(root, "outputs"), "utf8"), "publish=false\nversion=0.1.0\n");
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

test("recovery retains the exact signed SDK while a new source reversion needs a new version", () => {
  const latest = { ...previous, version: "v0.2.0", source_digest: "b" };
  assert.deepEqual(decideRelease("0.1.0", "a", [previous, latest], { recovery: true }), { publish: false, entry: previous });
  assert.throws(() => decideRelease("0.1.0", "a", [previous, latest]), /newer/);
});
test("an engine prerelease requires an already mapped SDK", () => {
  assert.throws(() => decideRelease("0.2.0", "b", [previous], { engineVersion: "v0.90.0-rc.1" }), /stable engine release/);
  assert.equal(decideRelease("0.1.0", "a", [previous], { engineVersion: "v0.90.0-rc.1" }).publish, false);
});

test("publication decisions ignore unit-only inputs and retain shipping and native gates", async t => {
  const root = mkdtempSync(join(tmpdir(), "typescript-release-inputs-"));
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("GIT_")));
  const files = {
    "sdk/typescript/package.json": '{"name":"@sparkwing/sdk","version":"0.1.0","files":["dist","index.d.ts","README.md"]}',
    "sdk/typescript/package-lock.json": '{"version":"0.1.0","packages":{"":{"version":"0.1.0"}},"lockfileVersion":3}',
    "sdk/typescript/README.md": "shipping README\n",
    "sdk/typescript/index.d.ts": "export declare const value: number;\n",
    "sdk/typescript/tsconfig.json": '{"compilerOptions":{"strict":true}}',
    "sdk/typescript/tsconfig.build.json": '{"include":["src"],"exclude":["test","examples"]}',
    "sdk/typescript/src/index.ts": "export const value = 1;\n",
    "sdk/typescript/native/protocol.c": "native bridge\n",
    "sdk/typescript/native/build.mjs": "native compiler\n",
    "sdk/typescript/native/check-package.mjs": "package validation\n",
    "sdk/typescript/native/release.mjs": "native publication gate\n",
    "sdk/typescript/native/verification.mjs": "native receipts\n",
    "sdk/typescript/test/nativehost/protocol_test.go": "native publication test\n",
    "sdk/typescript/test/nativehost/fixture.mjs": "native fixture\n",
    "sdk/typescript/test/nativehost/sdk_fixture_cache_test.go": "compiled native fixture package\n",
    "sdk/typescript/test/command.test.ts": "unit test\n",
    "sdk/typescript/test/helpers.ts": "unit helper\n",
    "sdk/typescript/test/fixtures/helper-vectors.json": "unit vectors\n",
    "sdk/typescript/test/testdata/commandhost/main.go": "unit command helper\n",
    "sdk/typescript/examples/hello/pipeline.ts": "excluded example\n",
    "sdk/typescript/IMPLEMENTATION.md": "unpublished design notes\n",
    "sdk/typescript/.prettierrc.json": '{}',
    "sdk/typescript/native/win32-x64/protocol.node": "generated bridge\n",
    "sdk/typescript/native/win32-arm64/verification.json": "generated receipt\n",
    "sdk/typescript/dist/index.js": "generated JS\n",
    ".github/workflows/release.yaml": "engine: one\n  typescript-native: node22\n  typescript-package: pack\n  release: engine\n",
  };
  const put = (path, body) => {
    mkdirSync(join(root, path, ".."), { recursive: true });
    writeFileSync(join(root, path), body);
  };
  try {
    for (const [path, body] of Object.entries(files)) put(path, body);
    execFileSync("git", ["-C", root, "init", "--quiet"], { env });
    execFileSync("git", ["-C", root, "add", "."], { env });
    const first = sdkDigest(root), entry = { ...previous, source_digest: first };
    const ignored = [
      "sdk/typescript/test/command.test.ts", "sdk/typescript/test/helpers.ts",
      "sdk/typescript/test/fixtures/helper-vectors.json", "sdk/typescript/test/testdata/commandhost/main.go",
      "sdk/typescript/examples/hello/pipeline.ts", "sdk/typescript/IMPLEMENTATION.md", "sdk/typescript/.prettierrc.json",
      "sdk/typescript/native/win32-x64/protocol.node", "sdk/typescript/native/win32-arm64/verification.json", "sdk/typescript/dist/index.js",
    ];
    for (const path of ignored) await t.test(`excluded: ${path}`, () => {
      try {
        put(path, files[path] + "changed\n");
        assert.deepEqual(decideRelease("0.1.0", sdkDigest(root), [entry]), { publish: false, entry });
      } finally { put(path, files[path]); }
    });
    for (const path of Object.keys(files).filter(path => !ignored.includes(path))) await t.test(`required: ${path}`, () => {
      try {
        const body = path === ".github/workflows/release.yaml" ? files[path].replace("node22", "node24")
          : path.endsWith("/package.json") || path.endsWith("/package-lock.json")
          ? JSON.stringify({ ...JSON.parse(files[path]), description: "changed input" })
          : files[path] + "changed\n";
        put(path, body);
        const digest = sdkDigest(root);
        assert.throws(() => decideRelease("0.1.0", digest, [entry]), /already names different source/);
        assert.equal(decideRelease("0.2.0", digest, [entry]).publish, true);
      } finally { put(path, files[path]); }
    });
    put("policy.json", JSON.stringify({ sdks: [entry] }));
    put("sdk/typescript/test/command.test.ts", files["sdk/typescript/test/command.test.ts"] + "test change\n");
    const command = [fileURLToPath(new URL("./typescript-release.mjs", import.meta.url)), root, join(root, "policy.json"), join(root, "entry.json"), root];
    for (const event of ["", "push"]) await t.test(`shared CLI decision: ${event || "pre-tag"}`, () => {
      const options = { env: { ...env, GITHUB_EVENT_NAME: event, GITHUB_OUTPUT: "" }, stdio: "pipe" };
      assert.deepEqual(JSON.parse(execFileSync(process.execPath, command, options)), { publish: false, entry });
      put("sdk/typescript/src/index.ts", files["sdk/typescript/src/index.ts"] + "export const changed = true;\n");
      try {
        assert.throws(() => execFileSync(process.execPath, command, options), error => /already names different source/.test(error.stderr.toString()));
      } finally { put("sdk/typescript/src/index.ts", files["sdk/typescript/src/index.ts"]); }
    });
  } finally { rmSync(root, { recursive: true, force: true }); }
});
