import { Fragment } from "react";
import { linkify } from "@/lib/linkify";

/** Plain-text announcement body: line breaks and http(s) links only, never HTML. */
export function AnnouncementText({ text, className }: { text: string; className?: string }) {
  const lines = text.split(/\r?\n/);
  return (
    <p className={className}>
      {lines.map((line, i) => (
        <Fragment key={i}>
          {i > 0 ? <br /> : null}
          {linkify(line).map((s, j) =>
            s.kind === "link" ? (
              <a key={j} href={s.href} target="_blank" rel="noopener noreferrer nofollow" className="font-semibold break-all underline underline-offset-2">
                {s.text}
              </a>
            ) : (
              <Fragment key={j}>{s.text}</Fragment>
            ),
          )}
        </Fragment>
      ))}
    </p>
  );
}
