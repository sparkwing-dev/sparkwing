import { parseJSON } from "./json.ts";
import type { Annotation, ChildOptions, CleanupRegistration, CleanupSpec, PipelineRef } from "../index.d.ts";
import { decodeError, failure, object, string } from "./errors.ts";
import { assertExecution } from "./scope.ts";

export interface ProcessRoutes {
  url: string;
  bearer: string;
}
export interface HostServices {
  secret(name: string, signal: AbortSignal): Promise<string>;
  output<T>(job: string, signal: AbortSignal): Promise<T | undefined>;
  latest<T>(ref: PipelineRef<T>, signal: AbortSignal): Promise<T>;
  annotate(annotation: Annotation, signal: AbortSignal, step?: string): Promise<void>;
  summary(markdown: string, signal: AbortSignal, step?: string): Promise<void>;
  mask(value: string, signal: AbortSignal): Promise<void>;
  oidcToken(audience: string, signal: AbortSignal): Promise<string>;
  runPipeline<T, In extends object>(pipeline: string, job: string, options: ChildOptions<In>, signal: AbortSignal): Promise<T>;
  cleanup(spec: CleanupSpec, signal: AbortSignal): Promise<CleanupRegistration>;
}

export class NodeRoutes implements HostServices {
  readonly #base: string;
  readonly #bearer: string;
  readonly #fetch: typeof fetch;
  constructor(routes: ProcessRoutes, request: typeof fetch = fetch) {
    let url: URL;
    try {
      url = new URL(routes.url);
    } catch {
      throw failure("bad_request", "invalid hello routes URL");
    }
    if (
      url.protocol !== "http:" ||
      !["127.0.0.1", "localhost", "[::1]"].includes(url.hostname) ||
      !url.port ||
      url.username ||
      url.password ||
      url.search ||
      url.hash ||
      url.pathname !== "/"
    )
      throw failure("bad_request", "hello routes must name a loopback HTTP port");
    this.#base = url.origin;
    this.#bearer = string(routes.bearer, "routes.bearer");
    this.#fetch = request;
  }
  async #request(method: string, path: string, signal: AbortSignal, body?: unknown): Promise<Record<string, unknown>> {
    assertExecution();
    signal.throwIfAborted();
    const response = await this.#fetch(this.#base + path, {
      method,
      signal,
      redirect: "error",
      headers: {
        accept: "application/json",
        authorization: `Bearer ${this.#bearer}`,
        ...(body === undefined ? {} : { "content-type": "application/json" }),
      },
      ...(body === undefined
        ? {}
        : {
            body: JSON.stringify(body, (_key, value: unknown) => {
              if (value === null || (typeof value === "number" && !Number.isFinite(value)))
                throw failure("invalid_args", "service request fields cannot be null");
              return value;
            }),
          }),
    });
    const text = await response.text();
    if (response.status === 204 && response.ok) return {};
    let data: Record<string, unknown>;
    try {
      data = object(parseJSON(text), "route response");
    } catch {
      throw failure("protocol_error", `invalid JSON response (HTTP ${response.status})`);
    }
    if (!response.ok) throw decodeError(data.error);
    return data;
  }
  async secret(name: string, signal: AbortSignal): Promise<string> {
    const data = await this.#request("GET", `/node/v1/secrets/${encodeURIComponent(name)}`, signal);
    if (typeof data.value !== "string") throw failure("protocol_error", "secret response requires a string value");
    return data.value;
  }
  async output<T>(job: string, signal: AbortSignal): Promise<T | undefined> {
    const data = await this.#request("GET", `/node/v1/jobs/${encodeURIComponent(job)}/output`, signal);
    if (!("output" in data)) throw failure("protocol_error", "output response requires output");
    return data.output as T;
  }
  async latest<T>(ref: PipelineRef<T>, signal: AbortSignal): Promise<T> {
    const query = new URLSearchParams({ job: ref.jobId });
    if (ref.fromAnyRun) query.set("from_any_run", "true");
    if (ref.maxAgeMs !== undefined) query.set("max_age_ms", String(ref.maxAgeMs));
    const data = await this.#request("GET", `/node/v1/pipelines/${encodeURIComponent(ref.pipeline)}/latest?${query}`, signal);
    if (!("output" in data)) throw failure("protocol_error", "latest response requires output");
    return data.output as T;
  }
  async annotate(annotation: Annotation, signal: AbortSignal, step?: string): Promise<void> {
    const { endLine, ...fields } = annotation;
    await this.#request("POST", "/node/v1/annotations", signal, {
      ...fields,
      level: annotation.level ?? "info",
      ...(endLine === undefined ? {} : { end_line: endLine }),
      ...(step === undefined ? {} : { step }),
    });
  }
  async summary(markdown: string, signal: AbortSignal, step?: string): Promise<void> {
    await this.#request("POST", "/node/v1/summary", signal, { markdown, ...(step === undefined ? {} : { step }) });
  }
  async mask(value: string, signal: AbortSignal): Promise<void> {
    await this.#request("POST", "/node/v1/masks", signal, { value });
  }
  async oidcToken(audience: string, signal: AbortSignal): Promise<string> {
    return string((await this.#request("POST", "/node/v1/oidc-token", signal, { audience })).token, "OIDC token");
  }
  async runPipeline<T, In extends object>(pipeline: string, job: string, options: ChildOptions<In>, signal: AbortSignal): Promise<T> {
    const args: Record<string, string> = Object.create(null);
    for (const [key, value] of Object.entries(options.inputs ?? {})) {
      if (
        typeof value !== "string" &&
        typeof value !== "boolean" &&
        !(typeof value === "number" && Number.isFinite(value)) &&
        !(Array.isArray(value) && value.every((v) => typeof v === "string"))
      )
        throw failure("invalid_args", `child input ${key} must be a string, number, boolean or string array`);
      args[key] = Array.isArray(value) ? value.join(",") : String(value);
    }
    for (const [key, value] of Object.entries(options.args ?? {})) {
      if (typeof value !== "string") throw failure("invalid_args", `child argument ${key} must be a string`);
      args[key] = value;
    }
    const waiting = childWait(signal, options.timeoutMs);
    const waitSignal = waiting.signal;
    try {
      const child = await this.#request("POST", "/node/v1/children", waitSignal, {
        pipeline,
        args,
        ...(options.repo === undefined ? {} : { repo: options.repo, branch: options.branch ?? "main" }),
        ...(options.repo === undefined && options.branch !== undefined ? { branch: options.branch } : {}),
      });
      const id = string(child.run_id, "child.run_id");
      const query = new URLSearchParams({ job, wait_ms: "60000" });
      for (;;) {
        const result = await this.#request("GET", `/node/v1/children/${encodeURIComponent(id)}/await?${query}`, waitSignal);
        if (result.status === "success") {
          if (!("output" in result)) throw failure("protocol_error", "child result requires output");
          return result.output as T;
        }
        if (["failed", "cancelled", "superseded"].includes(String(result.status))) {
          if (result.error !== undefined) throw decodeError(result.error);
          throw failure("sdk_error", `child ${result.status}`);
        }
        if (!["pending", "running", "queued"].includes(String(result.status))) throw failure("protocol_error", "unknown child status");
      }
    } finally {
      waiting.close();
    }
  }

  async cleanup(spec: CleanupSpec, signal: AbortSignal): Promise<CleanupRegistration> {
    const id = string((await this.#request("POST", "/node/v1/cleanups", signal, spec)).id, "cleanup.id");
    return {
      id,
      unregister: async () => {
        await this.#request("DELETE", `/node/v1/cleanups/${encodeURIComponent(id)}`, new AbortController().signal);
      },
    };
  }
}

function childWait(parent: AbortSignal, timeoutMs: number | undefined): { signal: AbortSignal; close(): void } {
  if (timeoutMs !== undefined && (!Number.isSafeInteger(timeoutMs) || timeoutMs < 0))
    throw failure("invalid_args", "child timeout must be nonnegative milliseconds");
  if (!timeoutMs) return { signal: parent, close() {} };
  const controller = new AbortController();
  let timer: NodeJS.Timeout;
  const schedule = (remaining: number): void => {
    const duration = Math.min(remaining, 2 ** 31 - 1);
    timer = setTimeout(() => {
      if (remaining > duration) schedule(remaining - duration);
      else controller.abort(new Error("child wait timed out"));
    }, duration);
    timer.unref();
  };
  schedule(timeoutMs);
  return {
    signal: AbortSignal.any([parent, controller.signal]),
    close() {
      clearTimeout(timer);
    },
  };
}
