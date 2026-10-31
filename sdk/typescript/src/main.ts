import { createReadStream, fstatSync, writeSync } from "node:fs";
import { protocolDescriptors } from "./transport.ts";
import { serve } from "./runner.ts";

async function drained(stream: NodeJS.WritableStream & { writableLength: number }): Promise<void> {
  do {
    await new Promise<void>((resolve, reject) => stream.write("", (error) => (error ? reject(error) : resolve())));
    await new Promise<void>((resolve) => setImmediate(resolve));
  } while (stream.writableLength > 0);
}
function pipe(fd: number): number {
  // bug: Node's numeric pipe stats can make realpath stop before resolving package symlinks.
  const stat = fstatSync(fd, { bigint: true });
  if (!stat.isFIFO() && !stat.isSocket()) throw new Error("protocol descriptor must be a pipe");
  return fd;
}
function initialize(): { descriptors?: { requestFd: number; frameFd: number }; error?: unknown } {
  try {
    return { descriptors: protocolDescriptors(process.argv.slice(2)) };
  } catch (error) {
    return { error };
  }
}
// safety: Conversion clears Windows handle inheritance before SDK tools spawn; independent imports may run earlier.
const inherited = process.argv.includes("--sw-node-protocol") ? initialize() : undefined;
export async function main(): Promise<void> {
  let descriptors: { requestFd: number; frameFd: number };
  try {
    const initialized = inherited ?? initialize();
    if (initialized.error) throw initialized.error;
    descriptors = initialized.descriptors!;
    // Windows handles are checked by the bridge; Node's FIFO/socket predicates always return false there.
    if (process.platform !== "win32") {
      pipe(descriptors.requestFd);
      pipe(descriptors.frameFd);
    }
  } catch (error) {
    process.stderr.write(`sparkwing: ${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 2;
    return;
  }
  let status = 0;
  const input = createReadStream("", { fd: descriptors.requestFd, autoClose: false });
  try {
    await serve({
      input,
      output: {
        write(line) {
          const bytes = Buffer.from(line);
          for (let offset = 0; offset < bytes.length;) offset += writeSync(descriptors.frameFd, bytes, offset);
        },
      },
    });
  } catch (error) {
    process.stderr.write(`sparkwing: ${error instanceof Error ? error.message : String(error)}\n`);
    status = 1;
  } finally {
    input.destroy();
  }
  await Promise.all([drained(process.stdout), drained(process.stderr)]);
  process.exit(status);
}
