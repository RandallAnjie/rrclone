"use client";

import { AppLayout } from "@heroui-pro/react/app-layout";
import { Navbar } from "@heroui-pro/react/navbar";
import { Sidebar } from "@heroui-pro/react/sidebar";
import { Button, Chip, ListBox, Select } from "@heroui/react";
import { usePathname, useRouter } from "next/navigation";
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

function isActive(pathname: string, href: string) {
  return href === "/" ? pathname === "/" : pathname.startsWith(href);
}

function NavMenu({
  pathname,
  onNavigate,
}: {
  pathname: string;
  onNavigate: (href: string) => void;
}) {
  return (
    <Sidebar.Menu>
      {NAV.map((item) => {
        const Icon = item.icon;
        return (
          <Sidebar.MenuItem
            key={item.href}
            id={item.href}
            href={item.href}
            isCurrent={isActive(pathname, item.href)}
            textValue={item.label}
            onAction={() => onNavigate(item.href)}
          >
            <Sidebar.MenuIcon>
              <Icon />
            </Sidebar.MenuIcon>
            <Sidebar.MenuLabel>{item.label}</Sidebar.MenuLabel>
          </Sidebar.MenuItem>
        );
      })}
    </Sidebar.Menu>
  );
}

function Brand() {
  return (
    <div className="px-1">
      <p className="text-xs font-medium tracking-[0.18em] text-muted uppercase">rrclone</p>
      <p className="text-sm font-semibold">rclone 看板</p>
    </div>
  );
}

export function AppShell({
  children,
  defaultSidebarOpen = true,
}: {
  children: ReactNode;
  defaultSidebarOpen?: boolean;
}) {
  const pathname = usePathname();
  const router = useRouter();
  const { hosts, host, selectHost } = useHosts();
  const version = useRc<CoreVersion>(host, "core/version", { intervalMs: 8000 });
  const connected = Boolean(version.data && !version.error);

  const go = (href: string) => {
    router.push(href);
  };

  const sidebar = (
    <>
      <Sidebar>
        <Sidebar.Header>
          <Brand />
        </Sidebar.Header>
        <Sidebar.Content>
          <Sidebar.Group>
            <Sidebar.GroupLabel>监控</Sidebar.GroupLabel>
            <NavMenu pathname={pathname} onNavigate={go} />
          </Sidebar.Group>
        </Sidebar.Content>
        <Sidebar.Footer>
          <p className="px-1 text-xs text-muted">独立于官方 rclone 源码，后续可直接合并上游。</p>
        </Sidebar.Footer>
        <Sidebar.Rail />
      </Sidebar>
      <Sidebar.Mobile>
        <Sidebar.Header>
          <Brand />
        </Sidebar.Header>
        <Sidebar.Content>
          <NavMenu pathname={pathname} onNavigate={go} />
        </Sidebar.Content>
      </Sidebar.Mobile>
    </>
  );

  const navbar = (
    <Navbar maxWidth="full">
      <Navbar.Header>
        <AppLayout.MenuToggle tooltip="打开导航" />
        <Sidebar.Trigger />
        <div className="min-w-[220px] max-w-sm">
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
        <Navbar.Spacer />
        <Navbar.Content>
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
        </Navbar.Content>
      </Navbar.Header>
    </Navbar>
  );

  return (
    <AppLayout
      defaultSidebarOpen={defaultSidebarOpen}
      navigate={go}
      scrollMode="content"
      sidebarCollapsible="icon"
      sidebarVariant="inset"
      sidebar={sidebar}
      navbar={navbar}
    >
      <div className="px-6 py-6">{children}</div>
    </AppLayout>
  );
}
