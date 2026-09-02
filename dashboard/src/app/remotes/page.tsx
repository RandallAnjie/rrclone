"use client";

import { Chip, Table } from "@heroui/react";
import { PageHeader } from "@/components/page-header";
import { BlankState, ErrorState, LoadingState } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { redactRecord } from "@/lib/rc/format";
import { useRc } from "@/lib/rc/use-rc";
import type { ConfigDump } from "@/lib/rc/types";

export default function RemotesPage() {
  const { host } = useHosts();
  const dump = useRc<ConfigDump>(host, "config/dump", { intervalMs: 8000 });
  const remotes = Object.entries(dump.data ?? {});

  return (
    <div>
      <PageHeader
        title="远程"
        description="读取 config/dump。token、secret、password 等字段会自动打码。"
      />
      {dump.loading && !dump.data ? <LoadingState /> : null}
      {dump.error ? <ErrorState message={dump.error} onRetry={() => void dump.refresh()} /> : null}
      {!dump.error && dump.data && remotes.length === 0 ? (
        <BlankState
          title="还没有配置远程"
          description="用 rclone config 或 rc config/create 加完之后，这里会列出来。"
        />
      ) : null}
      {!dump.error && remotes.length > 0 ? (
        <Table>
          <Table.ScrollContainer>
            <Table.Content aria-label="rclone 远程">
              <Table.Header>
                <Table.Column isRowHeader>名称</Table.Column>
                <Table.Column>类型</Table.Column>
                <Table.Column>配置</Table.Column>
              </Table.Header>
              <Table.Body>
                {remotes.map(([name, config]) => {
                  const safe = redactRecord(config);
                  return (
                    <Table.Row key={name} id={name}>
                      <Table.Cell className="font-medium">{name}</Table.Cell>
                      <Table.Cell>
                        <Chip size="sm">
                          <Chip.Label>{safe.type || "unknown"}</Chip.Label>
                        </Chip>
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
      ) : null}
    </div>
  );
}
