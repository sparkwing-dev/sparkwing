import { createReadStream } from "node:fs";
import { realpath, stat } from "node:fs/promises";
import { createServer, request } from "node:http";
import { dirname, extname, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../web/out");
const port = 4344;
const hosts = new Set([`localhost:${port}`, `127.0.0.1:${port}`]);
const types = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json",
  ".txt": "text/plain; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
};

function reply(res, status, message) {
  res.writeHead(status, { "content-type": "text/plain; charset=utf-8" });
  res.end(message);
}

const server = createServer(async (req, res) => {
  res.setHeader("cache-control", "no-store");
  res.setHeader("x-content-type-options", "nosniff");
  if (!hosts.has(req.headers.host)) return reply(res, 403, "Loopback preview only");
  if (req.method !== "GET" && req.method !== "HEAD") {
    return reply(res, 405, "Preview is read-only; actions are disabled");
  }

  let pathname;
  try {
    pathname = decodeURIComponent(req.url.split("?")[0]);
  } catch {
    return reply(res, 400, "Invalid path");
  }
  if (
    !pathname.startsWith("/") ||
    pathname.includes("\\") ||
    pathname.includes("\0") ||
    pathname.split("/").some((part) => part === ".." || part === ".")
  ) {
    return reply(res, 400, "Invalid path");
  }

  if (pathname.startsWith("/api/")) {
    if (req.method !== "GET") return reply(res, 405, "API preview supports GET only");
    const upstream = request({
      hostname: "127.0.0.1",
      port: 4343,
      method: "GET",
      path: req.url,
      headers: { accept: req.headers.accept || "*/*" },
    }, (response) => {
      res.writeHead(response.statusCode || 502, {
        "content-type": response.headers["content-type"] || "application/json",
      });
      response.on("error", () => res.destroy());
      response.pipe(res);
    });
    upstream.on("error", () => {
      if (res.headersSent) res.destroy();
      else reply(res, 502, "Live dashboard API is unavailable on port 4343");
    });
    upstream.setTimeout(30_000, () => upstream.destroy());
    res.on("close", () => upstream.destroy());
    upstream.end();
    return;
  }

  if (pathname === "/") {
    res.writeHead(302, { location: "/runs?variant=focus" });
    res.end();
    return;
  }

  if (pathname === "/sparkwing-runtime.js") {
    res.writeHead(200, { "content-type": types[".js"] });
    res.end(req.method === "HEAD" ? undefined :
      "window.__SPARKWING_VERSION__='UI prototype';window.__SPARKWING_REQUIRE_LOGIN__='false';\n");
    return;
  }

  const base = resolve(root, `.${pathname}`);
  const candidates = extname(pathname)
    ? [base]
    : [base, `${base}.html`, resolve(base, "index.html")];
  for (const candidate of candidates) {
    try {
      const path = await realpath(candidate);
      if (!path.startsWith(root + sep)) continue;
      const info = await stat(path);
      if (!info.isFile()) continue;
      res.writeHead(200, {
        "content-type": types[extname(path)] || "application/octet-stream",
        "content-length": info.size,
      });
      if (req.method === "HEAD") res.end();
      else createReadStream(path).on("error", () => res.destroy()).pipe(res);
      return;
    } catch (error) {
      if (error.code !== "ENOENT" && error.code !== "ENOTDIR") {
        return reply(res, 500, "Unable to read preview assets");
      }
    }
  }
  reply(res, 404, "Preview asset not found; build the candidate to populate web/out");
});

server.listen(port, "127.0.0.1", () => {
  console.log(`UI prototype: http://127.0.0.1:${port}/runs?variant=focus`);
  console.log(`Assets: ${root}; GET-only API: http://127.0.0.1:4343`);
});
for (const signal of ["SIGTERM", "SIGINT"]) {
  process.on(signal, () => {
    server.close();
    server.closeAllConnections();
  });
}
