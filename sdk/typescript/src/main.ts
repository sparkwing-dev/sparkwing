import { emitDescribe } from "./describe.ts";
import { serve } from "./runner.ts";

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
    await new Promise<void>((resolve) => process.stdout.write("", () => resolve()));
    process.exit(0);
  }
  process.stderr.write("usage: <pipeline file> --describe | --sw-node-protocol\n");
  return 2;
}
