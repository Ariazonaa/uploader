import { Check, Eye, Flame, HardDrive, Package, PartyPopper, Rocket, type LucideIcon } from "lucide-react";
import { useActivity, useMe, useStats } from "../lib/api/hooks";
import { Card } from "../components/ui";
import { Heatmap } from "../components/heatmap";
import { formatBytes, formatDate } from "../lib/format";
import { currentStreak, longestStreak, mostActiveDay, parseDayKey, totalInRange } from "../lib/activity";
import type { MessageKey } from "../lib/i18n/types";
import { useI18n } from "../lib/i18n/context";

export function ProfilePage() {
  const { lang, t, tn } = useI18n();
  const { data: me } = useMe();
  const { data: stats } = useStats();
  const { data: activity } = useActivity();

  const days = new Set((activity ?? []).filter((a) => a.count > 0).map((a) => a.day));
  const sortedKeys = [...days].sort();
  const streak = currentStreak(days);
  const longest = longestStreak(sortedKeys);
  const rangeTotal = totalInRange(activity ?? []);
  const busiest = mostActiveDay(activity ?? []);

  const fileCount = stats?.file_count ?? 0;
  const totalBytes = stats?.total_bytes ?? 0;
  const totalViews = stats?.total_views ?? 0;

  const achievements: { labelKey: MessageKey; icon: LucideIcon; unlocked: boolean }[] = [
    { labelKey: "profile.achievement.firstUpload", icon: PartyPopper, unlocked: fileCount >= 1 },
    { labelKey: "profile.achievement.tenUploads", icon: Package, unlocked: fileCount >= 10 },
    { labelKey: "profile.achievement.hundredUploads", icon: Rocket, unlocked: fileCount >= 100 },
    { labelKey: "profile.achievement.gbStored", icon: HardDrive, unlocked: totalBytes >= 1024 ** 3 },
    { labelKey: "profile.achievement.hundredViews", icon: Eye, unlocked: totalViews >= 100 },
    { labelKey: "profile.achievement.weekStreak", icon: Flame, unlocked: longest >= 7 },
  ];

  const wrapped: { labelKey: MessageKey; value: string }[] = [
    { labelKey: "profile.filesUploaded", value: String(fileCount) },
    { labelKey: "profile.totalStored", value: formatBytes(totalBytes) },
    { labelKey: "profile.totalViews", value: String(totalViews) },
    { labelKey: "profile.currentStreak", value: tn("profile.days", streak) },
    { labelKey: "profile.longestStreak", value: tn("profile.days", longest) },
    { labelKey: "profile.memberSince", value: me ? formatDate(me.created_at).split(",")[0] : "—" },
  ];

  return (
    <div className="mx-auto max-w-3xl space-y-6 p-6">
      <div className="flex items-center justify-between gap-3">
        <h1 className="text-lg font-semibold">{t("profile.title")}</h1>
        <div className="flex gap-2 text-sm">
          <a
            href="/api/v1/stats/export?format=csv"
            className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 transition hover:border-[var(--color-border-strong)] hover:bg-white/5"
          >
            {t("profile.exportCsv")}
          </a>
          <a
            href="/api/v1/stats/export?format=json"
            className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 transition hover:border-[var(--color-border-strong)] hover:bg-white/5"
          >
            {t("profile.exportJson")}
          </a>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
        {wrapped.map((w) => (
          <Card key={w.labelKey} className="p-4">
            <div className="text-xs text-[var(--color-muted)]">{t(w.labelKey)}</div>
            <div className="text-xl font-semibold">{w.value}</div>
          </Card>
        ))}
      </div>

      <Card className="p-4">
        <h3 className="mb-3 font-semibold">{t("profile.achievements")}</h3>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
          {achievements.map((a) => {
            const Icon = a.icon;
            return (
              <div
                key={a.labelKey}
                className={`flex items-center gap-2 rounded-lg border p-2 text-sm ${
                  a.unlocked ? "border-[var(--color-accent)]" : "border-[var(--color-border)] opacity-40"
                }`}
              >
                <Icon
                  className={`h-5 w-5 shrink-0 ${a.unlocked ? "text-[var(--color-accent)]" : ""}`}
                  aria-hidden="true"
                />
                <span>{t(a.labelKey)}</span>
                <span className="sr-only">{a.unlocked ? t("profile.unlocked") : t("profile.locked")}</span>
                {a.unlocked && (
                  <Check
                    className="ml-auto h-4 w-4 shrink-0 text-[var(--color-success)]"
                    aria-hidden="true"
                  />
                )}
              </div>
            );
          })}
        </div>
      </Card>

      <Card className="p-4">
        <h3 className="mb-1 font-semibold">{t("profile.uploadActivity")}</h3>
        <p className="mb-3 text-xs text-[var(--color-muted)]">
          {tn("profile.uploadsInYear", rangeTotal)}
          {busiest &&
            ` · ${t("profile.busiestDay", {
              date: parseDayKey(busiest.day).toLocaleDateString(lang, {
                day: "numeric",
                month: "short",
                year: "numeric",
              }),
              count: busiest.count,
            })}`}
        </p>
        <Heatmap activity={activity ?? []} />
      </Card>
    </div>
  );
}
