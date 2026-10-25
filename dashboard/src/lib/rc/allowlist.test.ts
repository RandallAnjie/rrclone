import { describe, expect, it } from "vitest";
import { isAllowedTarget, isSameOrigin, parseAllowedHosts } from "./allowlist";

describe("parseAllowedHosts", () => {
  it("splits and normalises the list", () => {
    expect(parseAllowedHosts(" NAS.local , 10.0.0.2:5572,,")).toEqual(["nas.local", "10.0.0.2:5572"]);
    expect(parseAllowedHosts(undefined)).toEqual([]);
  });
});

describe("isAllowedTarget", () => {
  it("always allows loopback", () => {
    expect(isAllowedTarget(new URL("http://127.0.0.1:5572"), [])).toBe(true);
    expect(isAllowedTarget(new URL("http://localhost:5572"), [])).toBe(true);
    expect(isAllowedTarget(new URL("http://[::1]:5572"), [])).toBe(true);
  });

  it("rejects other hosts unless listed", () => {
    expect(isAllowedTarget(new URL("http://169.254.169.254/latest"), [])).toBe(false);
    expect(isAllowedTarget(new URL("http://nas.local:5572"), ["nas.local"])).toBe(true);
    expect(isAllowedTarget(new URL("http://10.0.0.2:5572"), ["10.0.0.2:5572"])).toBe(true);
    expect(isAllowedTarget(new URL("http://10.0.0.2:8080"), ["10.0.0.2:5572"])).toBe(false);
    expect(isAllowedTarget(new URL("http://anything:1"), ["*"])).toBe(true);
  });
});

describe("isSameOrigin", () => {
  it("accepts same origin and requests without Origin", () => {
    expect(isSameOrigin(null, "127.0.0.1:3000")).toBe(true);
    expect(isSameOrigin("http://127.0.0.1:3000", "127.0.0.1:3000")).toBe(true);
  });

  it("rejects cross-site requests", () => {
    expect(isSameOrigin("https://evil.example", "127.0.0.1:3000")).toBe(false);
    expect(isSameOrigin("null", "127.0.0.1:3000")).toBe(false);
    expect(isSameOrigin("http://127.0.0.1:3000", null)).toBe(false);
  });
});
