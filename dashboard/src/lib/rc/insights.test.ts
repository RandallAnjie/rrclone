import { describe, expect, it } from "vitest";
import {
  appendTrend,
  bytesSlices,
  jobStatusSlices,
  operationSlices,
  overallPercent,
  remoteTypeSlices,
  transferStatusSlices,
  transferredOutcomeSlices,
} from "./insights";
import type { CoreStats } from "./types";

const stats: CoreStats = {
  bytes: 50,
  totalBytes: 200,
  speed: 10,
  transfers: 4,
  checks: 2,
  deletes: 1,
  errors: 1,
  transferring: [{ name: "a.bin", size: 10 }],
  checking: ["b"],
  serverSideCopies: 3,
};

describe("appendTrend", () => {
  it("appends and caps history", () => {
    let history = appendTrend([], stats);
    expect(history).toHaveLength(1);
    expect(history[0]?.speed).toBe(10);
    for (let i = 0; i < 100; i += 1) {
      history = appendTrend(history, { ...stats, speed: i });
    }
    expect(history).toHaveLength(90);
    expect(history.at(-1)?.speed).toBe(99);
  });
});

describe("slices", () => {
  it("builds transfer and byte shares", () => {
    expect(transferStatusSlices(stats).map((item) => item.name)).toEqual([
      "进行中",
      "校验中",
      "已完成",
      "错误",
    ]);
    expect(bytesSlices(stats)).toEqual([
      { name: "已传输", value: 50 },
      { name: "剩余", value: 150 },
    ]);
    expect(overallPercent(stats)).toBe(25);
  });

  it("counts remotes, jobs, and completed outcomes", () => {
    expect(
      remoteTypeSlices({
        a: { type: "drive" },
        b: { type: "drive" },
        c: { type: "s3" },
      }),
    ).toEqual([
      { name: "drive", value: 2 },
      { name: "s3", value: 1 },
    ]);
    expect(
      jobStatusSlices([
        { finished: false },
        { finished: true, success: true },
        { finished: true, success: false },
      ]),
    ).toEqual([
      { name: "运行中", value: 1 },
      { name: "成功", value: 1 },
      { name: "失败", value: 1 },
    ]);
    expect(
      transferredOutcomeSlices([
        { name: "ok" },
        { name: "chk", checked: true },
        { name: "bad", error: "boom" },
      ]),
    ).toEqual([
      { name: "成功", value: 1 },
      { name: "仅校验", value: 1 },
      { name: "失败", value: 1 },
    ]);
    expect(operationSlices(stats).find((item) => item.name === "服务端复制")?.value).toBe(3);
  });
});
