import { describe, expect, it } from "vitest";
import { accountName, accountState, allCoolingDown, summarizeRotation } from "./rotation";

const rotators = [
  {
    name: "dropbox",
    current: "/a/app2.json",
    switches: 3,
    accounts: [
      { file: "/a/app1.json", cooling_down: true, cooldown_left: 120 },
      { file: "/a/app2.json", active: true },
      { file: "/a/app3.json" },
    ],
  },
  {
    name: "drive",
    switches: 1,
    accounts: [
      { file: "C:\\tok\\one.json", active: true, cooling_down: true },
      { file: "C:\\tok\\two.json", cooling_down: true },
    ],
  },
];

describe("summarizeRotation", () => {
  it("adds up remotes, accounts, cooldowns and switches", () => {
    expect(summarizeRotation(rotators)).toEqual({ remotes: 2, accounts: 5, coolingDown: 3, switches: 4 });
  });

  it("handles no rotation", () => {
    expect(summarizeRotation([])).toEqual({ remotes: 0, accounts: 0, coolingDown: 0, switches: 0 });
  });
});

describe("accountName", () => {
  it("takes the last part of either kind of path", () => {
    expect(accountName("/a/app1.json")).toBe("app1.json");
    expect(accountName("C:\\tok\\one.json")).toBe("one.json");
    expect(accountName(undefined)).toBe("—");
  });
});

describe("accountState", () => {
  it("tells active, cooling and ready apart", () => {
    expect(accountState({ file: "x", active: true })).toBe("active");
    expect(accountState({ file: "x", active: true, cooling_down: true })).toBe("cooling");
    expect(accountState({ file: "x" })).toBe("ready");
  });
});

describe("allCoolingDown", () => {
  it("is true only when every account rests", () => {
    expect(allCoolingDown(rotators[0])).toBe(false);
    expect(allCoolingDown(rotators[1])).toBe(true);
    expect(allCoolingDown({ name: "empty" })).toBe(false);
  });
});
