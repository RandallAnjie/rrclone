"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { rcCall } from "./client";
import type { Host } from "./types";

type UseRcOptions = {
  intervalMs?: number;
  enabled?: boolean;
  params?: Record<string, unknown>;
};

type Box<T> = {
  identity: string;
  data: T | null;
  error: string | null;
  stale: boolean;
};

export function useRc<T>(host: Host | null, path: string, options: UseRcOptions = {}) {
  const { intervalMs = 0, enabled = true, params } = options;
  const paramsKey = JSON.stringify(params ?? {});
  const hostKey = host
    ? `${host.id}\0${host.url}\0${host.user ?? ""}\0${host.pass ?? ""}`
    : "";
  const identity = `${hostKey}|${path}|${paramsKey}`;
  const reqId = useRef(0);

  const [box, setBox] = useState<Box<T>>({
    identity,
    data: null,
    error: null,
    stale: false,
  });

  const data = box.identity === identity ? box.data : null;
  const error = box.identity === identity ? box.error : null;
  const stale = box.identity === identity ? box.stale : false;
  const loading = Boolean(host && enabled && data === null && error === null);

  const refresh = useCallback(async () => {
    if (!host || !enabled) {
      return;
    }
    const id = ++reqId.current;
    const captured = identity;
    try {
      const next = await rcCall<T>(host, path, params ?? {});
      if (id !== reqId.current) {
        return;
      }
      setBox({ identity: captured, data: next, error: null, stale: false });
    } catch (err) {
      if (id !== reqId.current) {
        return;
      }
      const message = err instanceof Error ? err.message : "请求失败";
      setBox((prev) => ({
        identity: captured,
        data: prev.identity === captured ? prev.data : null,
        error: message,
        stale: prev.identity === captured && prev.data !== null,
      }));
    }
    // host/params identity is captured via identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, identity, path]);

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
      reqId.current += 1;
      if (timer) {
        window.clearInterval(timer);
      }
    };
  }, [enabled, host, identity, intervalMs, refresh]);

  return { data, error, loading, stale, refresh };
}
