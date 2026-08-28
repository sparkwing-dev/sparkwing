import { readFile, stat } from "node:fs/promises";
import {
  createServer,
  type IncomingMessage,
  type Server,
  type ServerResponse,
} from "node:http";
import { extname, join, normalize } from "node:path";

const root = join(__dirname, "..", "..", "out");
const contentTypes = new Map([
  [".css", "text/css; charset=utf-8"],
  [".html", "text/html; charset=utf-8"],
  [".ico", "image/x-icon"],
  [".js", "text/javascript; charset=utf-8"],
  [".json", "application/json; charset=utf-8"],
  [".svg", "image/svg+xml"],
  [".woff2", "font/woff2"],
]);

async function existingFile(pathname: string): Promise<string> {
  const clean = normalize(decodeURIComponent(pathname)).replace(
    /^(\.\.(\/|\\|$))+/, "",
  );
  const relative = clean.replace(/^[/\\]+/, "");
  const candidates = relative
    ? [relative, `${relative}.html`, join(relative, "index.html")]
    : ["index.html"];
  for (const candidate of candidates) {
    const filename = join(root, candidate);
    try {
      if ((await stat(filename)).isFile()) return filename;
    } catch {}
  }
  return join(root, "index.html");
}

export type StaticDashboardRequestHandler = (
  request: IncomingMessage,
  response: ServerResponse,
) => boolean | Promise<boolean>;

function serveStatic(
  server: Server,
  requestHandler?: StaticDashboardRequestHandler,
): void {
  server.on("request", async (request, response) => {
    try {
      if (requestHandler && (await requestHandler(request, response))) return;
      if (request.method !== "GET" && request.method !== "HEAD") {
        response.writeHead(405).end();
        return;
      }
      const pathname = new URL(request.url ?? "/", "http://localhost").pathname;
      const filename = await existingFile(pathname);
      const body = await readFile(filename);
      response.writeHead(200, {
        "Cache-Control": "no-store",
        "Content-Type":
          contentTypes.get(extname(filename)) || "application/octet-stream",
      });
      response.end(request.method === "HEAD" ? undefined : body);
    } catch (error) {
      response.writeHead(500, { "Content-Type": "text/plain; charset=utf-8" });
      response.end(error instanceof Error ? error.message : String(error));
    }
  });
}

export type StaticDashboard = {
  origin: string;
  close: () => Promise<void>;
};

export async function startStaticDashboard(
  requestHandler?: StaticDashboardRequestHandler,
): Promise<StaticDashboard> {
  const server = createServer();
  serveStatic(server, requestHandler);
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      resolve();
    });
  });
  const address = server.address();
  if (!address || typeof address === "string") {
    await new Promise<void>((resolve) => server.close(() => resolve()));
    throw new Error("static dashboard server did not allocate a TCP port");
  }
  let closing: Promise<void> | undefined;
  return {
    origin: `http://127.0.0.1:${address.port}`,
    close: () => {
      if (closing) return closing;
      closing = new Promise<void>((resolve, reject) => {
        server.close((error) => (error ? reject(error) : resolve()));
        server.closeAllConnections();
      });
      return closing;
    },
  };
}
