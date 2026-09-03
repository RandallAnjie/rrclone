"use client";

import { KPI } from "@heroui-pro/react/kpi";
import type { ReactNode } from "react";

type SparkPoint = Record<string, number | string>;

type KpiCardProps = {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  status?: "success" | "warning" | "danger";
  chart?: SparkPoint[];
};

export function KpiCard({ label, value, hint, status, chart }: KpiCardProps) {
  const numeric = typeof value === "number" ? value : undefined;

  return (
    <KPI>
      <KPI.Header>
        <KPI.Title>{label}</KPI.Title>
      </KPI.Header>
      <KPI.Content>
        {numeric == null ? (
          <p className="text-2xl font-semibold tracking-tight">{value}</p>
        ) : (
          <KPI.Value value={numeric} />
        )}
      </KPI.Content>
      {chart && chart.length > 1 ? (
        <KPI.Chart
          data={chart}
          dataKey="value"
          height={56}
          color={
            status === "danger"
              ? "var(--danger)"
              : status === "warning"
                ? "var(--warning)"
                : "var(--accent)"
          }
        />
      ) : null}
      {hint ? <KPI.Footer className="text-sm text-muted">{hint}</KPI.Footer> : null}
    </KPI>
  );
}
