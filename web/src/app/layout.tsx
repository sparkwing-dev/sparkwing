import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";
import Nav from "@/components/Nav";
import ConnectionBanner from "@/components/ConnectionBanner";
import DynamicFavicon from "@/components/DynamicFavicon";
import Toaster from "@/components/Toasts";

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
          dangerouslySetInnerHTML={{
            __html:
              'window.__SPARKWING_TOKEN__="__SPARKWING_TOKEN_MARKER__";' +
              'window.__SPARKWING_API_URL__="__SPARKWING_API_URL_MARKER__";' +
              'window.__SPARKWING_VERSION__="__SPARKWING_VERSION_MARKER__";' +
              'window.__SPARKWING_REQUIRE_LOGIN__="__SPARKWING_REQUIRE_LOGIN_MARKER__";',
          }}
          suppressHydrationWarning
        />
      </head>
      <body className="h-full flex flex-col">
        <DynamicFavicon />
        <Nav />
        <ConnectionBanner />
        <div className="flex-1 flex flex-col overflow-hidden">{children}</div>
        <Toaster />
      </body>
    </html>
  );
}
