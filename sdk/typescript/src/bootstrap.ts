// The trusted preload a host starts every pipeline entry with:
// node --import <this module> <entry> --sw-node-protocol. Node.js evaluates
// it before it loads the entry, so the bearer and the handle list leave
// process.env before any entry or sibling-import code runs, whatever order
// that code imports in. It imports only Node.js built-ins for the same reason.

import { realpathSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

/** The one argument the engine starts an SDK process with. */
export const PROTOCOL_ARG = "--sw-node-protocol";

export const NODE_URL_ENV = "SPARKWING_NODE_URL";
export const NODE_TOKEN_ENV = "SPARKWING_NODE_TOKEN";
const HANDLES_ENV = "SPARKWING_PROTOCOL_HANDLES";

const env: Record<string, string | undefined> = {};
let preloaded = false;
if (process.argv.length === 3 && process.argv[2] === PROTOCOL_ARG) {
  env[NODE_URL_ENV] = process.env[NODE_URL_ENV];
  env[NODE_TOKEN_ENV] = process.env[NODE_TOKEN_ENV];
  delete process.env[NODE_TOKEN_ENV];
  delete process.env[HANDLES_ENV];
  preloaded = importedByFlag();
}

// safety: the same module reached through the entry's own imports runs only
// after every module the entry imports first, so main refuses to serve
// unless Node.js loaded this file from an --import flag.
function importedByFlag(): boolean {
  const self = fileURLToPath(import.meta.url);
  const argv = process.execArgv;
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i] ?? "";
    const value = arg === "--import" ? argv[i + 1] : arg.startsWith("--import=") ? arg.slice("--import=".length) : undefined;
    if (value === undefined) continue;
    try {
      if (realpathSync(value.startsWith("file:") ? fileURLToPath(value) : resolve(value)) === self) return true;
    } catch {
      // A specifier that names no file is some other preload.
    }
  }
  return false;
}

/**
 * The node routes' variables as the host passed them, and whether this
 * module ran as the --import preload, before any entry code.
 */
export function startup(): { env: Readonly<Record<string, string | undefined>>; preloaded: boolean } {
  return { env, preloaded };
}
