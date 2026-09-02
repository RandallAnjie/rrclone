"use client";

import { Table } from "@heroui/react";
import { PageHeader } from "@/components/page-header";
import { BlankState, ErrorState, LoadingState } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { useRc } from "@/lib/rc/use-rc";
import type { MountList } from "@/lib/rc/types";

export default function MountsPage() {
  const { host } = useHosts();
  const mounts = useRc<MountList>(host, "mount/listmounts", { intervalMs: 4000 });
  const items = mounts.data?.mountPoints ?? [];

  return (
    <div>
      <PageHeader title="挂载" description="当前 FUSE / 盘符挂载来自 mount/listmounts。" />
      {mounts.loading && !mounts.data ? <LoadingState /> : null}
      {mounts.error ? (
        <ErrorState message={mounts.error} onRetry={() => void mounts.refresh()} />
      ) : null}
      {!mounts.error && mounts.data && items.length === 0 ? (
        <BlankState
          title="没有活动挂载"
          description="rclone mount 之后会显示 fs 和本地挂载点。"
        />
      ) : null}
      {items.length > 0 ? (
        <Table>
          <Table.ScrollContainer>
            <Table.Content aria-label="活动挂载">
              <Table.Header>
                <Table.Column isRowHeader>远程</Table.Column>
                <Table.Column>挂载点</Table.Column>
                <Table.Column>时间</Table.Column>
              </Table.Header>
              <Table.Body>
                {items.map((item, index) => (
                  <Table.Row
                    key={`${item.Fs}-${item.MountPoint}-${index}`}
                    id={`${item.MountPoint}-${index}`}
                  >
                    <Table.Cell>{item.Fs || "—"}</Table.Cell>
                    <Table.Cell>{item.MountPoint || "—"}</Table.Cell>
                    <Table.Cell>{item.MountedOn || "—"}</Table.Cell>
                  </Table.Row>
                ))}
              </Table.Body>
            </Table.Content>
          </Table.ScrollContainer>
        </Table>
      ) : null}
    </div>
  );
}
