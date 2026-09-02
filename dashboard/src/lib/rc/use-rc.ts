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
  const hostKey = host
    ? `${host.id}\0${host.url}\0${host.user ?? ""}\0${host.pass ?? ""}`
    : "";
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
    // host/params identity is captured via hostKey/paramsKey.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, hostKey, paramsKey, path]);

  useEffect(() => {
    if (!host || !enabled) {
      return;
    }
    let cancelled = false;
    const run = () => {
      if (!cancelled) {
        void refresh();
      }
    };
    run();
    const timer = intervalMs ? window.setInterval(run, intervalMs) : 0;
    return () => {
      cancelled = true;
      if (timer) {
        window.clearInterval(timer);
      }
    };
  }, [enabled, host, hostKey, intervalMs, refresh]);

  return { data, error, loading, refresh };
}
