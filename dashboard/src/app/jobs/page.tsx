"use client";

import { Button, Chip, Table } from "@heroui/react";
import { useEffect, useMemo, useState } from "react";
import { PageHeader } from "@/components/page-header";
import { BlankState, ErrorState, LoadingState } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { rcCall } from "@/lib/rc/client";
import { formatDuration } from "@/lib/rc/format";
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
  const [jobs, setJobs] = useState<JobStatus[]>([]);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      if (!list.data) {
        return;
      }
      if (ids.length === 0) {
        if (!cancelled) {
          setJobs([]);
          setDetailError(null);
        }
        return;
      }
      try {
        const next = await mapPool(ids, 6, (jobid) =>
          rcCall<JobStatus>(host, "job/status", { jobid }),
        );
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
  }, [host, ids, list.data]);

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

  const error = list.error || detailError;
  const totalKnown =
    (list.data?.runningIds?.length ?? 0) + (list.data?.finishedIds?.length ?? list.data?.jobids?.length ?? 0);

  return (
    <div>
      <PageHeader
        title="任务"
        description={`异步 RC 任务来自 job/list 和 job/status。当前展示运行中 + 最近 ${MAX_FINISHED} 条完成记录${
          totalKnown > ids.length ? `（共 ${totalKnown}）` : ""
        }。`}
      />
      {list.loading && !list.data ? <LoadingState /> : null}
      {error ? <ErrorState message={error} onRetry={() => void list.refresh()} /> : null}
      {!list.error && list.data && ids.length === 0 ? (
        <BlankState title="没有最近任务" description="带 _async=true 的 copy/sync 会出现在这里。" />
      ) : null}
      {jobs.length > 0 ? (
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
      ) : null}
    </div>
  );
}
