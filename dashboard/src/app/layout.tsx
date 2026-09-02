import type { Metadata } from "next";
import { cookies } from "next/headers";
import { Geist, Geist_Mono } from "next/font/google";
import { AppShell } from "@/components/app-shell";
import { HostProvider } from "@/components/host-provider";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: "rrclone 看板",
  description: "管理本机 rclone RC 状态，后续可扩展到多机器",
};

export default async function RootLayout({ children }: LayoutProps<"/">) {
  const store = await cookies();
  const defaultSidebarOpen = store.get("sidebar_state")?.value !== "false";

  return (
    <html
      lang="zh-CN"
      className={`dark ${geistSans.variable} ${geistMono.variable} h-full antialiased`}
      style={{ fontFamily: "var(--font-geist-sans), sans-serif" }}
    >
      <body className="min-h-full bg-background text-foreground">
        <HostProvider>
          <AppShell defaultSidebarOpen={defaultSidebarOpen}>{children}</AppShell>
        </HostProvider>
      </body>
    </html>
  );
}
