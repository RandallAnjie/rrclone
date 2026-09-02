"use client";

import { ChartTooltip } from "@heroui-pro/react/chart-tooltip";
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Legend,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import type { ChartSlice, TrendPoint } from "@/lib/rc/insights";

export const CHART_COLORS = [
  "#8b9dff",
  "#3dd68c",
  "#f5a524",
  "#f31260",
  "#7dd3fc",
  "#c084fc",
  "#f5c451",
];

function EmptyChart({ label }: { label: string }) {
  return (
    <div
      role="img"
      aria-label={label}
      className="flex h-full min-h-52 items-center justify-center text-sm text-muted"
    >
      {label}
    </div>
  );
}

export function SpeedAreaChart({
  data,
  speedFormatter,
}: {
  data: TrendPoint[];
  speedFormatter: (value: number) => string;
}) {
  if (data.length < 2) {
    return <EmptyChart label="采集两点之后会画出速度曲线" />;
  }
  return (
    <div className="h-full w-full" role="img" aria-label="速度与并发折线图">
    <ResponsiveContainer width="100%" height="100%">
      <AreaChart data={data} margin={{ top: 8, right: 12, left: 0, bottom: 0 }}>
        <defs>
          <linearGradient id="speedFill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="#8b9dff" stopOpacity={0.35} />
            <stop offset="100%" stopColor="#8b9dff" stopOpacity={0.02} />
          </linearGradient>
        </defs>
        <CartesianGrid stroke="rgba(255,255,255,0.08)" vertical={false} />
        <XAxis dataKey="label" hide />
        <YAxis
          yAxisId="speed"
          width={64}
          tick={{ fill: "#9ba3b5", fontSize: 11 }}
          tickFormatter={(value: number) => speedFormatter(value).replace("/s", "")}
        />
        <YAxis
          yAxisId="count"
          orientation="right"
          width={28}
          tick={{ fill: "#9ba3b5", fontSize: 11 }}
          allowDecimals={false}
        />
        <Tooltip
          content={
            <ChartTooltip.Content
              valueFormatter={(value) =>
                typeof value === "number" && value > 20 ? speedFormatter(value) : String(value)
              }
            />
          }
        />
        <Area
          yAxisId="speed"
          type="monotone"
          dataKey="speed"
          name="速度"
          stroke="#8b9dff"
          strokeWidth={2}
          fill="url(#speedFill)"
          isAnimationActive={data.length < 8}
        />
        <Area
          yAxisId="count"
          type="monotone"
          dataKey="transferring"
          name="进行中"
          stroke="#3dd68c"
          strokeWidth={1.5}
          fill="transparent"
          isAnimationActive={data.length < 8}
        />
      </AreaChart>
    </ResponsiveContainer>
    </div>
  );
}

export function MemoryAreaChart({
  data,
  bytesFormatter,
}: {
  data: TrendPoint[];
  bytesFormatter: (value: number) => string;
}) {
  if (data.length < 2) {
    return <EmptyChart label="内存曲线将随轮询出现" />;
  }
  return (
    <div className="h-full w-full" role="img" aria-label="堆内存折线图">
    <ResponsiveContainer width="100%" height="100%">
      <AreaChart data={data} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
        <defs>
          <linearGradient id="heapFill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="#7dd3fc" stopOpacity={0.3} />
            <stop offset="100%" stopColor="#7dd3fc" stopOpacity={0.02} />
          </linearGradient>
        </defs>
        <CartesianGrid stroke="rgba(255,255,255,0.08)" vertical={false} />
        <XAxis dataKey="label" hide />
        <YAxis
          width={56}
          tick={{ fill: "#9ba3b5", fontSize: 11 }}
          tickFormatter={(value: number) => bytesFormatter(value)}
        />
        <Tooltip content={<ChartTooltip.Content valueFormatter={(value) => bytesFormatter(Number(value))} />} />
        <Area
          type="monotone"
          dataKey="heap"
          name="堆内存"
          stroke="#7dd3fc"
          strokeWidth={2}
          fill="url(#heapFill)"
          isAnimationActive={data.length < 8}
        />
      </AreaChart>
    </ResponsiveContainer>
    </div>
  );
}

export function DonutChart({
  data,
  valueFormatter,
  emptyLabel = "还没有可汇总的数据",
}: {
  data: ChartSlice[];
  valueFormatter?: (value: number) => string;
  emptyLabel?: string;
}) {
  if (data.length === 0) {
    return <EmptyChart label={emptyLabel} />;
  }
  return (
    <div className="h-full w-full" role="img" aria-label="占比饼图">
    <ResponsiveContainer width="100%" height="100%">
      <PieChart>
        <Pie
          data={data}
          dataKey="value"
          nameKey="name"
          innerRadius={58}
          outerRadius={84}
          paddingAngle={2}
          stroke="transparent"
        >
          {data.map((entry, index) => (
            <Cell key={entry.name} fill={CHART_COLORS[index % CHART_COLORS.length]} />
          ))}
        </Pie>
        <Tooltip
          content={
            <ChartTooltip.Content
              valueFormatter={(value) => (valueFormatter ? valueFormatter(Number(value)) : String(value))}
            />
          }
        />
        <Legend
          verticalAlign="bottom"
          formatter={(value) => <span className="text-xs text-muted">{value}</span>}
        />
      </PieChart>
    </ResponsiveContainer>
    </div>
  );
}

export function HorizontalBars({
  data,
  valueFormatter,
  emptyLabel = "没有可比较的条目",
}: {
  data: ChartSlice[];
  valueFormatter?: (value: number) => string;
  emptyLabel?: string;
}) {
  if (data.length === 0) {
    return <EmptyChart label={emptyLabel} />;
  }
  return (
    <div className="h-full w-full" role="img" aria-label="对比柱状图">
    <ResponsiveContainer width="100%" height="100%">
      <BarChart data={data} layout="vertical" margin={{ top: 8, right: 16, left: 8, bottom: 0 }}>
        <CartesianGrid stroke="rgba(255,255,255,0.08)" horizontal={false} />
        <XAxis type="number" hide />
        <YAxis
          type="category"
          dataKey="name"
          width={96}
          tick={{ fill: "#9ba3b5", fontSize: 11 }}
        />
        <Tooltip
          content={
            <ChartTooltip.Content
              valueFormatter={(value) => (valueFormatter ? valueFormatter(Number(value)) : String(value))}
            />
          }
        />
        <Bar dataKey="value" name="数值" radius={[0, 8, 8, 0]}>
          {data.map((entry, index) => (
            <Cell key={entry.name} fill={CHART_COLORS[index % CHART_COLORS.length]} />
          ))}
        </Bar>
      </BarChart>
    </ResponsiveContainer>
    </div>
  );
}
