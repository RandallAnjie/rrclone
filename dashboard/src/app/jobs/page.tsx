"use client";

import { Button, Chip, Table } from "@heroui/react";
import { useEffect, useMemo, useState } from "react";
import { PageHeader } from "@/components/page-header";
import { BlankState, ErrorState, LoadingState } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { rcCall } from "@/lib/rclone/client";
import { formatDuration } from "@/lib/rclone/format";
import { useRc } from "@/lib/rclone/use-rc";
import type { JobList, JobStatus } from "@/lib/rclone/types";

export default function JobsPage() {
  const { host } = useHosts();
  const list = useRc<JobList>(host, "job/list", { intervalMs: 3000 });
  const ids = useMemo(() => list.data?.jobids ?? [], [list.data]);
  const [jobs, setJobs] = useState<JobStatus[]>([]);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      if (!list.data) {
        return;
      }
      try {
        const next = await Promise.all(
          ids.map((jobid) => rcCall<JobStatus>(host, "job/status", { jobid })),
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

  return (
    <div>
      <PageHeader title="任务" description="异步 RC 任务来自 job/list 和 job/status。" />
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
