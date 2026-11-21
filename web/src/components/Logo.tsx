import { LOGO_BAR, LOGO_GLYPHS } from "./logoGlyphs.gen";

/**
 * Reagent-label mark: a reagent-yellow square with a 2px ink border, the
 * oxalate ion C₂O₄²⁻ set in IBM Plex Mono SemiBold (real subscripts and a
 * stacked superscript, as outlines from scripts/gen-logo.py) and a 3px ink
 * bar under it. The colours are fixed in both themes.
 */
export function LogoMark({ size = 36, title }: { size?: number; title?: string }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 36 36"
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      className="shrink-0"
      data-testid="logo-mark"
    >
      <rect x="1" y="1" width="34" height="34" fill="#F2C200" stroke="#0A0A0A" strokeWidth="2" />
      <path fill="#0A0A0A" d={LOGO_GLYPHS} />
      <rect x={LOGO_BAR.x} y={LOGO_BAR.y} width={LOGO_BAR.width} height={LOGO_BAR.height} fill="#0A0A0A" />
    </svg>
  );
}

export function Logo({ sub, size }: { sub?: string; size?: number }) {
  return (
    <div className="flex items-center gap-3">
      <LogoMark size={size} />
      <div className="min-w-0 leading-none">
        <div className="font-display text-[17px] tracking-tight uppercase">OXALATE&apos;S BRIDGE</div>
        {sub ? <div className="num mt-1 text-[10px] text-muted uppercase">{sub}</div> : null}
      </div>
    </div>
  );
}
