"use client";

import { Widget } from "@heroui-pro/react/widget";
import { Chip, Table } from "@heroui/react";
import { KpiCard } from "@/components/kpi-card";
import { LiveDot, MotionPage, Rise, Stagger } from "@/components/motion-ui";
import { PageHeader } from "@/components/page-header";
import { BlankState, ErrorState, LoadingState, SoftNotice } from "@/components/state-views";
import { useHosts } from "@/components/host-provider";
import { formatDuration } from "@/lib/rc/format";
import { accountName, accountState, allCoolingDown, summarizeRotation } from "@/lib/rc/rotation";
import { useRc } from "@/lib/rc/use-rc";
import type { RotationAccount, RotationList, RotationStatus } from "@/lib/rc/types";

function StateChip({ account }: { account: RotationAccount }) {
  switch (accountState(account)) {
    case "active":
      return (
        <Chip color="success" size="sm">
          <Chip.Label>使用中</Chip.Label>
        </Chip>
      );
    case "cooling":
      return (
        <Chip color="warning" size="sm">
          <Chip.Label>冷却中 {formatDuration(account.cooldown_left)}</Chip.Label>
        </Chip>
      );
    default:
      return (
        <Chip size="sm">
          <Chip.Label>可用</Chip.Label>
        </Chip>
      );
  }
}

function RotatorCard({ rotator }: { rotator: RotationStatus }) {
  const accounts = rotator.accounts ?? [];
  const last = rotator.last_switch ? new Date(rotator.last_switch).toLocaleString() : null;
  return (
    <Widget>
      <Widget.Header>
        <Widget.Title>{rotator.name}</Widget.Title>
        <Widget.Description>
          当前 {accountName(rotator.current)} · 已切换 {rotator.switches ?? 0} 次 · 冷却{" "}
          {formatDuration(rotator.cooldown)}
          {last ? ` · 上次 ${last}（${rotator.last_reason || "—"}）` : ""}
        </Widget.Description>
      </Widget.Header>
      <Widget.Content>
        {allCoolingDown(rotator) ? (
          <SoftNotice message="所有账号都在冷却，请求会等到最早的账号恢复。" />
        ) : null}
        <Table>
          <Table.ScrollContainer>
            <Table.Content aria-label={`${rotator.name} 的账号`}>
              <Table.Header>
                <Table.Column isRowHeader>账号文件</Table.Column>
                <Table.Column>状态</Table.Column>
                <Table.Column>恢复时间</Table.Column>
              </Table.Header>
              <Table.Body>
                {accounts.map((account) => (
                  <Table.Row key={account.file} id={account.file}>
                    <Table.Cell>
                      <span title={account.file}>{accountName(account.file)}</span>
                    </Table.Cell>
                    <Table.Cell>
                      <StateChip account={account} />
                    </Table.Cell>
                    <Table.Cell>
                      {account.cooldown_until ? new Date(account.cooldown_until).toLocaleTimeString() : "—"}
                    </Table.Cell>
                  </Table.Row>
                ))}
              </Table.Body>
            </Table.Content>
          </Table.ScrollContainer>
        </Table>
      </Widget.Content>
    </Widget>
  );
}

export default function RotationPage() {
  const { host } = useHosts();
  const status = useRc<RotationList>(host, "rotation/status", { intervalMs: 5000 });
  const rotators = status.data?.rotators ?? [];
  const summary = summarizeRotation(rotators);

  return (
    <MotionPage>
      <PageHeader
        title="账号轮换"
        description="oauth_account_files 多账号轮换的状态，来自 rotation/status。"
        meta={<LiveDot active={Boolean(status.data)} label="5 秒刷新" />}
      />
      {status.loading && !status.data ? <LoadingState /> : null}
      {status.error && !status.data ? (
        <ErrorState
          message={
            /not found|couldn't find/i.test(status.error)
              ? "这个 rclone 没有 rotation/status，需要 rrclone 的新版本。"
              : status.error
          }
          onRetry={() => void status.refresh()}
        />
      ) : null}
      {status.stale ? <SoftNotice message={`轮换状态暂时读取失败：${status.error}`} /> : null}
      {status.data ? (
        <Stagger className="mb-6 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <Rise>
            <KpiCard label="轮换中的远程" value={summary.remotes} />
          </Rise>
          <Rise>
            <KpiCard label="账号" value={summary.accounts} />
          </Rise>
          <Rise>
            <KpiCard
              label="冷却中"
              value={summary.coolingDown}
              status={summary.coolingDown > 0 ? "warning" : "success"}
            />
          </Rise>
          <Rise>
            <KpiCard label="累计切换" value={summary.switches} />
          </Rise>
        </Stagger>
      ) : null}
      {status.data && rotators.length === 0 ? (
        <BlankState
          title="没有正在轮换的远程"
          description="使用了 oauth_account_files 的 Dropbox / Drive 远程被访问后会显示在这里。"
        />
      ) : null}
      <div className="grid gap-4">
        {rotators.map((rotator) => (
          <Rise key={rotator.name}>
            <RotatorCard rotator={rotator} />
          </Rise>
        ))}
      </div>
    </MotionPage>
  );
}
