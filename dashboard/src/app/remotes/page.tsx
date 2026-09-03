"use client";

import { Widget } from "@heroui-pro/react/widget";
import { Chip, ProgressBar, Table } from "@heroui/react";
import { useEffect, useMemo, useState } from "react";
import { DonutChart, HorizontalBars } from "@/components/charts";
import { LiveDot, MotionPage, Rise, Stagger } from "@/components/motion-ui";
import { PageHeader } from "@/components/page-header";
import { BlankState, ErrorState, LoadingState, SoftNotice } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { rcCall } from "@/lib/rc/client";
import { formatBytes, formatPercent, redactRecord } from "@/lib/rc/format";
import { remoteTypeSlices } from "@/lib/rc/insights";
import { useRc } from "@/lib/rc/use-rc";
import type { ConfigDump, OperationsAbout } from "@/lib/rc/types";

type RemoteAbout = {
  name: string;
  about: OperationsAbout | null;
  error?: string;
};

export default function RemotesPage() {
  const { host } = useHosts();
  const dump = useRc<ConfigDump>(host, "config/dump", { intervalMs: 8000 });
  const remotes = Object.entries(dump.data ?? {});
  const namesKey = remotes.map(([name]) => name).sort().join("\0");
  const [abouts, setAbouts] = useState<RemoteAbout[]>([]);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      const names = namesKey ? namesKey.split("\0") : [];
      if (names.length === 0) {
        if (!cancelled) {
          setAbouts([]);
        }
        return;
      }
      const next = await Promise.all(
        names.slice(0, 16).map(async (name) => {
          try {
            const about = await rcCall<OperationsAbout>(host, "operations/about", { fs: `${name}:` });
            return { name, about };
          } catch (error) {
            return {
              name,
              about: null,
              error: error instanceof Error ? error.message : "about 失败",
            };
          }
        }),
      );
      if (!cancelled) {
        setAbouts(next);
      }
    }
    void load();
    return () => {
      cancelled = true;
    };
  }, [host, namesKey]);

  const typePie = useMemo(() => remoteTypeSlices(dump.data), [dump.data]);
  const usageBars = useMemo(
    () =>
      abouts
        .filter((item) => (item.about?.used ?? 0) > 0)
        .map((item) => ({ name: item.name, value: Number(item.about?.used ?? 0) }))
        .sort((a, b) => b.value - a.value)
        .slice(0, 8),
    [abouts],
  );
  const aboutErrors = abouts.filter((item) => item.error).length;

  return (
    <MotionPage>
      <PageHeader
        title="远程"
        description="读取 config/dump，并尝试 operations/about 拿用量。token、secret、password 会自动打码。"
        meta={<LiveDot active={Boolean(dump.data)} label="8 秒刷新配置" />}
      />
      {dump.loading && !dump.data ? <LoadingState /> : null}
      {dump.error && !dump.data ? <ErrorState message={dump.error} onRetry={() => void dump.refresh()} /> : null}
      {dump.stale ? <SoftNotice message={`配置暂时失败：${dump.error}`} onRetry={() => void dump.refresh()} /> : null}
      {aboutErrors > 0 ? (
        <SoftNotice message={`${aboutErrors} 个远程没有用量信息（本地盘或后端不支持 about 很常见）`} />
      ) : null}
      {dump.data && remotes.length === 0 ? (
        <BlankState
          title="还没有配置远程"
          description="用 rclone config 或 rc config/create 加完之后，这里会列出来。"
        />
      ) : null}
      {dump.data && remotes.length > 0 ? (
        <div className="space-y-6">
          <Stagger className="grid gap-4 lg:grid-cols-2">
            <Rise>
              <Widget>
                <Widget.Header>
                  <Widget.Title>类型分布</Widget.Title>
                  <Widget.Description>{remotes.length} 个远程</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  <div className="chart-panel">
                    <DonutChart data={typePie} />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
            <Rise>
              <Widget>
                <Widget.Header>
                  <Widget.Title>已用空间</Widget.Title>
                  <Widget.Description>operations/about 的 used</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  <div className="chart-panel">
                    <HorizontalBars data={usageBars} valueFormatter={formatBytes} emptyLabel="这些远程没有返回用量" />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
          </Stagger>
          <Table>
            <Table.ScrollContainer>
              <Table.Content aria-label="rclone 远程">
                <Table.Header>
                  <Table.Column isRowHeader>名称</Table.Column>
                  <Table.Column>类型</Table.Column>
                  <Table.Column>用量</Table.Column>
                  <Table.Column>配置</Table.Column>
                </Table.Header>
                <Table.Body>
                  {remotes.map(([name, config]) => {
                    const safe = redactRecord(config);
                    const about = abouts.find((item) => item.name === name);
                    const used = about?.about?.used;
                    const total = about?.about?.total;
                    const pct = total ? formatPercent(((used ?? 0) / total) * 100) : 0;
                    return (
                      <Table.Row key={name} id={name}>
                        <Table.Cell className="font-medium">{name}</Table.Cell>
                        <Table.Cell>
                          <Chip size="sm">
                            <Chip.Label>{safe.type || "unknown"}</Chip.Label>
                          </Chip>
                        </Table.Cell>
                        <Table.Cell>
                          {about?.about ? (
                            <div className="min-w-40">
                              <ProgressBar value={pct}>
                                <ProgressBar.Track>
                                  <ProgressBar.Fill />
                                </ProgressBar.Track>
                              </ProgressBar>
                              <p className="mt-1 text-xs text-muted">
                                {formatBytes(used)} / {formatBytes(total)}
                              </p>
                            </div>
                          ) : (
                            <span className="text-xs text-muted">{about?.error || "—"}</span>
                          )}
                        </Table.Cell>
                        <Table.Cell>
                          <div className="flex flex-wrap gap-1.5">
                            {Object.entries(safe)
                              .filter(([key]) => key !== "type")
                              .slice(0, 8)
                              .map(([key, value]) => (
                                <Chip key={key} size="sm" variant="soft">
                                  <Chip.Label>
                                    {key}={value}
                                  </Chip.Label>
                                </Chip>
                              ))}
                          </div>
                        </Table.Cell>
                      </Table.Row>
                    );
                  })}
                </Table.Body>
              </Table.Content>
            </Table.ScrollContainer>
          </Table>
        </div>
      ) : null}
    </MotionPage>
  );
}
