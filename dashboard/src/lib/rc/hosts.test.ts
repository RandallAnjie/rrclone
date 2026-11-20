import { describe, expect, it } from "vitest";
import { applyHostDraft, LOCAL_HOST } from "./hosts";

describe("applyHostDraft", () => {
  it("lets the locked 本机 entry change RC port", () => {
    const next = applyHostDraft(LOCAL_HOST, {
      name: "本机",
      url: "http://127.0.0.1:5573/",
      user: "gui",
      pass: "secret",
    });
    expect(next.id).toBe("local");
    expect(next.locked).toBe(true);
    expect(next.url).toBe("http://127.0.0.1:5573");
    expect(next.user).toBe("gui");
    expect(next.pass).toBe("secret");
  });
});
