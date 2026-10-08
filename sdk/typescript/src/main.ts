import { emitDescribe } from "./describe.ts";
import { once } from "node:events";
import { serve } from "./runner.ts";

// A pipe is written asynchronously, so process.exit before the queue empties
// drops whatever the body logged last.
async function drained(stream: NodeJS.WriteStream): Promise<void> {
  await new Promise<void>((resolve) => stream.write("", () => resolve()));
  while (stream.writableLength > 0) await once(stream, "drain");
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
