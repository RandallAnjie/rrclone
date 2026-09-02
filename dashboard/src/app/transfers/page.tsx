"use client";

import { Chip, ProgressBar, Table, Tabs } from "@heroui/react";
import { PageHeader } from "@/components/page-header";
import { BlankState, ErrorState, LoadingState } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { formatBytes, formatDuration, formatPercent, formatSpeed, formatTime } from "@/lib/rc/format";
import { useRc } from "@/lib/rc/use-rc";
import type { CoreStats, CoreTransferred } from "@/lib/rc/types";

export default function TransfersPage() {
  const { host } = useHosts();
  const stats = useRc<CoreStats>(host, "core/stats", { intervalMs: 2000 });
  const done = useRc<CoreTransferred>(host, "core/transferred", { intervalMs: 4000 });
  const error = stats.error || done.error;

  return (
    <div>
      <PageHeader
        title="传输"
        description="活动传输来自 core/stats，最近完成记录来自 core/transferred。"
      />
      {stats.loading && !stats.data ? <LoadingState /> : null}
      {error ? <ErrorState message={error} onRetry={() => void stats.refresh()} /> : null}
      {!error && stats.data ? (
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
      ) : null}
    </div>
  );
}
