import { createRequire } from "node:module";

type WindowsBridge = (request: bigint, frame: bigint) => readonly [number, number];
interface Descriptors {
  requestFd: number;
  frameFd: number;
}
const protocolArg = "--sw-node-protocol";
const handlesArg = "--sw-protocol-handles";

export function protocolDescriptors(
  argv: readonly string[],
  platform: NodeJS.Platform = process.platform,
  bridge: WindowsBridge = windowsDescriptors,
): Descriptors {
  let protocol = false;
  let handles: string | undefined;
  for (let index = 0; index < argv.length; index++) {
    const arg = argv[index];
    if (arg === protocolArg && !protocol) protocol = true;
    else if (arg === handlesArg && handles === undefined && platform === "win32") {
      handles = argv[++index];
      if (handles === undefined) throw new Error(`${handlesArg} requires request,frame handles`);
    } else throw new Error(`unexpected protocol argument: ${arg}`);
  }
  if (!protocol) throw new Error(`pipeline entry requires ${protocolArg}`);
  if (platform !== "win32") return { requestFd: 3, frameFd: 4 };
  if (handles === undefined || !/^\d+,\d+$/.test(handles)) throw new Error(`${handlesArg} requires two decimal handles`);
  const [request, frame] = handles.split(",").map((value) => BigInt(value));
  const maximum = (1n << 64n) - 1n;
  if (
    request === undefined ||
    frame === undefined ||
    request === frame ||
    request <= 0n ||
    frame <= 0n ||
    request >= maximum ||
    frame >= maximum
  ) {
    throw new Error(`${handlesArg} requires distinct valid handles`);
  }
  const [requestFd, frameFd] = bridge(request, frame);
  return { requestFd, frameFd };
}

function windowsDescriptors(request: bigint, frame: bigint): readonly [number, number] {
  const require = createRequire(import.meta.url);
  let bridge: { open: WindowsBridge };
  try {
    bridge = require(`../native/win32-${process.arch}/protocol.node`) as { open: WindowsBridge };
  } catch (cause) {
    throw new Error(`cannot load the SDK's Windows ${process.arch} protocol bridge`, { cause });
  }
  return bridge.open(request, frame);
}
