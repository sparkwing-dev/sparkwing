// Thin clients for the node-facing routes the engine's node host serves.
// The host resolves secrets and registers them with its masker before it
// answers, so the SDK never masks anything itself.

import { createHash } from "node:crypto";
import { PROTOCOL_VERSION } from "./describe.ts";

export interface NodeRouteConfig {
  baseUrl: string;
  token?: string;
  fetch?: typeof fetch;
}

/** Reads the route location from the node environment the engine sets. */
export function routeConfigFromEnv(env: Record<string, string | undefined>): NodeRouteConfig {
  const baseUrl = env["SPARKWING_CONTROLLER_URL"];
  if (!baseUrl) {
    throw new Error("SPARKWING_CONTROLLER_URL is not set; the engine did not start this process as a node");
  }
  const token = env["SPARKWING_AGENT_TOKEN"];
  return token ? { baseUrl, token } : { baseUrl };
}

export class RouteError extends Error {
  readonly status: number;

  constructor(route: string, status: number, body: string) {
    super(`${route}: HTTP ${status}${body ? `: ${body}` : ""}`);
    this.status = status;
  }
}

interface OutputGrant {
  url?: string;
  sha256?: string;
  size?: number;
}

export class NodeRoutes {
  readonly #base: string;
  readonly #token: string | undefined;
  readonly #fetch: typeof fetch;

  constructor(cfg: NodeRouteConfig) {
    this.#base = cfg.baseUrl.replace(/\/+$/, "");
    this.#token = cfg.token;
    this.#fetch = cfg.fetch ?? fetch;
  }

  /** Returns the secret's value; an unset secret is an error, never an empty string. */
  async secret(name: string): Promise<string> {
    const body = (await this.#route("GET", `/node/v1/secrets/${encodeURIComponent(name)}`)) as { value?: unknown } | null;
    if (typeof body?.value !== "string") {
      throw new Error(`secret ${name}: the engine answered without a string value`);
    }
    return body.value;
  }

  /**
   * Returns the JSON output another node of this run produced, or null when
   * it recorded none. The route answers with a signed grant; the bytes come
   * from the grant's URL and must match its size and sha256, so a grant that
   * names an object without both is refused.
   */
  async output<T = unknown>(nodeId: string): Promise<T | null> {
    const route = `/node/v1/nodes/${encodeURIComponent(nodeId)}/output`;
    const grant = (await this.#route("GET", route)) as OutputGrant | null;
    if (!grant?.url) return null;
    if (typeof grant.sha256 !== "string" || grant.sha256 === "" || typeof grant.size !== "number") {
      throw new Error(`output of ${nodeId}: the grant names an object without its size and sha256`);
    }
    const res = await this.#fetch(new URL(grant.url, this.#base + "/"));
    const bytes = Buffer.from(await res.arrayBuffer());
    if (!res.ok) throw new RouteError(`GET output of ${nodeId}`, res.status, bytes.toString("utf8").slice(0, 512));
    if (bytes.length !== grant.size) {
      throw new Error(`output of ${nodeId}: ${bytes.length} bytes, the grant says ${grant.size}`);
    }
    const got = createHash("sha256").update(bytes).digest("hex");
    if (got !== grant.sha256) throw new Error(`output of ${nodeId}: sha256 ${got}, the grant says ${grant.sha256}`);
    return JSON.parse(bytes.toString("utf8")) as T;
  }

  async #route(method: string, path: string): Promise<unknown> {
    const headers: Record<string, string> = {
      accept: "application/json",
      "sparkwing-node-protocol": PROTOCOL_VERSION,
    };
    if (this.#token) headers["authorization"] = `Bearer ${this.#token}`;
    const res = await this.#fetch(this.#base + path, { method, headers });
    const text = await res.text();
    if (!res.ok) throw new RouteError(`${method} ${path}`, res.status, text.slice(0, 512));
    return text === "" ? null : JSON.parse(text);
  }
}
