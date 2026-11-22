/** A piece of plain text or an http(s) link. */
export type Segment = { kind: "text"; text: string } | { kind: "link"; text: string; href: string };

const URL_RE = /https?:\/\/[^\s<>"'）)\]}，。]+/g;

/**
 * Splits plain text into text and http(s) link segments. Nothing is ever
 * interpreted as HTML: callers render segments as React text and <a>.
 * Trailing punctuation is kept out of the link.
 */
export function linkify(text: string): Segment[] {
  const out: Segment[] = [];
  let last = 0;
  for (const m of text.matchAll(URL_RE)) {
    let url = m[0];
    const trail = /[.,;:!?]+$/.exec(url);
    if (trail) url = url.slice(0, -trail[0].length);
    const start = m.index ?? 0;
    if (start > last) out.push({ kind: "text", text: text.slice(last, start) });
    out.push({ kind: "link", text: url, href: url });
    last = start + url.length;
  }
  if (last < text.length) out.push({ kind: "text", text: text.slice(last) });
  return out;
}
