"use client";

import { Widget } from "@heroui-pro/react/widget";
import { Chip, ProgressBar, Table, Tabs } from "@heroui/react";
import { useEffect, useMemo, useRef, useState } from "react";
import { DonutChart, HorizontalBars, SpeedAreaChart } from "@/components/charts";
import { LiveDot, MotionPage, Rise, Stagger } from "@/components/motion-ui";
import { PageHeader } from "@/components/page-header";
import { BlankState, ErrorState, LoadingState, SoftNotice } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { formatBytes, formatDuration, formatPercent, formatSpeed, formatTime } from "@/lib/rc/format";
import {
  appendTrend,
  transferSizeBars,
  transferredOutcomeSlices,
  type TrendPoint,
} from "@/lib/rc/insights";
import { useRc } from "@/lib/rc/use-rc";
import type { CoreStats, CoreTransferred } from "@/lib/rc/types";

export default function TransfersPage() {
  const { host } = useHosts();
  const stats = useRc<CoreStats>(host, "core/stats", { intervalMs: 2000 });
  const done = useRc<CoreTransferred>(host, "core/transferred", { intervalMs: 4000 });
  const [trend, setTrend] = useState<TrendPoint[]>([]);
  const lastHostId = useRef(host.id);

  useEffect(() => {
    const starter = window.setTimeout(() => {
      setTrend((prev) => {
        if (lastHostId.current !== host.id) {
          lastHostId.current = host.id;
          return appendTrend([], stats.data);
        }
        return appendTrend(prev, stats.data);
      });
    }, 0);
    return () => window.clearTimeout(starter);
  }, [host.id, stats.data]);

  const sizeBars = useMemo(() => transferSizeBars(stats.data?.transferring, 10), [stats.data]);
  const outcomes = useMemo(() => transferredOutcomeSlices(done.data?.transferred), [done.data]);
  const fatal = Boolean(stats.error && !stats.data);

  return (
    <MotionPage>
      <PageHeader
        title="传输"
        description="活动传输来自 core/stats，完成记录来自 core/transferred，并带速度曲线和结果占比。"
        meta={<LiveDot active={Boolean(stats.data)} label="2 秒刷新活动传输" />}
      />
      {stats.loading && !stats.data ? <LoadingState /> : null}
      {fatal ? <ErrorState message={stats.error || "读取失败"} onRetry={() => void stats.refresh()} /> : null}
      {done.error && !done.data ? (
        <SoftNotice message={`最近完成：${done.error}`} onRetry={() => void done.refresh()} />
      ) : null}
      {stats.stale ? <SoftNotice message={`统计暂时失败：${stats.error}`} onRetry={() => void stats.refresh()} /> : null}
      {!fatal && stats.data ? (
        <div className="space-y-6">
          <Stagger className="grid gap-4 xl:grid-cols-3">
            <Rise className="xl:col-span-2">
              <Widget>
                <Widget.Header>
                  <Widget.Title>实时速度</Widget.Title>
                  <Widget.Description>当前 {formatSpeed(stats.data.speed)}</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  <div className="chart-panel-lg">
                    <SpeedAreaChart data={trend} speedFormatter={formatSpeed} />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
            <Rise>
              <Widget>
                <Widget.Header>
                  <Widget.Title>最近完成结果</Widget.Title>
                  <Widget.Description>成功 / 校验 / 失败</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  <div className="chart-panel">
                    <DonutChart data={outcomes} emptyLabel="还没有完成记录" />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
          </Stagger>
          <Rise>
            <Widget>
              <Widget.Header>
                <Widget.Title>当前文件体积</Widget.Title>
                <Widget.Description>按正在传输的文件大小排序</Widget.Description>
              </Widget.Header>
              <Widget.Content>
                <div className="chart-panel">
                  <HorizontalBars data={sizeBars} valueFormatter={formatBytes} emptyLabel="没有活动文件" />
                </div>
              </Widget.Content>
            </Widget>
          </Rise>
          <Tabs defaultSelectedKey="live">
            <Tabs.ListContainer>
              <Tabs.List aria-label="传输视图">
                <Tabs.Tab id="live">进行中 ({stats.data.transferring?.length ?? 0})</Tabs.Tab>
                <Tabs.Tab id="done">最近完成 ({done.data?.transferred?.length ?? 0})</Tabs.Tab>
              </Tabs.List>
            </Tabs.ListContainer>
            <Tabs.Panel id="live" className="pt-4">
              {(stats.data.transferring?.length ?? 0) === 0 ? (
                <BlankState title="没有活动传输" description="新的 copy / sync / mount 写入会显示在这里。" />
              ) : (
                <Table>
                  <Table.ScrollContainer>
                    <Table.Content aria-label="活动传输">
                      <Table.Header>
                        <Table.Column isRowHeader>文件</Table.Column>
                        <Table.Column>进度</Table.Column>
                        <Table.Column>大小</Table.Column>
                        <Table.Column>速度</Table.Column>
                        <Table.Column>ETA</Table.Column>
                      </Table.Header>
                      <Table.Body>
                        {stats.data.transferring?.map((file, index) => (
                          <Table.Row key={`${file.name}-${index}`} id={`${file.name}-${index}`}>
                            <Table.Cell>{file.name}</Table.Cell>
                            <Table.Cell>
                              <div className="min-w-40">
                                <ProgressBar value={formatPercent(file.percentage)}>
                                  <ProgressBar.Track>
                                    <ProgressBar.Fill />
                                  </ProgressBar.Track>
                                </ProgressBar>
                              </div>
                            </Table.Cell>
                            <Table.Cell>
                              {formatBytes(file.bytes)} / {formatBytes(file.size)}
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
            </Tabs.Panel>
            <Tabs.Panel id="done" className="pt-4">
              {(done.data?.transferred?.length ?? 0) === 0 ? (
                <BlankState title="还没有完成记录" description="rclone 只保留最近大约 100 条完成传输。" />
              ) : (
                <Table>
                  <Table.ScrollContainer>
                    <Table.Content aria-label="最近完成的传输">
                      <Table.Header>
                        <Table.Column isRowHeader>文件</Table.Column>
                        <Table.Column>动作</Table.Column>
                        <Table.Column>大小</Table.Column>
                        <Table.Column>状态</Table.Column>
                        <Table.Column>时间</Table.Column>
                      </Table.Header>
                      <Table.Body>
                        {done.data?.transferred?.map((file, index) => (
                          <Table.Row key={`${file.name}-${file.timestamp}-${index}`} id={`${index}`}>
                            <Table.Cell>{file.name}</Table.Cell>
                            <Table.Cell>{file.what || "transfer"}</Table.Cell>
                            <Table.Cell>{formatBytes(file.size)}</Table.Cell>
                            <Table.Cell>
                              <Chip color={file.error ? "danger" : "success"} size="sm">
                                <Chip.Label>{file.error ? file.error : "成功"}</Chip.Label>
                              </Chip>
                            </Table.Cell>
                            <Table.Cell>{formatTime(file.timestamp)}</Table.Cell>
                          </Table.Row>
                        ))}
                      </Table.Body>
                    </Table.Content>
                  </Table.ScrollContainer>
                </Table>
              )}
            </Tabs.Panel>
          </Tabs>
        </div>
      ) : null}
    </MotionPage>
  );
}
