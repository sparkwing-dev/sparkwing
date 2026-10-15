import { access } from "node:fs/promises";

for (const arch of ["x64", "arm64"]) {
  try {
    await access(new URL(`win32-${arch}/protocol.node`, import.meta.url));
  } catch {
    throw new Error(`Build the Windows ${arch} protocol bridge before packaging: node native/build.mjs --headers <node-headers> --node-lib <node.lib> --arch ${arch}`);
  }
}
