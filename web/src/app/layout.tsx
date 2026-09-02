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
        {/* Blocking so the dashboard configuration is set before the app
            bundle runs. */}
        {/* eslint-disable-next-line @next/next/no-sync-scripts */}
        <script src="/sparkwing-runtime.js" />
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
