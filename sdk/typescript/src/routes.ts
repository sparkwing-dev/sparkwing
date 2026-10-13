// Thin clients for the /node/v1 routes the engine serves to a node-bound
// process. The host resolves secrets and registers them with its masker
// before it answers, so the SDK never masks anything itself.

import { NODE_TOKEN_ENV, NODE_URL_ENV } from "./bootstrap.ts";
import { PROTOCOL_VERSION } from "./describe.ts";

export { NODE_TOKEN_ENV, NODE_URL_ENV };

export interface NodeRouteConfig {
  baseUrl: string;
  token: string;
  fetch?: typeof fetch;
}

/**
 * Reads the route location and bearer from the node environment the engine
 * sets. It returns undefined for a process bound to no node, which gets
 * neither variable.
 */
export function routeConfigFromEnv(env: Record<string, string | undefined>): NodeRouteConfig | undefined {
  const baseUrl = env[NODE_URL_ENV] ?? "";
  const token = env[NODE_TOKEN_ENV] ?? "";
  if (baseUrl === "" && token === "") return undefined;
  if (!loopback(baseUrl)) {
    // safety: the URL is not echoed, since a wrong one may carry userinfo.
    throw new Error(`${NODE_URL_ENV} must be http://127.0.0.1:<port>, http://[::1]:<port> or http://localhost:<port>`);
  }
  if (token === "") throw new Error(`${NODE_TOKEN_ENV} is empty`);
  return { baseUrl, token };
}

function loopback(raw: string): boolean {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return false;
  }
  if (u.protocol !== "http:" || u.username !== "" || u.password !== "" || u.search !== "" || u.hash !== "" || u.pathname !== "/")
    return false;
  return u.port !== "" && ["127.0.0.1", "[::1]", "localhost"].includes(u.hostname);
}

/** A route answered with an error; code is the route's error code, such as secret_not_found. */
export class RouteError extends Error {
  readonly status: number;
  readonly code: string | undefined;

  constructor(route: string, status: number, message: string, code: string | undefined) {
    super(`${route}: HTTP ${status}${code ? ` ${code}` : ""}${message ? `: ${message}` : ""}`);
    this.status = status;
    this.code = code;
  }
}

export class NodeRoutes {
  readonly #base: string;
  readonly #token: string;
  readonly #fetch: typeof fetch;

  constructor(cfg: NodeRouteConfig) {
    this.#base = cfg.baseUrl.replace(/\/+$/, "");
    this.#token = cfg.token;
    this.#fetch = cfg.fetch ?? fetch;
  }

  /** Returns the secret's value; an unset secret is an error, never an empty string. */
  async secret(name: string, signal?: AbortSignal): Promise<string> {
    const body = (await this.#route("GET", `/node/v1/secrets/${encodeURIComponent(name)}`, signal)) as { value?: unknown } | null;
    if (typeof body?.value !== "string") {
      throw new Error(`secret ${name}: the engine answered without a string value`);
    }
    return body.value;
  }

  /** Returns the JSON output another node of this run produced, or null when it recorded none. */
  async output<T = unknown>(nodeId: string, signal?: AbortSignal): Promise<T | null> {
    const body = (await this.#route("GET", `/node/v1/nodes/${encodeURIComponent(nodeId)}/output`, signal)) as { output?: unknown } | null;
    if (body === null || !("output" in body)) throw new Error(`output of ${nodeId}: the engine answered without an output field`);
    return (body.output ?? null) as T | null;
  }

  async #route(method: string, path: string, signal: AbortSignal | undefined): Promise<unknown> {
    const headers: Record<string, string> = {
      accept: "application/json",
      authorization: `Bearer ${this.#token}`,
      "sparkwing-node-protocol": PROTOCOL_VERSION,
    };
    // safety: a redirect could carry the bearer to another local service, so
    // none is followed and a 3xx is an error.
    const res = await this.#fetch(this.#base + path, {
      method,
      headers,
      redirect: "manual",
      signal: signal ?? null,
    });
    const text = await res.text();
    if (res.status !== 200) {
      let parsed: { error?: unknown; code?: unknown } = {};
      try {
        parsed = JSON.parse(text) as typeof parsed;
      } catch {
        // A body that is not the route error shape keeps only the status.
      }
      const message = typeof parsed.error === "string" ? parsed.error : "";
      throw new RouteError(`${method} ${path}`, res.status, message, typeof parsed.code === "string" ? parsed.code : undefined);
    }
    return text === "" ? null : JSON.parse(text);
  }
}
