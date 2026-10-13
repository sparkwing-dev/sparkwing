import { fstatSync } from "node:fs";
import { Socket } from "node:net";
import { PROTOCOL_ARG, take } from "./startup.ts";
import { NodeRoutes, routeConfigFromEnv } from "./routes.ts";
import { serve } from "./runner.ts";

export { PROTOCOL_ARG };

const REQUEST_FD = 3;
const FRAME_FD = 4;

// A pipe is written asynchronously, so process.exit before the queue empties
// drops whatever was written last. An empty write's callback fires after
// every earlier write; 'drain' is not a barrier, since it fires only after a
// write that returned false. The loop catches writes a drain handler queues.
async function drained(stream: NodeJS.WritableStream & { writableLength: number }): Promise<void> {
  do {
    await new Promise<void>((resolve) => stream.write("", () => resolve()));
    await new Promise<void>((resolve) => setImmediate(resolve));
  } while (stream.writableLength > 0);
}

// safety: Node.js calls libuv's uv_disable_stdio_inheritance at startup, which
// marks every inherited descriptor close-on-exec, so a command pipeline code
// starts never holds the protocol pipes. The pipes test guards it.
function protocolPipe(fd: number): number {
  let fifo = false;
  try {
    fifo = fstatSync(fd).isFIFO();
  } catch {
    // A closed descriptor reads as no pipe.
  }
  if (!fifo) throw new Error(`descriptor ${fd} is not a pipe; only a sparkwing host starts ${PROTOCOL_ARG}`);
  return fd;
}

/**
 * The entry point a pipeline file calls after defining its pipelines. With
 * --sw-node-protocol it answers engine requests from the request pipe until
 * that pipe ends, then exits the process, so a timer or server a body left
 * running cannot keep the node alive after the engine has stopped asking.
 */
export async function main(argv: string[] = process.argv.slice(2)): Promise<number> {
  if (argv.length !== 1 || argv[0] !== PROTOCOL_ARG) {
    process.stderr.write(`usage: <pipeline file> ${PROTOCOL_ARG}\nOnly a sparkwing host starts a pipeline entry.\n`);
    return 2;
  }
  let input: Socket;
  let frames: Socket;
  let routes: NodeRoutes | undefined;
  try {
    if (process.platform === "win32") {
      throw new Error("the TypeScript SDK does not yet open the Windows protocol handles");
    }
    const boot = take();
    if (!boot.preloaded) {
      throw new Error(`the entry was not started with node --import @sparkwing/sdk/bootstrap; only a sparkwing host starts ${PROTOCOL_ARG}`);
    }
    const cfg = routeConfigFromEnv(boot.env);
    if (cfg) routes = new NodeRoutes(cfg);
    input = new Socket({
      fd: protocolPipe(REQUEST_FD),
      readable: true,
      writable: false,
    });
    frames = new Socket({
      fd: protocolPipe(FRAME_FD),
      readable: false,
      writable: true,
    });
  } catch (err) {
    process.stderr.write(`sw-node-protocol: ${err instanceof Error ? err.message : String(err)}\n`);
    return 2;
  }
  let status = 0;
  try {
    await serve(routes ? { input, output: frames, routes } : { input, output: frames });
  } catch (err) {
    process.stderr.write(`sw-node-protocol: ${err instanceof Error ? err.message : String(err)}\n`);
    status = 1;
  }
  await Promise.all([drained(frames), drained(process.stdout), drained(process.stderr)]);
  process.exit(status);
}
