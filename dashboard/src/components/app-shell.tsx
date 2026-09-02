"use client";

import { Button, Chip, ListBox, Select } from "@heroui/react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import type { ReactNode } from "react";
import { useRc } from "@/lib/rc/use-rc";
import type { CoreVersion } from "@/lib/rc/types";
import { useHosts } from "./host-provider";
import {
  HostsIcon,
  JobsIcon,
  MountsIcon,
  OverviewIcon,
  RemotesIcon,
  TransfersIcon,
} from "./icons";

const NAV = [
  { href: "/", label: "概览", icon: OverviewIcon },
  { href: "/transfers", label: "传输", icon: TransfersIcon },
  { href: "/remotes", label: "远程", icon: RemotesIcon },
  { href: "/jobs", label: "任务", icon: JobsIcon },
  { href: "/mounts", label: "挂载", icon: MountsIcon },
  { href: "/hosts", label: "主机", icon: HostsIcon },
] as const;

export function AppShell({ children }: { children: ReactNode }) {
  const pathname = usePathname();
  const { hosts, host, selectHost } = useHosts();
  const version = useRc<CoreVersion>(host, "core/version", { intervalMs: 8000 });
  const connected = Boolean(version.data && !version.error);

  return (
    <div className="flex min-h-full">
      <aside className="sticky top-0 flex h-screen w-64 shrink-0 flex-col border-r border-separator bg-surface">
        <div className="px-5 py-5">
          <p className="text-xs font-medium tracking-[0.18em] text-muted uppercase">rrclone</p>
          <p className="mt-1 text-lg font-semibold">rclone 看板</p>
        </div>
        <nav className="flex flex-1 flex-col gap-1 px-3">
          {NAV.map((item) => {
            const active = item.href === "/" ? pathname === "/" : pathname.startsWith(item.href);
            const Icon = item.icon;
            return (
              <Link
                key={item.href}
                href={item.href}
                className={`flex items-center gap-3 rounded-xl px-3 py-2 text-sm transition-colors ${
                  active
                    ? "bg-accent/10 text-accent font-medium"
                    : "text-foreground/80 hover:bg-overlay"
                }`}
              >
                <Icon className="size-4" />
                {item.label}
              </Link>
            );
          })}
        </nav>
        <div className="border-t border-separator px-5 py-4 text-xs text-muted">
          独立于官方 rclone 源码，后续可直接合并上游。
        </div>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-20 flex items-center justify-between gap-4 border-b border-separator bg-background/85 px-6 py-3 backdrop-blur">
          <div className="min-w-[240px]">
            <Select
              aria-label="当前主机"
              selectedKey={host.id}
              onSelectionChange={(key) => {
                if (key != null) {
                  selectHost(String(key));
                }
              }}
            >
              <Select.Trigger>
                <Select.Value />
                <Select.Indicator />
              </Select.Trigger>
              <Select.Popover>
                <ListBox>
                  {hosts.map((item) => (
                    <ListBox.Item key={item.id} id={item.id} textValue={item.name}>
                      {item.name}
                      <span className="ml-2 text-xs text-muted">{item.url}</span>
                    </ListBox.Item>
                  ))}
                </ListBox>
              </Select.Popover>
            </Select>
          </div>
          <div className="flex items-center gap-3">
            <Chip color={connected ? "success" : version.loading ? "default" : "danger"} size="sm">
              <Chip.Label>
                {connected ? "已连接" : version.loading ? "连接中" : "未连接"}
              </Chip.Label>
            </Chip>
            <span className="hidden text-sm text-muted md:inline">
              {version.data?.version ?? host.url}
            </span>
            <Button size="sm" variant="tertiary" onPress={() => void version.refresh()}>
              刷新
            </Button>
          </div>
        </header>
        <main className="flex-1 px-6 py-6">{children}</main>
      </div>
    </div>
  );
}
