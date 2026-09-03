"use client";

import { Widget } from "@heroui-pro/react/widget";
import { Table } from "@heroui/react";
import { DonutChart } from "@/components/charts";
import { LiveDot, MotionPage, Rise, Stagger } from "@/components/motion-ui";
import { PageHeader } from "@/components/page-header";
import { BlankState, ErrorState, LoadingState, SoftNotice } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { formatBytes } from "@/lib/rc/format";
import { useRc } from "@/lib/rc/use-rc";
import type { MountList, VfsStats } from "@/lib/rc/types";

export default function MountsPage() {
  const { host } = useHosts();
  const mounts = useRc<MountList>(host, "mount/listmounts", { intervalMs: 4000 });
  const vfs = useRc<VfsStats>(host, "vfs/stats", { intervalMs: 4000 });
  const items = mounts.data?.mountPoints ?? [];
  const vfsSlices = [
    { name: "缓存文件", value: vfs.data?.diskCache?.files ?? 0 },
    { name: "元数据文件", value: vfs.data?.metadataCache?.files ?? 0 },
    { name: "目录", value: vfs.data?.metadataCache?.dirs ?? 0 },
    { name: "出错文件", value: vfs.data?.diskCache?.erroredFiles ?? 0 },
  ].filter((item) => item.value > 0);

  return (
    <MotionPage>
      <PageHeader
        title="挂载"
        description="当前 FUSE / 盘符挂载来自 mount/listmounts，VFS 缓存来自 vfs/stats。"
        meta={<LiveDot active={Boolean(mounts.data)} label="4 秒刷新挂载" />}
      />
      {mounts.loading && !mounts.data ? <LoadingState /> : null}
      {mounts.error && !mounts.data ? (
        <ErrorState message={mounts.error} onRetry={() => void mounts.refresh()} />
      ) : null}
      {mounts.stale ? <SoftNotice message={`挂载列表暂时失败：${mounts.error}`} /> : null}
      {vfs.error && !vfs.data && !/no VFS active/i.test(vfs.error) ? (
        <SoftNotice message={`VFS：${vfs.error}`} onRetry={() => void vfs.refresh()} />
      ) : null}
      <Stagger className="mb-6 grid gap-4 lg:grid-cols-2">
        <Rise>
          <Widget>
            <Widget.Header>
              <Widget.Title>VFS 缓存</Widget.Title>
              <Widget.Description>
                占用 {formatBytes(vfs.data?.diskCache?.bytesUsed)} · inUse {vfs.data?.inUse ?? 0}
              </Widget.Description>
            </Widget.Header>
            <Widget.Content>
              <div className="chart-panel">
                <DonutChart data={vfsSlices} emptyLabel="当前没有 VFS 缓存统计" />
              </div>
            </Widget.Content>
          </Widget>
        </Rise>
      </Stagger>
      {mounts.data && items.length === 0 ? (
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
    </MotionPage>
  );
}
