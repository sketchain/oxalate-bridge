/** Ink square with a yellow arch bridge, plus the condensed word mark. */
export function LogoMark({ size = 36 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true" className="shrink-0">
      {/* Fixed black square (both themes); a thin ink frame keeps it visible on the dark background. */}
      <rect x="0.5" y="0.5" width="31" height="31" fill="#0A0A0A" className="stroke-ink" strokeWidth="1" />
      <path d="M4 24 V20 Q16 5 28 20 V24" fill="none" stroke="#F2C200" strokeWidth="3" strokeLinecap="square" />
      <path d="M10 24 V16.5 M16 24 V12.5 M22 24 V16.5" stroke="#F2C200" strokeWidth="2" strokeLinecap="square" />
      <path d="M2 25 H30" stroke="#F2C200" strokeWidth="2" strokeLinecap="square" />
    </svg>
  );
}

export function Logo({ sub }: { sub?: string }) {
  return (
    <div className="flex items-center gap-3">
      <LogoMark />
      <div className="min-w-0 leading-none">
        <div className="font-display text-[17px] tracking-tight uppercase">OXALATE&apos;S BRIDGE</div>
        {sub ? <div className="num mt-1 text-[10px] text-muted uppercase">{sub}</div> : null}
      </div>
    </div>
  );
}
