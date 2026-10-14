// Holds what the bootstrap preload captured until main takes it. The package
// exports neither this module nor anything that reads it, so entry code that
// imports the SDK or its bootstrap never gets the bearer to hand a child.

/** The one argument the engine starts an SDK process with. */
export const PROTOCOL_ARG = "--sw-node-protocol";

export const NODE_URL_ENV = "SPARKWING_NODE_URL";
export const NODE_TOKEN_ENV = "SPARKWING_NODE_TOKEN";

let captured: { env: Record<string, string | undefined>; preloaded: boolean } | undefined;

/** Records the preload's capture; only bootstrap.ts calls it. */
export function record(env: Record<string, string | undefined>, preloaded: boolean): void {
  captured ??= { env, preloaded };
}

/**
 * Returns the node routes' variables as the host passed them, and whether the
 * bootstrap ran as the --import preload, then forgets the bearer.
 */
export function take(): { env: Record<string, string | undefined>; preloaded: boolean } {
  const out = captured ?? { env: {}, preloaded: false };
  captured = { env: {}, preloaded: out.preloaded };
  return out;
}
