// Thin clients for the node-facing HTTP routes the engine serves to a node
// process. The engine resolves secrets and registers them with its masker
// before answering, so the SDK never masks anything itself.

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

export class NodeRoutes {
  readonly #base: string;
  readonly #token: string | undefined;
  readonly #fetch: typeof fetch;

  constructor(cfg: NodeRouteConfig) {
    this.#base = cfg.baseUrl.replace(/\/+$/, "");
    this.#token = cfg.token;
    this.#fetch = cfg.fetch ?? fetch;
  }

  /** Returns the secret's value; a missing secret is an error, never an empty string. */
  async secret(name: string): Promise<string> {
    const body = (await this.#json("GET", `/node/v1/secrets/${encodeURIComponent(name)}`)) as { value?: unknown };
    if (typeof body.value !== "string") {
      throw new Error(`secret ${name}: the engine answered without a string value`);
    }
    return body.value;
  }

  /** Returns the output a finished node published. */
  async output<T = unknown>(nodeId: string): Promise<T> {
    return (await this.#json("GET", `/node/v1/outputs/${encodeURIComponent(nodeId)}`)) as T;
  }

  /** Publishes this node's output before the node returns. */
  async putOutput(value: unknown): Promise<void> {
    await this.#json("PUT", "/node/v1/outputs", value);
  }

  async #json(method: string, path: string, body?: unknown): Promise<unknown> {
    const headers: Record<string, string> = { accept: "application/json" };
    if (this.#token) headers["authorization"] = `Bearer ${this.#token}`;
    const init: RequestInit = { method, headers };
    if (body !== undefined) {
      headers["content-type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    const res = await this.#fetch(this.#base + path, init);
    const text = await res.text();
    if (!res.ok) throw new RouteError(`${method} ${path}`, res.status, text.slice(0, 512));
    return text === "" ? null : JSON.parse(text);
  }
}
