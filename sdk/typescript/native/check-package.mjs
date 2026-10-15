import { readFile } from "node:fs/promises";

for (const [arch, machine] of [["x64", 0x8664], ["arm64", 0xaa64]]) {
  let bytes;
  try {
    bytes = await readFile(new URL(`win32-${arch}/protocol.node`, import.meta.url));
  } catch {
    throw new Error(`Build the Windows ${arch} protocol bridge before packaging: node native/build.mjs --headers <node-headers> --node-lib <node.lib> --arch ${arch}`);
  }
  const invalid = () => new Error(`Windows ${arch} protocol bridge has an invalid or mismatched PE32+ DLL header; rebuild it before packaging`);
  if (bytes.length < 64 || bytes.toString("ascii", 0, 2) !== "MZ") throw invalid();
  const pe = bytes.readUInt32LE(0x3c);
  if (pe < 64 || pe + 26 > bytes.length || bytes.toString("ascii", pe, pe + 4) !== "PE\0\0") throw invalid();
  const sections = bytes.readUInt16LE(pe + 6);
  const headerSize = bytes.readUInt16LE(pe + 20);
  if (bytes.readUInt16LE(pe + 4) !== machine ||
      (bytes.readUInt16LE(pe + 22) & 0x2002) !== 0x2002 ||
      bytes.readUInt16LE(pe + 24) !== 0x20b || headerSize < 112 || sections === 0 ||
      pe + 24 + headerSize + sections * 40 > bytes.length) throw invalid();
}
