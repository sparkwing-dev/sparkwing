// The trusted preload a host starts every pipeline entry with:
// node --import <this module> <entry> --sw-node-protocol. Node.js evaluates
// it before it loads the entry, so the bearer and the handle list leave
// process.env before any entry or sibling-import code runs, whatever order
// that code imports in. It imports only Node.js built-ins and its own
// startup module for the same reason. It exports nothing: what it captures
// goes to that unexported module, which main empties.
//
// The preload keeps the bearer from the tools a step starts, not from the
// repository's own code: that code, node_modules included, is trusted with
// its node's bearer, and can choose the installed SDK or NODE_OPTIONS.

import { realpathSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { NODE_TOKEN_ENV, NODE_URL_ENV, PROTOCOL_ARG, record } from "./startup.ts";

const HANDLES_ENV = "SPARKWING_PROTOCOL_HANDLES";

if (process.argv.length === 3 && process.argv[2] === PROTOCOL_ARG) {
  const env = { [NODE_URL_ENV]: process.env[NODE_URL_ENV], [NODE_TOKEN_ENV]: process.env[NODE_TOKEN_ENV] };
  delete process.env[NODE_TOKEN_ENV];
  delete process.env[HANDLES_ENV];
  record(env, importedByFlag());
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
