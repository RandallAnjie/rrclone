"use client";

import { Widget } from "@heroui-pro/react/widget";
import { Button, Chip, Table } from "@heroui/react";
import { useEffect, useMemo, useState } from "react";
import { DonutChart, HorizontalBars } from "@/components/charts";
import { LiveDot, MotionPage, Rise, Stagger } from "@/components/motion-ui";
import { PageHeader } from "@/components/page-header";
import { BlankState, ErrorState, LoadingState, SoftNotice } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { rcCall } from "@/lib/rc/client";
import { formatDuration } from "@/lib/rc/format";
import { jobStatusSlices } from "@/lib/rc/insights";
import { useRc } from "@/lib/rc/use-rc";
import type { JobList, JobStatus } from "@/lib/rc/types";

const MAX_FINISHED = 40;

function pickJobIds(list: JobList | null): number[] {
  if (!list) {
    return [];
  }
  const running = list.runningIds ?? [];
  const finished = list.finishedIds ?? list.jobids ?? [];
  const recentFinished = finished.slice(-MAX_FINISHED);
  return [...new Set([...running, ...recentFinished])].sort((a, b) => b - a);
}

async function mapPool<T, R>(items: T[], concurrency: number, worker: (item: T) => Promise<R>) {
  const results = new Array<R>(items.length);
  let index = 0;
  async function run() {
    while (index < items.length) {
      const current = index;
      index += 1;
      results[current] = await worker(items[current]!);
    }
  }
  const runners = Array.from({ length: Math.min(concurrency, items.length) }, () => run());
  await Promise.all(runners);
  return results;
}

export default function JobsPage() {
  const { host } = useHosts();
  const list = useRc<JobList>(host, "job/list", { intervalMs: 3000 });
  const ids = useMemo(() => pickJobIds(list.data), [list.data]);
  const idsKey = ids.join(",");
  const [jobs, setJobs] = useState<JobStatus[]>([]);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      if (!idsKey) {
        if (!cancelled) {
          setJobs([]);
          setDetailError(null);
        }
        return;
      }
      const jobIds = idsKey.split(",").map((item) => Number(item));
      try {
        const next = await mapPool(jobIds, 6, async (jobid) => {
          try {
            return await rcCall<JobStatus>(host, "job/status", { jobid });
          } catch {
            return { id: jobid, finished: true, success: false, error: "读取失败" };
          }
        });
        if (!cancelled) {
          setJobs(next);
          setDetailError(null);
        }
      } catch (error) {
        if (!cancelled) {
          setDetailError(error instanceof Error ? error.message : "读取任务失败");
        }
      }
    }
    void load();
    return () => {
      cancelled = true;
    };
  }, [host, idsKey]);

  async function stopJob(jobid: number) {
    setBusyId(jobid);
    try {
      await rcCall(host, "job/stop", { jobid });
      await list.refresh();
    } catch (error) {
      setDetailError(error instanceof Error ? error.message : "停止任务失败");
    } finally {
      setBusyId(null);
    }
  }

  const statusPie = useMemo(() => jobStatusSlices(jobs), [jobs]);
  const durationBars = useMemo(
    () =>
      [...jobs]
        .filter((job) => (job.duration ?? 0) > 0)
        .sort((a, b) => (b.duration ?? 0) - (a.duration ?? 0))
        .slice(0, 8)
        .map((job) => ({ name: `#${job.id ?? "?"}`, value: Number(job.duration ?? 0) })),
    [jobs],
  );
  const totalKnown =
    (list.data?.runningIds?.length ?? 0) + (list.data?.finishedIds?.length ?? list.data?.jobids?.length ?? 0);

  return (
    <MotionPage>
      <PageHeader
        kicker="调度"
        title="任务"
        description={`异步 RC 任务来自 job/list 和 job/status。当前展示运行中 + 最近 ${MAX_FINISHED} 条完成记录${
          totalKnown > ids.length ? `（共 ${totalKnown}）` : ""
        }。`}
        meta={<LiveDot active={Boolean(list.data)} label="3 秒刷新任务列表" />}
      />
      {list.loading && !list.data ? <LoadingState /> : null}
      {list.error && !list.data ? <ErrorState message={list.error} onRetry={() => void list.refresh()} /> : null}
      {detailError ? <SoftNotice message={detailError} onRetry={() => void list.refresh()} /> : null}
      {list.stale ? <SoftNotice message={`任务列表暂时失败：${list.error}`} /> : null}
      {list.data && ids.length === 0 ? (
        <BlankState title="没有最近任务" description="带 _async=true 的 copy/sync 会出现在这里。" />
      ) : null}
      {jobs.length > 0 ? (
        <div className="space-y-6">
          <Stagger className="grid gap-4 lg:grid-cols-2">
            <Rise>
              <Widget>
                <Widget.Header>
                  <Widget.Title>任务状态</Widget.Title>
                  <Widget.Description>运行中 / 成功 / 失败</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  <div className="chart-panel">
                    <DonutChart data={statusPie} />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
            <Rise>
              <Widget>
                <Widget.Header>
                  <Widget.Title>耗时最长</Widget.Title>
                  <Widget.Description>当前批次里 duration 最大的任务</Widget.Description>
                </Widget.Header>
                <Widget.Content>
                  <div className="chart-panel">
                    <HorizontalBars data={durationBars} valueFormatter={(value) => formatDuration(value)} />
                  </div>
                </Widget.Content>
              </Widget>
            </Rise>
          </Stagger>
          <div className="table-shell">
          <Table>
            <Table.ScrollContainer>
              <Table.Content aria-label="rclone 任务">
                <Table.Header>
                  <Table.Column isRowHeader>ID</Table.Column>
                  <Table.Column>状态</Table.Column>
                  <Table.Column>耗时</Table.Column>
                  <Table.Column>开始</Table.Column>
                  <Table.Column>错误</Table.Column>
                  <Table.Column>操作</Table.Column>
                </Table.Header>
                <Table.Body>
                  {jobs.map((job) => {
                    const running = !job.finished;
                    return (
                      <Table.Row key={job.id} id={String(job.id)}>
                        <Table.Cell>{job.id}</Table.Cell>
                        <Table.Cell>
                          <Chip
                            color={running ? "accent" : job.success ? "success" : "danger"}
                            size="sm"
                          >
                            <Chip.Label>
                              {running ? "运行中" : job.success ? "成功" : "失败"}
                            </Chip.Label>
                          </Chip>
                        </Table.Cell>
                        <Table.Cell>{formatDuration(job.duration)}</Table.Cell>
                        <Table.Cell>{job.startTime ?? "—"}</Table.Cell>
                        <Table.Cell className="max-w-xs truncate">{job.error || "—"}</Table.Cell>
                        <Table.Cell>
                          <Button
                            size="sm"
                            variant="danger-soft"
                            isDisabled={!running || busyId === job.id}
                            onPress={() => {
                              if (job.id != null) {
                                void stopJob(job.id);
                              }
                            }}
                          >
                            停止
                          </Button>
                        </Table.Cell>
                      </Table.Row>
                    );
                  })}
                </Table.Body>
              </Table.Content>
            </Table.ScrollContainer>
          </Table>
          </div>
        </div>
      ) : null}
    </MotionPage>
  );
}
