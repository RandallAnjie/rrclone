import { describe, expect, it } from "vitest";
import { appendSparkline } from "./sparkline";

describe("appendSparkline", () => {
  it("keeps the latest 24 samples", () => {
    let history: Array<{ value: number }> = [];
    for (let i = 0; i < 30; i += 1) {
      history = appendSparkline(history, i);
    }
    expect(history).toHaveLength(24);
    expect(history[0]).toEqual({ value: 6 });
    expect(history[23]).toEqual({ value: 29 });
  });

  it("ignores empty samples", () => {
    expect(appendSparkline([{ value: 1 }], undefined)).toEqual([{ value: 1 }]);
  });
});
