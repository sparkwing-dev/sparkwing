import type { NextConfig } from "next";

// Static export: `next build` emits HTML/JS/CSS to web/out/. The Go
// binary embeds that directory via //go:embed and serves it from
// `sparkwing web`. Runtime config (API token, controller URL) is
// injected by the Go server via HTML templating, not server-rendered
// by Next.js, because static export has no request lifecycle.
const nextConfig: NextConfig = {
  output: "export",
  // Image optimization requires a Node runtime; disable for static.
  images: { unoptimized: true },
};

export default nextConfig;
