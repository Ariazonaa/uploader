import { useLayoutEffect, useRef } from "react";
import type { DayCount } from "../lib/api/types";
import { bucketLevel, dayKey, HEATMAP_WEEKS } from "../lib/activity";
import { useI18n } from "../lib/i18n/context";

// 5-step intensity scale derived from the Liquid Glass accent token so the
// heatmap matches the rest of the UI (L0 is a faint text tint = "visible but
// empty"; L1..L4 ramp the accent from translucent to solid).
const BUCKET_BG = [
  "color-mix(in oklab, var(--color-text) 7%, transparent)",
  "color-mix(in oklab, var(--color-accent) 30%, transparent)",
  "color-mix(in oklab, var(--color-accent) 50%, transparent)",
  "color-mix(in oklab, var(--color-accent) 72%, transparent)",
  "var(--color-accent)",
] as const;

type Cell = { key: string; date: Date; count: number };

// Heatmap is a purely presentational GitHub-style contribution calendar. It
// renders `weeks` Monday-aligned columns (more than HEATMAP_WEEKS would reach
// past the days useActivity fetches) of 7 day cells ending at `endDate`
// (default today), colouring each cell by its bucket level.
export function Heatmap({
  activity,
  weeks = HEATMAP_WEEKS,
  endDate,
}: {
  activity: DayCount[];
  weeks?: number;
  endDate?: Date;
}) {
  const { lang, t, tn } = useI18n();
  // The grid is wider than narrow cards; start scrolled to the newest week
  // (today), not the oldest, which is where the activity a user cares about is.
  const scroller = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el) el.scrollLeft = el.scrollWidth;
  }, [weeks]);
  // Date formatters follow the active UI language (was hardcoded en-GB).
  const humanDate = new Intl.DateTimeFormat(lang, { day: "numeric", month: "short", year: "numeric" });
  const monthShort = new Intl.DateTimeFormat(lang, { month: "short" });

  const counts = new Map(activity.map((a) => [a.day, a.count]));

  const end = endDate ? new Date(endDate) : new Date();
  end.setHours(0, 0, 0, 0);

  // Start `weeks*7 - 1` days before the end, then snap back to the Monday of
  // that week so every column is a clean Mon→Sun run.
  const start = new Date(end);
  start.setDate(start.getDate() - (weeks * 7 - 1));
  const dow = (start.getDay() + 6) % 7; // Mon = 0 … Sun = 6
  start.setDate(start.getDate() - dow);

  // Build columns of up to 7 cells. The final (current) column may be partial.
  const columns: Cell[][] = [];
  const cursor = new Date(start);
  let total = 0;
  while (cursor <= end) {
    const col: Cell[] = [];
    for (let r = 0; r < 7 && cursor <= end; r++) {
      const key = dayKey(cursor);
      const count = counts.get(key) ?? 0;
      total += count;
      col.push({ key, date: new Date(cursor), count });
      cursor.setDate(cursor.getDate() + 1);
    }
    columns.push(col);
  }

  // A column carries a month label when its top cell's month differs from the
  // previous column's top cell (and always for the first column).
  const monthLabels = columns.map((col, i) => {
    const top = col[0]?.date;
    if (!top) return "";
    const prev = columns[i - 1]?.[0]?.date;
    if (!prev || prev.getMonth() !== top.getMonth() || prev.getFullYear() !== top.getFullYear()) {
      return monthShort.format(top);
    }
    return "";
  });

  const summary = t("heatmap.summary", {
    uploads: tn("heatmap.uploads", total),
    weeks: columns.length,
  });

  const weekdayLabels = [t("heatmap.mon"), "", t("heatmap.wed"), "", t("heatmap.fri"), "", ""];

  return (
    <div ref={scroller} className="overflow-x-auto">
      <div className="inline-flex min-w-max flex-col gap-1">
        {/* Month labels aligned to their columns (left gutter matches the grid). */}
        <div className="flex gap-1" aria-hidden="true">
          <div className="w-9 shrink-0" />
          <div className="grid grid-flow-col grid-rows-1 gap-1">
            {monthLabels.map((label, i) => (
              <div
                key={i}
                className="h-3 w-3 text-[10px] leading-3 whitespace-nowrap text-[var(--color-muted)]"
              >
                {label}
              </div>
            ))}
          </div>
        </div>

        <div className="flex gap-1">
          {/* Weekday gutter: label Mon/Wed/Fri (rows 0/2/4, Monday-aligned). */}
          <div className="grid w-9 shrink-0 grid-rows-7 gap-1" aria-hidden="true">
            {weekdayLabels.map((d, i) => (
              <div key={i} className="h-3 text-[10px] leading-3 text-[var(--color-muted)]">
                {d}
              </div>
            ))}
          </div>

          {/* The calendar itself: one <img>-role region summarised for a11y. */}
          <div className="grid grid-flow-col grid-rows-7 gap-1" role="img" aria-label={summary}>
            {columns.flatMap((col) =>
              col.map((cell) => {
                const label = `${humanDate.format(cell.date)} · ${tn("heatmap.uploads", cell.count)}`;
                return (
                  <div
                    key={cell.key}
                    title={label}
                    aria-label={label}
                    tabIndex={-1}
                    className="h-3 w-3 rounded-[4px]"
                    style={{ backgroundColor: BUCKET_BG[bucketLevel(cell.count)] }}
                  />
                );
              }),
            )}
          </div>
        </div>

        {/* Legend: Less → More across the 5 buckets. */}
        <div className="flex items-center gap-1.5 pl-10 text-[10px] text-[var(--color-muted)]">
          <span>{t("heatmap.less")}</span>
          {BUCKET_BG.map((bg, i) => (
            <span
              key={i}
              className="h-3 w-3 rounded-[4px]"
              style={{ backgroundColor: bg }}
              aria-hidden="true"
            />
          ))}
          <span>{t("heatmap.more")}</span>
        </div>
      </div>
    </div>
  );
}
