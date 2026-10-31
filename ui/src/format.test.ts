import { describe, expect, it } from "vitest";
import { offset } from "./format";

describe("offset", () => {
  it.each([
    [undefined, ""],
    [0, "+0.0s"],
    [3.25, "+3.3s"],
    [-0.5, "-0.5s"],
  ])("writes %s as %j", (sec, want) => {
    expect(offset(sec)).toBe(want);
  });

  it("keeps the sign for long offsets", () => {
    expect(offset(90)).toMatch(/^\+/);
    expect(offset(-90)).toMatch(/^-/);
  });
});
