import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { digest, nativeTests, sourceDigest } from "./verification.mjs";

const manifest = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
const lock = JSON.parse(await readFile(new URL("../package-lock.json", import.meta.url), "utf8"));
if (
  typeof manifest.version !== "string" ||
  !manifest.version ||
  manifest.version === "0.0.0" ||
  lock.version !== manifest.version ||
  lock.packages?.[""]?.version !== manifest.version
) {
  throw new Error("Stamp the engine release version before packaging: npm version <engine-version> --no-git-tag-version");
}

for (const [arch, machine] of [
  ["x64", 0x8664],
  ["arm64", 0xaa64],
]) {
  let bytes;
  try {
    bytes = await readFile(new URL(`win32-${arch}/protocol.node`, import.meta.url));
  } catch {
    throw new Error(
      `Build the Windows ${arch} protocol bridge before packaging: node native/build.mjs --headers <node-headers> --node-lib <node.lib> --arch ${arch}`,
    );
  }
  const invalid = () =>
    new Error(`Windows ${arch} protocol bridge has an invalid or mismatched PE32+ DLL header; rebuild it before packaging`);
  if (bytes.length < 64 || bytes.toString("ascii", 0, 2) !== "MZ") throw invalid();
  const pe = bytes.readUInt32LE(0x3c);
  if (pe < 64 || pe + 26 > bytes.length || bytes.toString("ascii", pe, pe + 4) !== "PE\0\0") throw invalid();
  const sections = bytes.readUInt16LE(pe + 6);
  const headerSize = bytes.readUInt16LE(pe + 20);
  if (
    bytes.readUInt16LE(pe + 4) !== machine ||
    (bytes.readUInt16LE(pe + 22) & 0x2002) !== 0x2002 ||
    bytes.readUInt16LE(pe + 24) !== 0x20b ||
    headerSize < 112 ||
    sections === 0 ||
    pe + 24 + headerSize + sections * 40 > bytes.length
  )
    throw invalid();
}

let source;
for (const arch of ["x64", "arm64"]) {
  let receipt;
  try {
    receipt = JSON.parse(await readFile(new URL(`win32-${arch}/verification.json`, import.meta.url), "utf8"));
  } catch {
    throw new Error(
      `Windows ${arch} needs native verification: node native/release.mjs bridge --version ${manifest.version} --arch ${arch} --headers <node-headers> --node-lib <node.lib>`,
    );
  }
  source ??= await sourceDigest(fileURLToPath(new URL("..", import.meta.url)));
  const bytes = await readFile(new URL(`win32-${arch}/protocol.node`, import.meta.url));
  const node = /^v(\d+)\.(\d+)\.\d+/.exec(receipt?.node ?? "");
  if (
    !receipt ||
    receipt.platform !== "win32" ||
    receipt.arch !== arch ||
    receipt.version !== manifest.version ||
    receipt.source !== source ||
    receipt.sha256 !== digest(bytes) ||
    !node ||
    !(Number(node[1]) > 22 || (Number(node[1]) === 22 && Number(node[2]) >= 18)) ||
    JSON.stringify(receipt.tests) !== JSON.stringify(nativeTests)
  )
    throw new Error(`Windows ${arch} verification does not match the package; rerun native/release.mjs bridge`);
}
