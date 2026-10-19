import { useEffect, useMemo, useRef, useState } from "react";
import { useBulkDeleteFiles, useDeleteFile, useFiles, useRenameFile, useStats } from "../lib/api/hooks";
import { ArrowDown, ArrowUp, LayoutGrid, List as ListIcon, Lock, MoreVertical, Upload } from "lucide-react";
import { Uploader } from "../components/uploader";
import { Lightbox } from "../components/lightbox";
import { Button, Card, Input, LoadError } from "../components/ui";
import { formatBytes, formatDate } from "../lib/format";
import { fileType } from "../lib/filetype";
import { toast } from "../lib/toast";
import { copyToClipboard } from "../lib/clipboard";
import { useT, useTn } from "../lib/i18n/context";
import type { FileItem } from "../lib/api/types";

function StatWidgets() {
  const t = useT();
  const { data } = useStats();
  if (!data) return null;
  const items = [
    { label: t("dashboard.statFiles"), value: String(data.file_count) },
    { label: t("dashboard.statStorage"), value: formatBytes(data.total_bytes) },
    { label: t("dashboard.statViews"), value: String(data.total_views) },
  ];

  return (
    <div className="grid grid-cols-3 gap-3">
      {items.map((i) => (
        <Card key={i.label} className="p-3">
          <div className="text-xs text-[var(--color-muted)]">{i.label}</div>
          <div className="text-lg font-semibold">{i.value}</div>
        </Card>
      ))}
    </div>
  );
}

function SkeletonGrid() {
  return (
    <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4">
      {Array.from({ length: 8 }).map((_, i) => (
        <div key={i} className="panel overflow-hidden rounded-[var(--radius-card)]">
          <div className="skeleton h-40 w-full" />
          <div className="space-y-2 p-3">
            <div className="skeleton h-3 w-3/4 rounded" />
            <div className="skeleton h-3 w-1/2 rounded" />
          </div>
        </div>
      ))}
    </div>
  );
}

interface Menu {
  x: number;
  y: number;
  file: FileItem;
  // The element that opened the menu (an actions button), so focus can be
  // restored to it on close. Null for right-click (mouse) invocations.
  trigger?: HTMLElement | null;
}

function FileCard({
  file,
  selected,
  menuOpen,
  onToggle,
  onOpen,
  onMenu,
}: {
  file: FileItem;
  selected: boolean;
  menuOpen: boolean;
  onToggle: (id: string) => void;
  onOpen: (f: FileItem) => void;
  onMenu: (m: Menu) => void;
}) {
  const t = useT();
  const tn = useTn();
  const previewable = file.mime_type.startsWith("image/") || file.mime_type.startsWith("video/");
  const ft = fileType(file.mime_type);

  return (
    <div
      className={`panel card-lift relative overflow-hidden rounded-[var(--radius-card)] ${
        selected ? "ring-2 ring-[var(--color-accent)]" : ""
      }`}
      onContextMenu={(e: React.MouseEvent) => {
        e.preventDefault();
        onMenu({ x: e.clientX, y: e.clientY, file, trigger: null });
      }}
    >
      <input
        type="checkbox"
        checked={selected}
        onChange={() => onToggle(file.id)}
        className="absolute left-2 top-2 z-10 h-4 w-4 accent-[var(--color-accent)]"
        aria-label={t("dashboard.selectFile", { name: file.name })}
      />
      {file.encrypted && (
        <div
          role="img"
          aria-label={t("dashboard.encryptedAtRest")}
          className="panel absolute right-2 top-2 z-10 flex h-6 w-6 items-center justify-center rounded-full text-[var(--color-muted)]"
        >
          <Lock className="h-3.5 w-3.5" aria-hidden="true" />
        </div>
      )}
      <button
        type="button"
        onClick={() => (previewable ? onOpen(file) : window.open(file.url, "_blank"))}
        className="block w-full bg-black/30"
      >
        {/* Never the original via /raw as a stand-in: that downloads full-size
            files into the grid and counts as views. Until the background job
            has made a thumbnail (or for files that never get one, e.g.
            encrypted ones) the card shows the file-type icon. */}
        {file.thumb ? (
          <img src={file.thumb} alt={file.name} className="max-h-80 w-full object-cover" loading="lazy" />
        ) : (
          <div className="flex h-40 flex-col items-center justify-center gap-2" style={{ color: ft.color }}>
            <ft.icon className="h-10 w-10" />
            <span className="text-xs">{t(ft.labelKey)}</span>
          </div>
        )}
      </button>
      <div className="space-y-2 p-3 text-sm">
        <div className="flex items-start justify-between gap-2">
          <div className="truncate font-medium" title={file.name}>
            {file.name}
          </div>
          <button
            type="button"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            aria-label={t("dashboard.actionsFor", { name: file.name })}
            onClick={(e) => {
              e.stopPropagation();
              const r = e.currentTarget.getBoundingClientRect();
              onMenu({ x: r.left, y: r.bottom, file, trigger: e.currentTarget });
            }}
            className="-m-1 shrink-0 rounded-full p-1 text-[var(--color-muted)] transition hover:text-[var(--color-text)]"
          >
            <MoreVertical className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
        <div className="flex justify-between text-xs text-[var(--color-muted)]">
          <span>{formatBytes(file.size_bytes)}</span>
          <span>{tn("dashboard.views", file.views)}</span>
        </div>
        <div className="text-xs text-[var(--color-muted)]">{formatDate(file.created_at)}</div>
      </div>
    </div>
  );
}

function FileRow({
  file,
  selected,
  menuOpen,
  onToggle,
  onOpen,
  onMenu,
}: {
  file: FileItem;
  selected: boolean;
  menuOpen: boolean;
  onToggle: (id: string) => void;
  onOpen: (f: FileItem) => void;
  onMenu: (m: Menu) => void;
}) {
  const t = useT();
  const tn = useTn();
  const ft = fileType(file.mime_type);
  const previewable = file.mime_type.startsWith("image/") || file.mime_type.startsWith("video/");
  return (
    <div
      className="flex items-center gap-3 rounded-xl px-3 py-2 text-sm hover:bg-white/5"
      onContextMenu={(e) => {
        e.preventDefault();
        onMenu({ x: e.clientX, y: e.clientY, file, trigger: null });
      }}
    >
      <input
        type="checkbox"
        checked={selected}
        onChange={() => onToggle(file.id)}
        className="h-4 w-4 accent-[var(--color-accent)]"
        aria-label={t("dashboard.selectFile", { name: file.name })}
      />
      <ft.icon className="h-4 w-4 shrink-0" style={{ color: ft.color }} />
      <button
        type="button"
        className="flex-1 truncate text-left hover:underline"
        title={file.name}
        onClick={() => (previewable ? onOpen(file) : window.open(file.url, "_blank"))}
      >
        {file.name}
      </button>
      <span className="w-20 shrink-0 text-right text-xs text-[var(--color-muted)]">
        {formatBytes(file.size_bytes)}
      </span>
      <span className="w-16 shrink-0 text-right text-xs text-[var(--color-muted)]">
        {tn("dashboard.views", file.views)}
      </span>
      <span className="hidden w-28 shrink-0 text-right text-xs text-[var(--color-muted)] sm:block">
        {formatDate(file.created_at)}
      </span>
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        aria-label={t("dashboard.actionsFor", { name: file.name })}
        onClick={(e) => {
          e.stopPropagation();
          const r = e.currentTarget.getBoundingClientRect();
          onMenu({ x: r.left, y: r.bottom, file, trigger: e.currentTarget });
        }}
        className="shrink-0 rounded-full p-1 text-[var(--color-muted)] transition hover:text-[var(--color-text)]"
      >
        <MoreVertical className="h-4 w-4" aria-hidden="true" />
      </button>
    </div>
  );
}

export function DashboardPage() {
  const t = useT();
  const tn = useTn();
  const [search, setSearch] = useState("");
  const [debouncedSearch, setDebouncedSearch] = useState("");
  const [type, setType] = useState("");
  const [sort, setSort] = useState<"created" | "size" | "views" | "name">("created");
  const [order, setOrder] = useState<"asc" | "desc">("desc");
  const [view, setView] = useState<"grid" | "list">(
    () => (localStorage.getItem("fileView") as "grid" | "list") || "grid",
  );

  // Debounce the search box so typing doesn't refetch on every keystroke.
  useEffect(() => {
    const t = setTimeout(() => setDebouncedSearch(search), 300);
    return () => clearTimeout(t);
  }, [search]);

  const { data, isLoading, isError, refetch, fetchNextPage, hasNextPage, isFetchingNextPage } = useFiles({
    q: debouncedSearch,
    type,
    sort,
    order,
  });
  const del = useDeleteFile();
  const rename = useRenameFile();
  const bulkDelete = useBulkDeleteFiles();
  const [active, setActive] = useState<FileItem | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [menu, setMenu] = useState<Menu | null>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  const setViewMode = (v: "grid" | "list") => {
    setView(v);
    localStorage.setItem("fileView", v);
  };

  // Pages are fetched by offset, so a file uploaded (or trashed) between two
  // "load more" clicks shifts the window and the next page can repeat a row.
  const files = useMemo(() => {
    const seen = new Set<string>();
    return (data?.pages.flat() ?? []).filter((f) => {
      if (seen.has(f.id)) return false;
      seen.add(f.id);
      return true;
    });
  }, [data]);
  // Only files still in the list count as selected: one trashed on its own or
  // dropped by a refetch must not inflate the counter or be trashed unseen.
  const selectedIds = files.filter((f) => selected.has(f.id)).map((f) => f.id);

  // A new search, filter or sort starts over at the first page, so a selection
  // made in the old view would point at files the user can no longer see.
  useEffect(() => setSelected(new Set()), [debouncedSearch, type, sort, order]);

  // Close the context menu on any outside click.
  useEffect(() => {
    if (!menu) return;
    const close = () => setMenu(null);
    window.addEventListener("click", close);
    return () => window.removeEventListener("click", close);
  }, [menu]);

  // Move focus to the first menu item when the menu opens, so keyboard users can
  // drive it immediately.
  useEffect(() => {
    if (!menu) return;
    const first = menuRef.current?.querySelector<HTMLElement>('[role="menuitem"]');
    first?.focus();
  }, [menu]);

  // Roving keyboard navigation within the open menu.
  const onMenuKey = (e: React.KeyboardEvent) => {
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
    if (items.length === 0) return;
    const idx = items.indexOf(document.activeElement as HTMLElement);
    if (e.key === "ArrowDown") {
      e.preventDefault();
      items[(idx + 1) % items.length].focus();
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      items[(idx - 1 + items.length) % items.length].focus();
    } else if (e.key === "Home") {
      e.preventDefault();
      items[0].focus();
    } else if (e.key === "End") {
      e.preventDefault();
      items[items.length - 1].focus();
    } else if (e.key === "Escape") {
      e.preventDefault();
      const trigger = menu?.trigger;
      setMenu(null);
      trigger?.focus();
    }
  };

  const toggle = (id: string) =>
    setSelected((s) => {
      const next = new Set(s);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });

  const doRename = (f: FileItem) => {
    const name = window.prompt(t("dashboard.renamePrompt"), f.name);
    if (name && name.trim() && name !== f.name) {
      rename.mutate({ id: f.id, name: name.trim() });
    }
  };

  const deleteOne = (id: string) => {
    del.mutate(id);
    setSelected((s) => {
      const next = new Set(s);
      next.delete(id);
      return next;
    });
  };

  const deleteSelected = () => {
    if (selectedIds.length === 0) return;
    if (!confirm(tn("dashboard.confirmBulkTrash", selectedIds.length))) return;
    bulkDelete.mutate(selectedIds, {
      onSuccess: (res) => {
        // The server skips files that are already gone, so its count is the true one.
        const all = res.deleted === selectedIds.length;
        toast(tn("dashboard.movedToTrash", res.deleted), all ? "success" : "info");
        setSelected(new Set());
      },
    });
  };

  return (
    <div className="mx-auto max-w-5xl space-y-8 p-6">
      <Uploader />
      <StatWidgets />

      <section>
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <h1 className="text-lg font-semibold">{t("dashboard.yourFiles")}</h1>
          {selectedIds.length > 0 && (
            <div className="flex items-center gap-2 text-sm">
              <span className="text-[var(--color-muted)]">
                {tn("dashboard.selected", selectedIds.length)}
              </span>
              <Button variant="ghost" onClick={() => setSelected(new Set())}>
                {t("dashboard.clear")}
              </Button>
              <Button variant="danger" onClick={deleteSelected} disabled={bulkDelete.isPending}>
                {t("dashboard.deleteSelected")}
              </Button>
            </div>
          )}
        </div>

        <div className="mb-4 flex flex-wrap items-center gap-2 text-sm">
          <Input
            placeholder={t("dashboard.searchPlaceholder")}
            aria-label={t("dashboard.searchLabel")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="max-w-xs"
          />
          <select
            value={type}
            onChange={(e) => setType(e.target.value)}
            aria-label={t("dashboard.filterByType")}
            className="control rounded-full px-3 py-2 text-[var(--color-text)]"
          >
            <option value="">{t("dashboard.typeAll")}</option>
            <option value="image/">{t("dashboard.typeImages")}</option>
            <option value="video/">{t("dashboard.typeVideos")}</option>
            <option value="audio/">{t("dashboard.typeAudio")}</option>
            <option value="application/">{t("dashboard.typeDocuments")}</option>
            <option value="text/">{t("dashboard.typeText")}</option>
          </select>
          <select
            value={sort}
            onChange={(e) => setSort(e.target.value as typeof sort)}
            aria-label={t("dashboard.sortBy")}
            className="control rounded-full px-3 py-2 text-[var(--color-text)]"
          >
            <option value="created">{t("dashboard.sortDate")}</option>
            <option value="size">{t("dashboard.sortSize")}</option>
            <option value="views">{t("dashboard.sortViews")}</option>
            <option value="name">{t("dashboard.sortName")}</option>
          </select>
          <Button variant="ghost" onClick={() => setOrder(order === "desc" ? "asc" : "desc")}>
            <span className="flex items-center gap-1">
              {order === "desc" ? <ArrowDown className="h-4 w-4" /> : <ArrowUp className="h-4 w-4" />}
              {order === "desc" ? t("dashboard.desc") : t("dashboard.asc")}
            </span>
          </Button>
          <div className="control ml-auto flex gap-0.5 rounded-full p-0.5">
            {(
              [
                { mode: "grid", Icon: LayoutGrid, label: t("dashboard.gridView") },
                { mode: "list", Icon: ListIcon, label: t("dashboard.listView") },
              ] as const
            ).map(({ mode, Icon, label }) => (
              <button
                key={mode}
                type="button"
                onClick={() => setViewMode(mode)}
                aria-pressed={view === mode}
                title={label}
                className={`rounded-full px-3 py-1.5 transition ${
                  view === mode
                    ? "bg-white/10 text-[var(--color-text)]"
                    : "text-[var(--color-muted)] hover:text-[var(--color-text)]"
                }`}
              >
                <Icon className="h-4 w-4" />
              </button>
            ))}
          </div>
        </div>

        {isLoading ? (
          <SkeletonGrid />
        ) : isError && files.length === 0 ? (
          <LoadError onRetry={() => refetch()} />
        ) : files.length === 0 ? (
          <div className="panel rounded-[var(--radius-card)] border-dashed border-[color:var(--color-border-strong)] p-12 text-center">
            <Upload className="mx-auto mb-3 h-12 w-12 text-[var(--color-muted)]" />
            <p className="font-medium">
              {debouncedSearch || type ? t("dashboard.noMatching") : t("dashboard.noFiles")}
            </p>
            <p className="text-sm text-[var(--color-muted)]">
              {debouncedSearch || type ? t("dashboard.noMatchingHint") : t("dashboard.noFilesHint")}
            </p>
          </div>
        ) : (
          <>
            {view === "grid" ? (
              <div className="masonry">
                {files.map((f) => (
                  <FileCard
                    key={f.id}
                    file={f}
                    selected={selected.has(f.id)}
                    menuOpen={menu?.file.id === f.id}
                    onToggle={toggle}
                    onOpen={setActive}
                    onMenu={setMenu}
                  />
                ))}
              </div>
            ) : (
              <div className="panel divide-y divide-[color:var(--color-border)] overflow-hidden rounded-[var(--radius-card)] p-1">
                {files.map((f) => (
                  <FileRow
                    key={f.id}
                    file={f}
                    selected={selected.has(f.id)}
                    menuOpen={menu?.file.id === f.id}
                    onToggle={toggle}
                    onOpen={setActive}
                    onMenu={setMenu}
                  />
                ))}
              </div>
            )}
            {hasNextPage && (
              <div className="mt-6 flex justify-center">
                <Button variant="ghost" onClick={() => fetchNextPage()} disabled={isFetchingNextPage}>
                  {isFetchingNextPage ? t("common.loading") : t("dashboard.loadMore")}
                </Button>
              </div>
            )}
          </>
        )}
      </section>

      <Lightbox file={active} onClose={() => setActive(null)} />

      {menu && (
        <div
          ref={menuRef}
          role="menu"
          aria-label={t("dashboard.actionsFor", { name: menu.file.name })}
          aria-orientation="vertical"
          className="panel fixed z-50 min-w-44 overflow-hidden rounded-2xl py-1.5 text-sm"
          style={{ top: menu.y, left: menu.x }}
          onClick={(e) => e.stopPropagation()}
          onKeyDown={onMenuKey}
        >
          {[
            { label: t("dashboard.menuOpen"), fn: () => window.open(menu.file.url, "_blank") },
            {
              label: t("common.copyLink"),
              fn: async () => {
                const ok = await copyToClipboard(menu.file.url);
                toast(ok ? t("common.linkCopied") : t("common.copyFailed"), ok ? "success" : "error");
              },
            },
            { label: t("dashboard.menuRename"), fn: () => doRename(menu.file) },
            { label: t("dashboard.menuTrash"), fn: () => deleteOne(menu.file.id), danger: true },
          ].map((item) => (
            <button
              key={item.label}
              type="button"
              role="menuitem"
              tabIndex={-1}
              className={`block w-full rounded-xl px-4 py-2 text-left hover:bg-white/10 focus:bg-white/10 focus:outline-none ${
                item.danger ? "text-[var(--color-danger)]" : ""
              }`}
              onClick={() => {
                item.fn();
                setMenu(null);
              }}
            >
              {item.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
