"use client";

import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { appendTrend, type TrendPoint } from "@/lib/rc/insights";
import { useRc } from "@/lib/rc/use-rc";
import type { CoreMemStats, CoreStats } from "@/lib/rc/types";
import { useHosts } from "./host-provider";

type StatsTrendValue = {
  stats: ReturnType<typeof useRc<CoreStats>>;
  mem: ReturnType<typeof useRc<CoreMemStats>>;
  trend: TrendPoint[];
};

const StatsTrendContext = createContext<StatsTrendValue | null>(null);

export function StatsTrendProvider({ children }: { children: ReactNode }) {
  const { host } = useHosts();
  const stats = useRc<CoreStats>(host, "core/stats", { intervalMs: 2000 });
  const mem = useRc<CoreMemStats>(host, "core/memstats", { intervalMs: 4000 });
  const [trend, setTrend] = useState<TrendPoint[]>([]);
  const lastHostId = useRef(host.id);

  useEffect(() => {
    const starter = window.setTimeout(() => {
      setTrend((prev) => {
        if (lastHostId.current !== host.id) {
          lastHostId.current = host.id;
          return appendTrend([], stats.data, mem.data);
        }
        return appendTrend(prev, stats.data, mem.data);
      });
    }, 0);
    return () => window.clearTimeout(starter);
  }, [host.id, mem.data, stats.data]);

  return (
    <StatsTrendContext.Provider value={{ stats, mem, trend }}>{children}</StatsTrendContext.Provider>
  );
}

export function useStatsTrend(): StatsTrendValue {
  const ctx = useContext(StatsTrendContext);
  if (!ctx) {
    throw new Error("useStatsTrend must be used within StatsTrendProvider");
  }
  return ctx;
}
