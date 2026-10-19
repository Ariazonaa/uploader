import { RotateCcw, Trash2 } from "lucide-react";
import { useEmptyTrash, usePurgeFile, useRestoreFile, useTrash } from "../lib/api/hooks";
import { Button, Card, LoadError } from "../components/ui";
import { formatBytes, formatDate } from "../lib/format";
import { fileType } from "../lib/filetype";
import { toast } from "../lib/toast";
import { useT, useTn } from "../lib/i18n/context";

export function TrashPage() {
  const t = useT();
  const tn = useTn();
  const { data, isLoading, isError, refetch, fetchNextPage, hasNextPage, isFetchingNextPage } = useTrash();
  const restore = useRestoreFile();
  const purge = usePurgeFile();
  const emptyTrash = useEmptyTrash();

  const items = data?.pages.flat() ?? [];

  return (
    <div className="mx-auto max-w-5xl space-y-6 p-6">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h1 className="text-lg font-semibold">{t("trash.title")}</h1>
          <p className="text-sm text-[var(--color-muted)]">{t("trash.subtitle")}</p>
        </div>
        {items.length > 0 && (
          <Button
            variant="danger"
            disabled={emptyTrash.isPending}
            onClick={() => {
              // Only the loaded pages are counted, so with more to load the
              // prompt leaves the number out.
              const question = hasNextPage
                ? t("trash.confirmEmptyAll")
                : tn("trash.confirmEmpty", items.length);
              if (!confirm(question)) return;
              emptyTrash.mutate(undefined, {
                onSuccess: () => toast(t("trash.emptied"), "success"),
              });
            }}
          >
            {t("trash.empty")}
          </Button>
        )}
      </div>

      {isLoading ? (
        <p className="text-sm text-[var(--color-muted)]">{t("common.loading")}</p>
      ) : isError && items.length === 0 ? (
        <LoadError onRetry={() => refetch()} />
      ) : items.length === 0 ? (
        <div className="rounded-xl border border-dashed border-[var(--color-border)] p-12 text-center">
          <Trash2 className="mx-auto mb-3 h-12 w-12 text-[var(--color-muted)]" />
          <p className="font-medium">{t("trash.isEmpty")}</p>
          <p className="text-sm text-[var(--color-muted)]">{t("trash.emptyHint")}</p>
        </div>
      ) : (
        <Card className="divide-y divide-[var(--color-border)]">
          {items.map((f) => {
            const ft = fileType(f.mime_type);
            return (
              <div key={f.id} className="flex items-center gap-3 px-3 py-2 text-sm">
                <ft.icon className="h-4 w-4 shrink-0" style={{ color: ft.color }} />
                <span className="flex-1 truncate" title={f.name}>
                  {f.name}
                </span>
                <span className="hidden w-20 shrink-0 text-right text-xs text-[var(--color-muted)] sm:block">
                  {formatBytes(f.size_bytes)}
                </span>
                <span className="hidden w-28 shrink-0 text-right text-xs text-[var(--color-muted)] sm:block">
                  {f.deleted_at ? formatDate(f.deleted_at) : ""}
                </span>
                <button
                  type="button"
                  className="inline-flex items-center gap-1 rounded-lg px-2 py-1 text-[var(--color-muted)] hover:text-[var(--color-text)]"
                  onClick={() => restore.mutate(f.id)}
                  title={t("trash.restore")}
                >
                  <RotateCcw className="h-4 w-4" /> {t("trash.restore")}
                </button>
                <button
                  type="button"
                  className="inline-flex items-center gap-1 rounded-lg px-2 py-1 text-[var(--color-danger)] hover:brightness-125"
                  onClick={() => {
                    if (!confirm(t("trash.confirmPurge", { name: f.name }))) return;
                    purge.mutate(f.id);
                  }}
                  title={t("trash.deletePermanently")}
                >
                  <Trash2 className="h-4 w-4" /> {t("common.delete")}
                </button>
              </div>
            );
          })}
        </Card>
      )}
      {hasNextPage && (
        <div className="flex justify-center">
          <Button variant="ghost" onClick={() => fetchNextPage()} disabled={isFetchingNextPage}>
            {isFetchingNextPage ? t("common.loading") : t("trash.loadMore")}
          </Button>
        </div>
      )}
    </div>
  );
}
