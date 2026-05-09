import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";
import Nav from "@/components/Nav";
import ConnectionBanner from "@/components/ConnectionBanner";
import DynamicFavicon from "@/components/DynamicFavicon";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: "Sparkwing",
  description: "CI/CD pipelines in Go",
};

// Runtime config (token + API URL) is injected by the Go server at
// serve time via HTML templating. The markers below are substituted
// by `pkg/orchestrator/web` before the HTML reaches the browser:
//
//   __SPARKWING_TOKEN_MARKER__   ->  controller bearer token
//   __SPARKWING_API_URL_MARKER__ ->  controller URL (empty = same origin)
//
// Static export means no server lifecycle at request time; per-
// deployment values must come from the serving layer, not from
// `next build`.
export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      className={`${geistSans.variable} ${geistMono.variable} h-full antialiased`}
    >
      <head>
        <script
          // eslint-disable-next-line react/no-danger
          dangerouslySetInnerHTML={{
            __html:
              'window.__SPARKWING_TOKEN__="__SPARKWING_TOKEN_MARKER__";' +
              'window.__SPARKWING_API_URL__="__SPARKWING_API_URL_MARKER__";',
          }}
          // Ad-blocker extensions sometimes rewrite scripts in the
          // head before React hydrates; suppress the hydration warning
          // since we set the content explicitly.
          suppressHydrationWarning
        />
      </head>
      <body className="h-full flex flex-col">
        <DynamicFavicon />
        <Nav />
        <ConnectionBanner />
        <div className="flex-1 flex flex-col overflow-hidden">{children}</div>
      </body>
    </html>
  );
}
