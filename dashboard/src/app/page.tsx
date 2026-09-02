"use client";

import { Card, Chip, ProgressBar, Table } from "@heroui/react";
import { KpiCard } from "@/components/kpi-card";
import { PageHeader } from "@/components/page-header";
import { ErrorState, LoadingState } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { formatBytes, formatDuration, formatPercent, formatSpeed } from "@/lib/rc/format";
import { useRc } from "@/lib/rc/use-rc";
import type {
  ConfigListRemotes,
  CoreMemStats,
  CorePid,
  CoreStats,
  CoreVersion,
  JobList,
  MountList,
} from "@/lib/rc/types";

export default function OverviewPage() {
  const { host } = useHosts();
  const version = useRc<CoreVersion>(host, "core/version", { intervalMs: 8000 });
  const pid = useRc<CorePid>(host, "core/pid", { intervalMs: 8000 });
  const mem = useRc<CoreMemStats>(host, "core/memstats", { intervalMs: 4000 });
  const stats = useRc<CoreStats>(host, "core/stats", { intervalMs: 2000 });
  const remotes = useRc<ConfigListRemotes>(host, "config/listremotes", { intervalMs: 8000 });
  const jobs = useRc<JobList>(host, "job/list", { intervalMs: 4000 });
  const mounts = useRc<MountList>(host, "mount/listmounts", { intervalMs: 8000 });

  const error =
    version.error || pid.error || mem.error || stats.error || remotes.error || jobs.error;
  const loading =
    !error &&
    (version.loading || stats.loading) &&
    !version.data &&
    !stats.data;

  return (
    <div>
      <PageHeader
        title="概览"
        description={`${host.name} · ${host.url}。当前只接本机 rclone RC，主机页已经预留多机器。`}
      />
      {loading ? <LoadingState /> : null}
      {error ? (
        <ErrorState
          message={error}
          onRetry={() => {
            void version.refresh();
            void stats.refresh();
          }}
        />
      ) : null}
      {!error && (version.data || stats.data) ? (
        <div className="space-y-6">
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            <KpiCard
              label="传输速度"
              value={formatSpeed(stats.data?.speed)}
              hint={`已传输 ${formatBytes(stats.data?.bytes)}`}
            />
            <KpiCard
              label="进行中"
              value={stats.data?.transferring?.length ?? 0}
              hint={`完成 ${stats.data?.transfers ?? 0} 个文件`}
            />
            <KpiCard
              label="错误"
              value={stats.data?.errors ?? 0}
              hint={stats.data?.lastError || "没有最近错误"}
            />
            <KpiCard
              label="远程 / 任务 / 挂载"
              value={`${remotes.data?.remotes?.length ?? 0} / ${jobs.data?.runningIds?.length ?? 0} / ${mounts.data?.mountPoints?.length ?? 0}`}
              hint={`PID ${pid.data?.pid ?? "—"}`}
            />
          </div>

          <div className="grid gap-4 xl:grid-cols-3">
            <Card className="xl:col-span-2">
              <Card.Header>
                <Card.Title>正在传输</Card.Title>
                <Card.Description>来自 core/stats，每 2 秒刷新。</Card.Description>
              </Card.Header>
              <Card.Content>
                {(stats.data?.transferring?.length ?? 0) === 0 ? (
                  <p className="py-6 text-sm text-muted">当前没有活动传输。</p>
                ) : (
                  <Table>
                    <Table.ScrollContainer>
                      <Table.Content aria-label="正在传输的文件">
                        <Table.Header>
                          <Table.Column isRowHeader>文件</Table.Column>
                          <Table.Column>进度</Table.Column>
                          <Table.Column>速度</Table.Column>
                          <Table.Column>ETA</Table.Column>
                        </Table.Header>
                        <Table.Body>
                          {stats.data?.transferring?.map((file, index) => (
                            <Table.Row key={`${file.name}-${index}`} id={`${file.name}-${index}`}>
                              <Table.Cell>
                                <span className="max-w-md truncate font-medium">{file.name}</span>
                              </Table.Cell>
                              <Table.Cell>
                                <div className="min-w-40">
                                  <ProgressBar value={formatPercent(file.percentage)}>
                                    <ProgressBar.Track>
                                      <ProgressBar.Fill />
                                    </ProgressBar.Track>
                                  </ProgressBar>
                                  <p className="mt-1 text-xs text-muted">
                                    {formatBytes(file.bytes)} / {formatBytes(file.size)}
                                  </p>
                                </div>
                              </Table.Cell>
                              <Table.Cell>{formatSpeed(file.speedAvg ?? file.speed)}</Table.Cell>
                              <Table.Cell>{formatDuration(file.eta)}</Table.Cell>
                            </Table.Row>
                          ))}
                        </Table.Body>
                      </Table.Content>
                    </Table.ScrollContainer>
                  </Table>
                )}
              </Card.Content>
            </Card>

            <Card>
              <Card.Header>
                <Card.Title>进程</Card.Title>
                <Card.Description>core/version 和 core/memstats</Card.Description>
              </Card.Header>
              <Card.Content className="space-y-3 text-sm">
                <Row label="版本" value={version.data?.version ?? "—"} />
                <Row label="系统" value={version.data?.osVersion ?? version.data?.os ?? "—"} />
                <Row label="Go" value={version.data?.goVersion ?? "—"} />
                <Row
                  label="堆内存"
                  value={`${formatBytes(mem.data?.HeapAlloc)} / ${formatBytes(mem.data?.HeapSys)}`}
                />
                <Row label="运行时长" value={formatDuration(stats.data?.elapsedTime)} />
                <div className="flex flex-wrap gap-2 pt-2">
                  {version.data?.isBeta ? (
                    <Chip color="warning" size="sm">
                      <Chip.Label>beta</Chip.Label>
                    </Chip>
                  ) : null}
                  {stats.data?.fatalError ? (
                    <Chip color="danger" size="sm">
                      <Chip.Label>fatal</Chip.Label>
                    </Chip>
                  ) : null}
                </div>
              </Card.Content>
            </Card>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-4">
      <span className="text-muted">{label}</span>
      <span className="text-right font-medium break-all">{value}</span>
    </div>
  );
}
