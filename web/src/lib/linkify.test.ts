import { describe, expect, it } from "vitest";
import { linkify } from "./linkify";

describe("linkify", () => {
  it("finds http(s) links and leaves the rest as text", () => {
    expect(linkify("见 https://example.com/a?b=1。谢谢")).toEqual([
      { kind: "text", text: "见 " },
      { kind: "link", text: "https://example.com/a?b=1", href: "https://example.com/a?b=1" },
      { kind: "text", text: "。谢谢" },
    ]);
  });
  it("never turns HTML or other schemes into links", () => {
    expect(linkify('<a href="javascript:alert(1)">x</a> javascript:alert(1)')).toEqual([{ kind: "text", text: '<a href="javascript:alert(1)">x</a> javascript:alert(1)' }]);
  });
  it("drops trailing punctuation", () => {
    expect(linkify("go to http://a.test.")[1]).toEqual({ kind: "link", text: "http://a.test", href: "http://a.test" });
  });
});
