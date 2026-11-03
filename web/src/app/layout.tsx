import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";
import Nav from "@/components/Nav";
import ConnectionBanner from "@/components/ConnectionBanner";
import Toaster from "@/components/Toasts";
import WaitlistGate from "@/components/WaitlistGate";

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
        {/* Blocking so the dashboard configuration is set before the app
            bundle runs. */}
        {/* eslint-disable-next-line @next/next/no-sync-scripts */}
        <script src="/sparkwing-runtime.js" />
      </head>
      <body className="h-full flex flex-col">
        <a
          href="#main-content"
          className="sr-only focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-[200] focus:px-3 focus:py-2 focus:rounded-sm focus:border focus:border-[var(--accent)] focus:bg-[var(--surface)] focus:text-sm"
        >
          Skip to content
        </a>
        <Nav />
        <ConnectionBanner />
        {/* tabIndex lets the skip link move focus, not just scroll, into main. */}
        <main
          id="main-content"
          tabIndex={-1}
          className="flex-1 flex flex-col overflow-hidden focus:outline-none"
        >
          <WaitlistGate>{children}</WaitlistGate>
        </main>
        <Toaster />
      </body>
    </html>
  );
}
