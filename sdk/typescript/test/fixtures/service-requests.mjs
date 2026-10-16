import { NodeRoutes } from "../../src/routes.ts";

const requests = [];
const routes = new NodeRoutes({ url: "http://127.0.0.1:12345", bearer: "fixture" }, async (url, options) => {
  const path = new URL(url).pathname;
  requests.push({ method: options.method, path, ...(options.body === undefined ? {} : { body: JSON.parse(options.body) }) });
  const result = path.endsWith("/secrets/empty")
    ? { value: "", masked: false }
    : path.endsWith("/output") || path.endsWith("/latest")
      ? { output: null }
      : path.endsWith("/oidc-token")
        ? { token: "fixture" }
        : path.endsWith("/children")
          ? { run_id: "child" }
          : path.endsWith("/await")
            ? { status: "success", output: null }
            : path.endsWith("/cleanups")
              ? { id: "cleanup" }
              : {};
  return new Response(JSON.stringify(result));
});
const signal = new AbortController().signal;
await routes.secret("empty", signal);
await routes.output("empty", signal);
await routes.latest({ pipeline: "empty", jobId: "empty" }, signal);
await routes.annotate({ message: "" }, signal);
await routes.summary("", signal);
await routes.mask("", signal);
await routes.oidcToken("", signal);
await routes.runPipeline("", "", {}, signal);
const cleanup = await routes.cleanup({ argv: [], description: "" }, signal);
await cleanup.unregister();
process.stdout.write(JSON.stringify(requests));
