"use client";

import { EmptyState } from "@heroui-pro/react/empty-state";
import { Alert, Button, Spinner } from "@heroui/react";

export function LoadingState({ label = "正在读取 rclone 状态…" }: { label?: string }) {
  return (
    <div className="flex items-center gap-3 py-10 text-sm text-muted">
      <Spinner size="sm" />
      <span>{label}</span>
    </div>
  );
}

export function SoftNotice({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}) {
  return (
    <Alert status="warning" className="mb-4">
      <Alert.Content>
        <Alert.Title>部分数据暂时不可用</Alert.Title>
        <Alert.Description>{message}。已保留上一轮成功结果。</Alert.Description>
        {onRetry ? (
          <Button className="mt-3" size="sm" variant="secondary" onPress={onRetry}>
            重试
          </Button>
        ) : null}
      </Alert.Content>
    </Alert>
  );
}

export function ErrorState({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}) {
  return (
    <Alert status="danger">
      <Alert.Content>
        <Alert.Title>无法连接当前主机</Alert.Title>
        <Alert.Description>
          {message}。请确认已经启动{" "}
          <code>rclone rcd --rc-addr 127.0.0.1:5572 --rc-no-auth</code>
          ，或到「主机」页填入正确的 RC 地址和认证。
        </Alert.Description>
        {onRetry ? (
          <Button className="mt-3" size="sm" variant="secondary" onPress={onRetry}>
            重试
          </Button>
        ) : null}
      </Alert.Content>
    </Alert>
  );
}

export function BlankState({ title, description }: { title: string; description: string }) {
  return (
    <EmptyState className="py-10">
      <EmptyState.Header>
        <EmptyState.Title>{title}</EmptyState.Title>
        <EmptyState.Description>{description}</EmptyState.Description>
      </EmptyState.Header>
    </EmptyState>
  );
}
