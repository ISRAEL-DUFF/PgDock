import { describe, expect, it } from "vitest";
import { toCSV } from "./csv";

describe("toCSV", () => {
  it("quotes only when needed and leaves NULL empty", () => {
    expect(toCSV(["id", "body"], [["1", "plain"], ["2", 'say "hi", ok'], ["3", null], ["4", "two\nlines"]])).toBe(
      'id,body\r\n1,plain\r\n2,"say ""hi"", ok"\r\n3,\r\n4,"two\nlines"\r\n',
    );
  });
  it("defuses spreadsheet formulas", () => {
    expect(toCSV(["v"], [["=1+1"], ["-2"], ["-x"], ["@x"]])).toBe("v\r\n'=1+1\r\n-2\r\n'-x\r\n'@x\r\n");
  });
});
