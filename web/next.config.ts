import type { NextConfig } from "next";

// Static export: `next build` emits HTML/JS/CSS to web/out/. The Go
// binary embeds that directory via //go:embed and serves it from
// `sparkwing web`. Runtime config (API token, controller URL) is
// injected by the Go server via HTML templating, not server-rendered
// by Next.js, because static export has no request lifecycle.
//
// Dev loop: `next dev` runs the Node runtime, so rewrites work to
// proxy /api/* to a separately-running `sparkwing-web` (default
// :4343). That gives HMR for UI iteration while real run data
// continues to flow through the Go side -- avoids the "rebuild +
// reinstall the whole binary on every CSS tweak" round trip.
//
// Static export ignores rewrites (they require a runtime), so we
// gate `output: "export"` and the rewrites on NODE_ENV: prod builds
// emit static files (no rewrites), dev runs the runtime (rewrites
// proxy the API).
const isDev = process.env.NODE_ENV === "development";
const apiProxyTarget = process.env.SPARKWING_API_URL || "http://localhost:4343";

const nextConfig: NextConfig = {
  ...(isDev ? {} : { output: "export" }),
  images: { unoptimized: true },
  ...(isDev
    ? {
        async rewrites() {
          return [
            {
              source: "/api/:path*",
              destination: `${apiProxyTarget}/api/:path*`,
            },
          ];
        },
      }
    : {}),
};

export default nextConfig;
