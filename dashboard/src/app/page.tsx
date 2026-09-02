"use client";

import { Widget } from "@heroui-pro/react/widget";
import { Chip, ProgressBar, Table } from "@heroui/react";
import { useEffect, useMemo, useRef, useState } from "react";
import { DonutChart, HorizontalBars, MemoryAreaChart, SpeedAreaChart } from "@/components/charts";
import { KpiCard } from "@/components/kpi-card";
import { LiveDot, MotionPage, Rise, Stagger } from "@/components/motion-ui";
import { PageHeader } from "@/components/page-header";
import { ErrorState, LoadingState, SoftNotice } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { rcCall } from "@/lib/rc/client";
import { formatBytes, formatDuration, formatPercent, formatSpeed } from "@/lib/rc/format";
import {
  appendTrend,
  bytesSlices,
  jobStatusSlices,
  operationSlices,
  overallPercent,
  remoteTypeSlices,
  sparkFromTrend,
  transferSizeBars,
  transferStatusSlices,
  type TrendPoint,
} from "@/lib/rc/insights";
import { useRc } from "@/lib/rc/use-rc";
import type {
  ConfigDump,
  CoreBwLimit,
  CoreMemStats,
  CorePid,
  CoreStats,
  CoreTransferred,
  CoreVersion,
  JobList,
  JobStatus,
  MountList,
} from "@/lib/rc/types";

export default function OverviewPage() {
  const { host } = useHosts();
  const version = useRc<CoreVersion>(host, "core/version", { intervalMs: 8000 });
  const pid = useRc<CorePid>(host, "core/pid", { intervalMs: 8000 });
  const mem = useRc<CoreMemStats>(host, "core/memstats", { intervalMs: 4000 });
  const stats = useRc<CoreStats>(host, "core/stats", { intervalMs: 2000 });
  const dump = useRc<ConfigDump>(host, "config/dump", { intervalMs: 10000 });
  const jobs = useRc<JobList>(host, "job/list", { intervalMs: 4000 });
  const mounts = useRc<MountList>(host, "mount/listmounts", { intervalMs: 8000 });
  const bwlimit = useRc<CoreBwLimit>(host, "core/bwlimit", { intervalMs: 8000 });
  const done = useRc<CoreTransferred>(host, "core/transferred", { intervalMs: 4000 });
  const [trend, setTrend] = useState<TrendPoint[]>([]);
  const [jobRows, setJobRows] = useState<JobStatus[]>([]);
  const lastHostId = useRef(host.id);

  const fatal =
    (version.error && !version.data && stats.error && !stats.data) ||
    (version.error && !version.data && !stats.data);
  const loading = !fatal && (version.loading || stats.loading) && !version.data && !stats.data;
  const softErrors = [
    version.stale ? `版本：${version.error}` : null,
    stats.stale ? `统计：${stats.error}` : null,
    dump.error && !dump.data ? `远程：${dump.error}` : null,
    jobs.error && !jobs.data ? `任务：${jobs.error}` : null,
    mounts.error && !mounts.data ? `挂载：${mounts.error}` : null,
  ].filter((item): item is string => Boolean(item));

  useEffect(() => {
    const starter = window.setTimeout(() => {
      setTrend((prev) => {
        if (lastHostId.current !== host.id) {
          lastHostId.current = host.id;
          return appendTrend([], stats.data, mem.data);
        }
        return appendTrend(prev, stats.data, mem.data);
      });
    }, 0);
    return () => window.clearTimeout(starter);
  }, [host.id, mem.data, stats.data]);

  const runningIds = jobs.data?.runningIds ?? [];
  const runningKey = runningIds.join(",");
  useEffect(() => {
    let cancelled = false;
    const ids = runningKey ? runningKey.split(",").map((item) => Number(item)) : [];
    async function loadJobs() {
      if (ids.length === 0) {
        if (!cancelled) {
          setJobRows([]);
        }
        return;
      }
      const rows = await Promise.all(
        ids.slice(0, 12).map(async (jobid) => {
          try {
            return await rcCall<JobStatus>(host, "job/status", { jobid });
          } catch {
            return { id: jobid, error: "读取失败" };
          }
        }),
      );
      if (!cancelled) {
        setJobRows(rows);
      }
    }
    void loadJobs();
    return () => {
      cancelled = true;
    };
  }, [host, runningKey]);

  const statusPie = useMemo(() => transferStatusSlices(stats.data), [stats.data]);
  const bytesPie = useMemo(() => bytesSlices(stats.data), [stats.data]);
  const opsBars = useMemo(() => operationSlices(stats.data), [stats.data]);
  const remotePie = useMemo(() => remoteTypeSlices(dump.data), [dump.data]);
  const jobPie = useMemo(() => jobStatusSlices(jobRows), [jobRows]);
  const sizeBars = useMemo(() => transferSizeBars(stats.data?.transferring), [stats.data]);
  const percent = overallPercent(stats.data);
  const connected = Boolean(version.data || stats.data);

  return (
    <MotionPage>
      <PageHeader
        title="概览"
        description={`${host.name} · ${host.url}。速度、占比和任务都会跟着 RC 轮询更新。`}
        meta={
          <LiveDot
            active={connected && !fatal}
            label={connected ? "实时监控中 · 2 秒刷新" : "等待连接 rclone RC"}
          />
        }
      />
      {loading ? <LoadingState /> : null}
      {fatal ? (
        <ErrorState
          message={version.error || stats.error || "无法读取 rclone"}
          onRetry={() => {
            void version.refresh();
            void stats.refresh();
          }}
        />
      ) : null}
      {softErrors.length > 0 && !fatal ? (
        <SoftNotice
          message={softErrors.join("；")}
          onRetry={() => {
            void dump.refresh();
            void jobs.refresh();
            void mounts.refresh();
          }}
        />
      ) : null}
      {!fatal && (version.data || stats.data) ? (
        <div className="space-y-6">
          <Stagger className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            <Rise>
              <KpiCard
                label="传输速度"
                value={formatSpeed(stats.data?.speed)}
                hint={`已传输 ${formatBytes(stats.data?.bytes)}`}
                chart={sparkFromTrend(trend, "speed")}
                status={(stats.data?.errors ?? 0) > 0 ? "warning" : "success"}
              />
            </Rise>
            <Rise>
              <KpiCard
                label="进行中"
                value={stats.data?.transferring?.length ?? 0}
                hint={`完成 ${stats.data?.transfers ?? 0} 个文件`}
                chart={sparkFromTrend(trend, "transferring")}
              />
            </Rise>
            <Rise>
              <KpiCard
                label="错误"
                value={stats.data?.errors ?? 0}
                hint={stats.data?.lastError || "没有最近错误"}
                status={(stats.data?.errors ?? 0) > 0 ? "danger" : "success"}
                chart={sparkFromTrend(trend, "errors")}
              />
            </Rise>
            <Rise>
              <KpiCard
                label="远程 / 任务 / 挂载"
                value={`${Object.keys(dump.data ?? {}).length} / ${runningIds.length} / ${mounts.data?.mountPoints?.length ?? 0}`}
                hint={`PID ${pid.data?.pid ?? "—"} · 限额 ${bwlimit.data?.rate || "不限"}`}
              />
            </Rise>
          </Stagger>

          <Stagger className="grid gap-4 xl:grid-cols-3">
            <Rise className="xl:col-span-2">
              <Widget>
                <Widget.Header>
                  <Widget.Title>速度与并发</Widget.Title>
                  <Widget.Description>core/stats 折线，左轴速度，右轴进行中文件数。</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  {percent > 0 ? (
                    <div className="mb-3">
                      <div className="mb-1 flex justify-between text-xs text-muted">
                        <span>总体进度</span>
                        <span>
                          {formatBytes(stats.data?.bytes)} / {formatBytes(stats.data?.totalBytes)} · {percent}%
                        </span>
                      </div>
                      <ProgressBar value={percent}>
                        <ProgressBar.Track>
                          <ProgressBar.Fill />
                        </ProgressBar.Track>
                      </ProgressBar>
                    </div>
                  ) : null}
                  <div className="chart-panel-lg">
                    <SpeedAreaChart data={trend} speedFormatter={formatSpeed} />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
            <Rise>
              <Widget>
                <Widget.Header>
                  <Widget.Title>传输状态</Widget.Title>
                  <Widget.Description>进行中 / 完成 / 错误占比</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  <div className="chart-panel">
                    <DonutChart data={statusPie} emptyLabel="还没有传输统计" />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
          </Stagger>

          <Stagger className="grid gap-4 lg:grid-cols-2 xl:grid-cols-4">
            <Rise>
              <Widget>
                <Widget.Header>
                  <Widget.Title>字节占比</Widget.Title>
                  <Widget.Description>已传输 vs 剩余</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  <div className="chart-panel">
                    <DonutChart data={bytesPie} valueFormatter={formatBytes} emptyLabel="总大小未知时只显示已传字节" />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
            <Rise>
              <Widget>
                <Widget.Header>
                  <Widget.Title>操作构成</Widget.Title>
                  <Widget.Description>传输、校验、删除和服务端操作</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  <div className="chart-panel">
                    <HorizontalBars data={opsBars} />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
            <Rise>
              <Widget>
                <Widget.Header>
                  <Widget.Title>远程类型</Widget.Title>
                  <Widget.Description>来自 config/dump</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  <div className="chart-panel">
                    <DonutChart data={remotePie} emptyLabel="还没有配置远程" />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
            <Rise>
              <Widget>
                <Widget.Header>
                  <Widget.Title>运行中任务</Widget.Title>
                  <Widget.Description>job/list 当前批次</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  <div className="chart-panel">
                    <DonutChart data={jobPie} emptyLabel="当前没有异步任务" />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
          </Stagger>

          <Stagger className="grid gap-4 xl:grid-cols-3">
            <Rise className="xl:col-span-2">
              <Widget>
                <Widget.Header>
                  <Widget.Title>正在传输</Widget.Title>
                  <Widget.Description>单文件进度，下方是当前批次体积对比。</Widget.Description>
                </Widget.Header>
                <Widget.Content className="space-y-4">
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
                  <div className="chart-panel">
                    <HorizontalBars data={sizeBars} valueFormatter={formatBytes} emptyLabel="没有可对比的文件大小" />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
            <Rise>
              <Widget>
                <Widget.Header>
                  <Widget.Title>进程与内存</Widget.Title>
                  <Widget.Description>core/version、memstats、最近完成</Widget.Description>
                </Widget.Header>
                <Widget.Content className="space-y-3 text-sm">
                  <Row label="版本" value={version.data?.version ?? "—"} />
                  <Row label="系统" value={version.data?.osVersion ?? version.data?.os ?? "—"} />
                  <Row label="架构" value={version.data?.osArch ?? version.data?.arch ?? "—"} />
                  <Row label="Go" value={version.data?.goVersion ?? "—"} />
                  <Row
                    label="堆内存"
                    value={`${formatBytes(mem.data?.HeapAlloc)} / ${formatBytes(mem.data?.HeapSys)}`}
                  />
                  <Row label="运行时长" value={formatDuration(stats.data?.elapsedTime)} />
                  <Row label="最近完成" value={`${done.data?.transferred?.length ?? 0} 条`} />
                  <div className="flex flex-wrap gap-2 pt-1">
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
                    {bwlimit.data?.rate ? (
                      <Chip size="sm">
                        <Chip.Label>bw {bwlimit.data.rate}</Chip.Label>
                      </Chip>
                    ) : null}
                  </div>
                  <div className="chart-panel pt-2">
                    <MemoryAreaChart data={trend} bytesFormatter={formatBytes} />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
          </Stagger>
        </div>
      ) : null}
    </MotionPage>
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
