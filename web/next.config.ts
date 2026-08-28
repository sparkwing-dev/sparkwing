import type { NextConfig } from "next";

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
