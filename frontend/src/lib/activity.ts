import type { DayCount } from "./api/types";

// --- heatmap window -------------------------------------------------------
// The heatmap shows HEATMAP_WEEKS Monday-aligned columns ending today, so its
// first cell lies up to HEATMAP_WEEKS*7 - 1 days back plus up to 6 more to
// reach that week's Monday. ACTIVITY_DAYS covers all of it (+1 because the
// server's window starts at "now minus N days", mid-day). The server caps
// `days` at 372, which is why the grid is 52 weeks and not 53 (that would need
// 377 days).
export const HEATMAP_WEEKS = 52;
export const ACTIVITY_DAYS = HEATMAP_WEEKS * 7 + 6 + 1;

// --- day keys -------------------------------------------------------------
// A day key is a "YYYY-MM-DD" calendar date in the browser's local time zone.
// The heatmap grid and the streak walk iterate local midnights, so the key must
// come from the local date parts: toISOString() would convert to UTC first and,
// east of Greenwich, label local midnight with the previous day (today's uploads
// would land on yesterday's cell). The server buckets /stats/activity by the
// same zone — useActivity sends it as `tz` — so both sides agree on "a day".

export function dayKey(d: Date): string {
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${d.getFullYear()}-${m}-${day}`;
}

// parseDayKey turns a day key back into local midnight of that date.
// (new Date("YYYY-MM-DD") would parse it as UTC midnight instead, which renders
// as the previous day west of Greenwich.)
export function parseDayKey(key: string): Date {
  const [y, m, d] = key.split("-").map(Number);
  return new Date(y, m - 1, d);
}

// localTimeZone is the browser's IANA zone name (e.g. "Europe/Berlin"), or ""
// when the runtime cannot tell — the server then falls back to UTC.
export function localTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone ?? "";
  } catch {
    return "";
  }
}

// --- streak helpers -------------------------------------------------------

export function currentStreak(days: Set<string>): number {
  let streak = 0;
  const cur = new Date();
  // Allow the streak to count from yesterday if nothing was uploaded today yet.
  if (!days.has(dayKey(cur))) cur.setDate(cur.getDate() - 1);
  while (days.has(dayKey(cur))) {
    streak++;
    cur.setDate(cur.getDate() - 1);
  }
  return streak;
}

export function longestStreak(sortedKeys: string[]): number {
  let best = 0;
  let run = 0;
  let prev: Date | null = null;
  for (const k of sortedKeys) {
    const d = parseDayKey(k);
    if (prev) {
      const diff = Math.round((d.getTime() - prev.getTime()) / 86400000);
      run = diff === 1 ? run + 1 : 1;
    } else {
      run = 1;
    }
    best = Math.max(best, run);
    prev = d;
  }
  return best;
}

// --- aggregates -----------------------------------------------------------

// mostActiveDay returns the day with the highest upload count (first one wins on
// a tie), or null when there is no activity at all.
export function mostActiveDay(activity: DayCount[]): { day: string; count: number } | null {
  let best: { day: string; count: number } | null = null;
  for (const a of activity) {
    if (a.count > 0 && (best === null || a.count > best.count)) {
      best = { day: a.day, count: a.count };
    }
  }
  return best;
}

// totalInRange sums every day's upload count in the activity series.
export function totalInRange(activity: DayCount[]): number {
  let total = 0;
  for (const a of activity) total += a.count;
  return total;
}

// bucketLevel maps a day's upload count onto the 5-step heatmap intensity scale.
// Thresholds: 0 → 0, 1–2 → 1, 3–5 → 2, 6–9 → 3, 10+ → 4.
export function bucketLevel(count: number): 0 | 1 | 2 | 3 | 4 {
  if (count <= 0) return 0;
  if (count <= 2) return 1;
  if (count <= 5) return 2;
  if (count <= 9) return 3;
  return 4;
}
