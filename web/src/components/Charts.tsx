import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Bar, BarChart, CartesianGrid, Legend, Line, LineChart, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { formatUSD } from "@/lib/format";

export interface ChartColors {
  ink: string;
  bg: string;
  muted: string;
  rule: string;
  chart: string;
  yellow: string;
  red: string;
}

function readColors(): ChartColors {
  const cs = getComputedStyle(document.documentElement);
  const v = (n: string) => cs.getPropertyValue(n).trim() || "#0a0a0a";
  return { ink: v("--ink"), bg: v("--bg"), muted: v("--muted"), rule: v("--rule"), chart: v("--chart"), yellow: v("--yellow"), red: v("--red") };
}

/** Palette from the CSS tokens; follows the theme class on <html>. */
export function useChartColors(): ChartColors {
  const [c, setC] = useState(readColors);
  useEffect(() => {
    const update = () => requestAnimationFrame(() => setC(readColors()));
    const obs = new MutationObserver(update);
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
    return () => obs.disconnect();
  }, []);
  return c;
}

/** Wraps a chart with a readable description (aria) and a fixed height. */
export function ChartFrame({ description, height = 240, children }: { description: string; height?: number; children: ReactNode }) {
  return (
    <figure role="img" aria-label={description} className="m-0 w-full min-w-0" style={{ height }}>
      <figcaption className="sr-only">{description}</figcaption>
      <div aria-hidden="true" className="h-full w-full">
        {children}
      </div>
    </figure>
  );
}

function axisProps(c: ChartColors) {
  return {
    stroke: c.ink,
    strokeWidth: 2,
    tick: { fill: c.ink, fontSize: 11, fontFamily: "IBM Plex Mono, monospace" },
    tickLine: { stroke: c.ink },
  };
}

function tooltipProps(c: ChartColors) {
  return {
    cursor: { fill: c.rule, fillOpacity: 0.6 },
    contentStyle: { background: c.bg, border: `2px solid ${c.ink}`, borderRadius: 0, boxShadow: "none", fontSize: 12 },
    labelStyle: { color: c.ink, fontWeight: 700, fontFamily: "IBM Plex Mono, monospace" },
    itemStyle: { color: c.ink, fontFamily: "IBM Plex Mono, monospace" },
    isAnimationActive: false,
  };
}

const usdTick = (v: number) => (Math.abs(v) >= 10_000_000 ? `$${Math.round(v / 1_000_000)}` : `$${(v / 1_000_000).toFixed(2)}`);

export interface TierPoint {
  label: string;
  plan: number;
  standard: number;
  force: number;
}

/** Bars stacked by tier: plan = ink, standard = chart grey, force = yellow with an ink stroke. */
export function TierStackedBars({ data, description, height }: { data: TierPoint[]; description: string; height?: number }) {
  const { t } = useTranslation();
  const c = useChartColors();
  return (
    <ChartFrame description={description} height={height}>
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }} barCategoryGap="20%">
          <CartesianGrid stroke={c.rule} vertical={false} />
          <XAxis dataKey="label" {...axisProps(c)} interval="preserveStartEnd" minTickGap={8} />
          <YAxis {...axisProps(c)} tickFormatter={usdTick} width={52} />
          <Tooltip {...tooltipProps(c)} formatter={(v) => formatUSD(Number(v))} />
          <Legend wrapperStyle={{ fontSize: 12, color: c.ink }} iconType="square" />
          <Bar dataKey="plan" name={t("tier.plan")} stackId="t" fill={c.ink} isAnimationActive={false} />
          <Bar dataKey="standard" name={t("tier.standard")} stackId="t" fill={c.chart} isAnimationActive={false} />
          <Bar dataKey="force" name={t("tier.force")} stackId="t" fill={c.yellow} stroke={c.ink} strokeWidth={1} isAnimationActive={false} />
        </BarChart>
      </ResponsiveContainer>
    </ChartFrame>
  );
}

/** Horizontal bars of one value per category (model distribution). */
export function CategoryBars({
  data,
  description,
  format = formatUSD,
}: {
  data: { name: string; value: number }[];
  description: string;
  format?: (v: number) => string;
}) {
  const c = useChartColors();
  const height = Math.max(120, data.length * 36 + 40);
  return (
    <ChartFrame description={description} height={height}>
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} layout="vertical" margin={{ top: 4, right: 16, bottom: 0, left: 8 }}>
          <CartesianGrid stroke={c.rule} horizontal={false} />
          <XAxis type="number" {...axisProps(c)} tickFormatter={(v) => format(Number(v))} />
          <YAxis type="category" dataKey="name" {...axisProps(c)} width={130} />
          <Tooltip {...tooltipProps(c)} formatter={(v) => format(Number(v))} />
          <Bar dataKey="value" fill={c.ink} isAnimationActive={false} barSize={18} />
        </BarChart>
      </ResponsiveContainer>
    </ChartFrame>
  );
}

export interface LineSeries {
  key: string;
  name: string;
  color: "ink" | "chart" | "yellow";
  dashed?: boolean;
}

/** Line chart over a time axis with optional reference lines (reset points). */
export function TimeLines<T extends { t: number }>({
  data,
  series,
  description,
  yFormat,
  marks = [],
  yDomain,
  height,
  dateOnly,
}: {
  data: T[];
  series: LineSeries[];
  description: string;
  yFormat: (v: number) => string;
  marks?: { t: number; label: string }[];
  yDomain?: [number, number];
  height?: number;
  dateOnly?: boolean;
}) {
  const c = useChartColors();
  const timeTick = (v: number) => {
    const d = new Date(v);
    return dateOnly ? `${d.getMonth() + 1}/${d.getDate()}` : `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, "0")}h`;
  };
  return (
    <ChartFrame description={description} height={height}>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data} margin={{ top: 12, right: 12, bottom: 0, left: 0 }}>
          <CartesianGrid stroke={c.rule} />
          <XAxis dataKey="t" type="number" scale="time" domain={["dataMin", "dataMax"]} tickFormatter={timeTick} {...axisProps(c)} minTickGap={24} />
          <YAxis {...axisProps(c)} tickFormatter={(v) => yFormat(Number(v))} width={56} domain={yDomain} />
          <Tooltip {...tooltipProps(c)} labelFormatter={(v) => new Date(Number(v)).toLocaleString()} formatter={(v) => yFormat(Number(v))} />
          <Legend wrapperStyle={{ fontSize: 12, color: c.ink }} iconType="plainline" />
          {marks.map((m) => (
            <ReferenceLine key={m.t} x={m.t} stroke={c.ink} strokeDasharray="3 3" label={{ value: m.label, fill: c.ink, fontSize: 10, position: "insideTopRight" }} />
          ))}
          {series.map((s) => (
            <Line
              key={s.key}
              dataKey={s.key}
              name={s.name}
              type="linear"
              stroke={s.color === "yellow" ? c.yellow : s.color === "chart" ? c.chart : c.ink}
              strokeWidth={2}
              strokeDasharray={s.dashed ? "6 3" : undefined}
              dot={false}
              isAnimationActive={false}
              connectNulls
            />
          ))}
        </LineChart>
      </ResponsiveContainer>
    </ChartFrame>
  );
}

/** Two bar series side by side (cost compare). */
export function PairBars({ data, a, b, description }: { data: { label: string; a: number; b: number }[]; a: string; b: string; description: string }) {
  const c = useChartColors();
  return (
    <ChartFrame description={description}>
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
          <CartesianGrid stroke={c.rule} vertical={false} />
          <XAxis dataKey="label" {...axisProps(c)} minTickGap={8} />
          <YAxis {...axisProps(c)} tickFormatter={usdTick} width={52} />
          <Tooltip {...tooltipProps(c)} formatter={(v) => formatUSD(Number(v))} />
          <Legend wrapperStyle={{ fontSize: 12, color: c.ink }} iconType="square" />
          <Bar dataKey="a" name={a} fill={c.ink} isAnimationActive={false} />
          <Bar dataKey="b" name={b} fill={c.chart} isAnimationActive={false} />
        </BarChart>
      </ResponsiveContainer>
    </ChartFrame>
  );
}
