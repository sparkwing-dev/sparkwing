import { createHash } from "node:crypto";
import { readFile, readdir } from "node:fs/promises";
import { join } from "node:path";

export const nativeTests = ["TestNativeProtocol", "TestNativeConformance"];

export function passedNativeTests(output) {
  const events = output
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));
  if (
    events.some((event) => event.Action === "skip" || event.Action === "fail") ||
    !events.some((event) => event.Action === "pass" && !event.Test)
  )
    throw new Error("native test package must pass without skips");
  for (const name of nativeTests)
    if (!events.some((event) => event.Action === "pass" && event.Test === name))
      throw new Error(`native verification did not execute ${name}`);
  return [...nativeTests];
}

export function digest(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

export async function sourceDigest(root) {
  const hash = createHash("sha256");
  const part = (file, bytes) => hash.update(file + "\0" + bytes.length + "\0").update(bytes);
  async function visit(relative) {
    const entries = await readdir(join(root, relative), { withFileTypes: true });
    entries.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
    for (const entry of entries) {
      const file = `${relative}/${entry.name}`;
      if (entry.isDirectory()) await visit(file);
      else {
        part(file, await readFile(join(root, file)));
      }
    }
  }
  await visit("src");
  await visit("test/nativehost");
  for (const file of [
    "index.d.ts",
    "tsconfig.json",
    "tsconfig.build.json",
    "native/protocol.c",
    "native/build.mjs",
    "native/release.mjs",
    "native/verification.mjs",
  ])
    part(file, await readFile(join(root, file)));
  return hash.digest("hex");
}
