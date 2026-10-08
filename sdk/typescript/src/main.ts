import { emitDescribe } from "./describe.ts";
import { serve } from "./runner.ts";

// A pipe is written asynchronously, so process.exit before the queue empties
// drops whatever the body logged last. An empty write's callback fires after
// every earlier write; 'drain' is not a barrier, since it fires only after a
// write that returned false. The loop catches writes a drain handler queues.
async function drained(stream: NodeJS.WriteStream): Promise<void> {
  do {
    await new Promise<void>((resolve) => stream.write("", () => resolve()));
    await new Promise<void>((resolve) => setImmediate(resolve));
  } while (stream.writableLength > 0);
}

/**
 * The entry point a pipeline file calls after defining its pipelines.
 * `--describe` prints the describe document; `--sw-node-protocol` answers
 * engine requests on stdin and stdout until stdin ends, then exits the
 * process, so a timer or server a body left running cannot keep the node
 * alive after the engine has stopped asking.
 */
export async function main(argv: string[] = process.argv.slice(2)): Promise<number> {
  if (argv.length === 1 && argv[0] === "--describe") {
    emitDescribe(process.stdout);
    return 0;
  }
  if (argv.length === 1 && argv[0] === "--sw-node-protocol") {
    await serve({ input: process.stdin, output: process.stdout });
    await Promise.all([drained(process.stdout), drained(process.stderr)]);
    process.exit(0);
  }
  process.stderr.write("usage: <pipeline file> --describe | --sw-node-protocol\n");
  return 2;
}
