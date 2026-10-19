import { useState } from "react";
import { ScrollText } from "lucide-react";
import { useAuditLog } from "../lib/api/hooks";
import type { AuditEvent } from "../lib/api/types";
import { Button, Card, LoadError } from "../components/ui";
import { formatDate } from "../lib/format";
import { useT } from "../lib/i18n/context";

const PAGE_SIZE = 50;

// The known event names, mirrored from the backend audit constants. Used to
// populate the filter dropdown.
const EVENTS = [
  "login.success",
  "login.failure",
  "logout",
  "token.create",
  "token.revoke",
  "file.delete",
  "file.purge",
  "file.restore",
  "settings.update",
  "invite.create",
  "invite.revoke",
  "user.register",
  "password.change",
  "totp.enable",
  "totp.disable",
  "session.revoke",
  "account.delete",
  "account.export",
  "file.delete_by_key",
];

// eventColor tints an event label: green for successful/creating actions, red
// for failures/deletions/purges, muted for everything else (e.g. logout).
function eventColor(event: string): string {
  if (event.endsWith(".failure") || event.endsWith(".delete") || event.endsWith(".purge")) {
    return "text-[var(--color-danger)]";
  }
  if (event.endsWith(".success") || event.endsWith(".create")) {
    return "text-[var(--color-success)]";
  }
  return "text-[var(--color-muted)]";
}

function AuditRow({ e }: { e: AuditEvent }) {
  return (
    <tr className="border-b border-[color:var(--color-border)] last:border-0">
      <td className="whitespace-nowrap px-4 py-3 text-[var(--color-muted)]">{formatDate(e.created_at)}</td>
      <td className="px-4 py-3">
        {e.actor_label ? e.actor_label : <span className="text-[var(--color-muted)]">—</span>}
      </td>
      <td className={`whitespace-nowrap px-4 py-3 font-medium ${eventColor(e.event)}`}>{e.event}</td>
      <td className="max-w-[16rem] truncate px-4 py-3" title={e.target}>
        {e.target ? e.target : <span className="text-[var(--color-muted)]">—</span>}
      </td>
      <td className="whitespace-nowrap px-4 py-3 text-[var(--color-muted)]">{e.ip}</td>
    </tr>
  );
}

export function AuditPage() {
  const t = useT();
  const [event, setEvent] = useState("");
  const [page, setPage] = useState(0);
  const { data, isFetching, isError, refetch } = useAuditLog({ event: event || undefined, page });

  const events = data?.events ?? [];
  const total = data?.total ?? 0;
  const maxPage = Math.max(0, Math.ceil(total / PAGE_SIZE) - 1);
  const from = total === 0 ? 0 : page * PAGE_SIZE + 1;
  const to = Math.min((page + 1) * PAGE_SIZE, total);

  return (
    <div className="mx-auto max-w-5xl space-y-6 p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="flex items-center gap-2 text-lg font-semibold">
          <ScrollText className="h-5 w-5 text-[var(--color-accent)]" aria-hidden="true" />
          {t("audit.title")}
        </h1>
        <select
          value={event}
          onChange={(e) => {
            setEvent(e.target.value);
            setPage(0);
          }}
          aria-label={t("audit.filterLabel")}
          className="control rounded-full px-4 py-2 text-sm text-[var(--color-text)] outline-none focus:shadow-[0_0_0_3px_var(--color-accent-soft)]"
        >
          <option value="">{t("audit.allEvents")}</option>
          {EVENTS.map((ev) => (
            <option key={ev} value={ev}>
              {ev}
            </option>
          ))}
        </select>
      </div>

      {isError && !isFetching ? (
        <LoadError onRetry={() => refetch()} />
      ) : (
        <Card className="overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <caption className="sr-only">{t("audit.title")}</caption>
              <thead className="text-xs uppercase tracking-wide text-[var(--color-muted)]">
                <tr className="border-b border-[color:var(--color-border)]">
                  <th scope="col" className="px-4 py-3 font-medium">
                    {t("audit.time")}
                  </th>
                  <th scope="col" className="px-4 py-3 font-medium">
                    {t("audit.actor")}
                  </th>
                  <th scope="col" className="px-4 py-3 font-medium">
                    {t("audit.event")}
                  </th>
                  <th scope="col" className="px-4 py-3 font-medium">
                    {t("audit.target")}
                  </th>
                  <th scope="col" className="px-4 py-3 font-medium">
                    {t("audit.ip")}
                  </th>
                </tr>
              </thead>
              <tbody>
                {events.map((e) => (
                  <AuditRow key={e.id} e={e} />
                ))}
              </tbody>
            </table>
          </div>
          {events.length === 0 && (
            <p className="px-4 py-6 text-sm text-[var(--color-muted)]">
              {isFetching ? t("common.loading") : t("audit.noEvents")}
            </p>
          )}
        </Card>
      )}

      <div className="flex items-center justify-between text-sm text-[var(--color-muted)]">
        <span>{total === 0 ? t("audit.noEventsShort") : t("audit.range", { from, to, total })}</span>
        <div className="flex gap-2">
          <Button variant="ghost" disabled={page <= 0} onClick={() => setPage((p) => Math.max(0, p - 1))}>
            {t("audit.prev")}
          </Button>
          <Button variant="ghost" disabled={page >= maxPage} onClick={() => setPage((p) => p + 1)}>
            {t("audit.next")}
          </Button>
        </div>
      </div>
    </div>
  );
}
