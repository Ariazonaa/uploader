import { useViewStats } from "../lib/api/hooks";
import { Card } from "../components/ui";
import { useT } from "../lib/i18n/context";

function TrafficPanel() {
  const t = useT();
  const { data } = useViewStats();
  if (!data) return null;
  const col = (title: string, rows: { name: string; count: number }[]) => (
    <Card className="flex-1 p-3 text-sm">
      <h3 className="mb-2 font-semibold">{title}</h3>
      {rows.length === 0 ? (
        <p className="text-xs text-[var(--color-muted)]">{t("traffic.noData")}</p>
      ) : (
        <ul className="space-y-1">
          {rows.map((r) => (
            <li key={r.name} className="flex justify-between gap-2">
              <span className="truncate">{r.name}</span>
              <span className="text-[var(--color-muted)]">{r.count}</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
  return (
    <div className="flex flex-col gap-3 sm:flex-row">
      {col(t("traffic.topReferrers"), data.referrers)}
      {col(t("traffic.byClient"), data.agents)}
    </div>
  );
}

export function TrafficPage() {
  const t = useT();

  return (
    <div className="mx-auto max-w-3xl space-y-6 p-6">
      <h1 className="text-lg font-semibold">{t("traffic.title")}</h1>
      <TrafficPanel />
    </div>
  );
}
