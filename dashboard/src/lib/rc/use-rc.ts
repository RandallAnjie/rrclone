"use client";

import { useCallback, useEffect, useState } from "react";
import { rcCall } from "./client";
import type { Host } from "./types";

type UseRcOptions = {
  intervalMs?: number;
  enabled?: boolean;
  params?: Record<string, unknown>;
};

export function useRc<T>(host: Host | null, path: string, options: UseRcOptions = {}) {
  const { intervalMs = 0, enabled = true, params } = options;
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const paramsKey = JSON.stringify(params ?? {});
  const loading = Boolean(host && enabled && data === null && error === null);

  const refresh = useCallback(async () => {
    if (!host || !enabled) {
      return;
    }
    try {
      const next = await rcCall<T>(host, path, params ?? {});
      setData(next);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败");
    }
    // params is represented by paramsKey so the callback stays stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, host, paramsKey, path]);

  useEffect(() => {
    if (!host || !enabled) {
      return;
    }
    // Poll rclone RC. The official hook lint treats any setState from a
    // fetch as a render cascade, but this is an external subscription.
    const run = () => {
      void refresh();
    };
    const starter = window.setTimeout(run, 0);
    const timer = intervalMs ? window.setInterval(run, intervalMs) : 0;
    return () => {
      window.clearTimeout(starter);
      if (timer) {
        window.clearInterval(timer);
      }
    };
  }, [enabled, host, intervalMs, refresh]);

  return { data, error, loading, refresh };
}
