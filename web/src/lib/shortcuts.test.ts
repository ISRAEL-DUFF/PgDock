import { describe, expect, it } from "vitest";
import { goSequence, isMac, keyLabel, keyParts } from "./shortcuts";

describe("shortcuts", () => {
  it("names keys for the platform", () => {
    expect(isMac("MacIntel")).toBe(true);
    expect(isMac("Win32")).toBe(false);
    expect(keyLabel("mod+shift+f", true)).toBe("⌘⇧F");
    expect(keyLabel("mod+shift+f", false)).toBe("Ctrl+Shift+F");
    expect(keyLabel("mod+enter", false)).toBe("Ctrl+Enter");
    expect(keyParts("g t", true)).toEqual(["G", "then", "T"]);
    expect(keyParts("?", true)).toEqual(["?"]);
  });

  it("completes g then a letter within the timeout", () => {
    const s = goSequence(1000);
    expect(s.feed("t", 0)).toBeNull();
    expect(s.feed("g", 100)).toBeNull();
    expect(s.armed).toBe(true);
    expect(s.feed("t", 500)).toBe("t");
    expect(s.armed).toBe(false);
    expect(s.feed("t", 600)).toBeNull();
    s.feed("g", 1000);
    expect(s.feed("s", 2500)).toBeNull();
  });
});
