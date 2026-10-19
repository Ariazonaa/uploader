import { useCallback, useEffect, useRef, useState } from "react";
import { useDropzone } from "react-dropzone";
import { useQueryClient } from "@tanstack/react-query";
import { Check, Copy, FolderUp, Lock, X } from "lucide-react";
import { useUploadDefaults } from "../lib/api/hooks";
import { copyToClipboard } from "../lib/clipboard";
import { formatBytes } from "../lib/format";
import { abortError, backoffMs, createLimiter, isAbortError, retryAfterMs, sleep } from "../lib/retry";
import { toast } from "../lib/toast";
import { tusUpload } from "../lib/tus";
import { uuid } from "../lib/uuid";
import { useT } from "../lib/i18n/context";
import { tGlobal, translateApiError } from "../lib/i18n";

// Files at/above this size use resumable tus uploads; smaller ones use the
// simpler single-request XHR path (which already reports speed/ETA).
const TUS_THRESHOLD = 10 * 1024 * 1024;

// At most this many uploads run at once; the rest of a big (folder) drop waits
// its turn. Together with the 429 backoff below this keeps a large drop inside
// the server's per-IP/per-account upload rate limits instead of failing
// everything after the initial burst.
const runUpload = createLimiter(3);

// How often a direct upload rejected with 429 is retried before giving up.
const MAX_RATE_LIMIT_RETRIES = 6;

// Thumbnails, dimensions and the like are made by background jobs after the
// upload has returned, so the file list is refetched again a little later for
// the new cards to pick them up.
const POST_PROCESS_REFETCH_MS = [3_000, 10_000];

interface Progress {
  pct: number;
  speed: number; // bytes/sec
  eta: number; // seconds remaining
}

interface UploadState {
  id: string;
  name: string;
  progress: number;
  speed?: number;
  eta?: number;
  error?: string;
  url?: string;
  // Whether the share URL made it onto the clipboard; when not (no clipboard on
  // plain HTTP, permission denied) the row shows the URL with a copy button.
  copied?: boolean;
}

// UploadHttpError is a failed direct upload, carrying what a retry needs.
class UploadHttpError extends Error {
  status: number;
  retryAfter: number | null;
  constructor(message: string, status: number, retryAfter: number | null) {
    super(message);
    this.status = status;
    this.retryAfter = retryAfter;
  }
}

// uploadFile posts a single file via XHR so we can report progress, throughput
// and ETA. An optional custom slug requests a specific share link; encrypt
// (when known) is sent explicitly either way, so turning the toggle off also
// overrides a server default of "encrypt".
function uploadFile(
  file: File,
  onProgress: (p: Progress) => void,
  slug?: string,
  encrypt?: boolean,
  signal?: AbortSignal,
): Promise<{ url: string }> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(abortError());
    const form = new FormData();
    form.append("file", file);
    const xhr = new XMLHttpRequest();
    const params = new URLSearchParams();
    if (slug) params.set("slug", slug);
    if (encrypt !== undefined) params.set("encrypt", String(encrypt));
    const qs = params.toString();
    const url = qs ? `/api/v1/upload?${qs}` : "/api/v1/upload";
    xhr.open("POST", url);
    xhr.withCredentials = true;
    const start = Date.now();
    xhr.upload.onprogress = (e) => {
      if (!e.lengthComputable) return;
      const elapsed = (Date.now() - start) / 1000;
      const speed = elapsed > 0 ? e.loaded / elapsed : 0;
      const eta = speed > 0 ? (e.total - e.loaded) / speed : 0;
      onProgress({ pct: Math.round((e.loaded / e.total) * 100), speed, eta });
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          resolve(JSON.parse(xhr.responseText));
        } catch {
          reject(new Error(tGlobal("uploader.invalidResponse")));
        }
      } else {
        let msg = tGlobal("uploader.uploadFailed", { status: xhr.status });
        try {
          const parsed = JSON.parse(xhr.responseText);
          if (parsed?.error) msg = translateApiError(String(parsed.error));
        } catch {
          /* keep default */
        }
        reject(new UploadHttpError(msg, xhr.status, retryAfterMs(xhr.getResponseHeader("Retry-After"))));
      }
    };
    xhr.onerror = () => reject(new Error(tGlobal("uploader.networkError")));
    xhr.onabort = () => reject(abortError());
    signal?.addEventListener("abort", () => xhr.abort(), { once: true });
    xhr.send(form);
  });
}

// uploadFileWithRetry retries a direct upload the server turned away with 429
// (rate limited before the body was processed, so a retry cannot duplicate
// the file), backing off and honoring Retry-After. Other failures are final: a
// direct upload is not idempotent.
async function uploadFileWithRetry(
  file: File,
  onProgress: (p: Progress) => void,
  slug?: string,
  encrypt?: boolean,
  signal?: AbortSignal,
): Promise<{ url: string }> {
  for (let attempt = 0; ; attempt++) {
    try {
      return await uploadFile(file, onProgress, slug, encrypt, signal);
    } catch (e) {
      if (!(e instanceof UploadHttpError) || e.status !== 429 || attempt >= MAX_RATE_LIMIT_RETRIES) throw e;
      onProgress({ pct: 0, speed: 0, eta: 0 });
      await sleep(backoffMs(attempt, e.retryAfter), signal);
    }
  }
}

// readEntry recursively collects File objects from a drag-and-drop filesystem
// entry (a file, or a directory whose contents are walked depth-first).
function readEntry(entry: FileSystemEntry): Promise<File[]> {
  if (entry.isFile) {
    return new Promise((resolve) => {
      (entry as FileSystemFileEntry).file(
        (f) => resolve([f]),
        () => resolve([]),
      );
    });
  }
  if (entry.isDirectory) {
    const reader = (entry as FileSystemDirectoryEntry).createReader();
    return new Promise((resolve) => {
      const all: File[] = [];
      // readEntries returns at most ~100 entries per call, so loop until empty.
      const readBatch = () => {
        reader.readEntries(
          async (entries) => {
            if (entries.length === 0) {
              resolve(all);
              return;
            }
            for (const e of entries) all.push(...(await readEntry(e)));
            readBatch();
          },
          () => resolve(all),
        );
      };
      readBatch();
    });
  }
  return Promise.resolve([]);
}

// filesFromDataTransfer expands a drop into a flat File list, recursing into
// dropped folders when the browser exposes the entries API (webkitGetAsEntry).
async function filesFromDataTransfer(dt: DataTransfer): Promise<File[]> {
  const items = dt.items;
  if (items && items.length && typeof items[0].webkitGetAsEntry === "function") {
    const entries: FileSystemEntry[] = [];
    for (const item of Array.from(items)) {
      const entry = item.webkitGetAsEntry();
      if (entry) entries.push(entry);
    }
    if (entries.length) {
      const nested = await Promise.all(entries.map(readEntry));
      return nested.flat();
    }
  }
  return Array.from(dt.files ?? []);
}

// ProgressRing is a compact circular progress indicator.
function ProgressRing({ pct }: { pct: number }) {
  const r = 13;
  const c = 2 * Math.PI * r;
  return (
    <svg width="32" height="32" viewBox="0 0 32 32" className="shrink-0" role="img" aria-label={`${pct}%`}>
      <circle cx="16" cy="16" r={r} fill="none" stroke="var(--color-border)" strokeWidth="3" />
      <circle
        cx="16"
        cy="16"
        r={r}
        fill="none"
        stroke="var(--color-accent)"
        strokeWidth="3"
        strokeLinecap="round"
        strokeDasharray={c}
        strokeDashoffset={c - (c * pct) / 100}
        transform="rotate(-90 16 16)"
        style={{ transition: "stroke-dashoffset 0.2s ease" }}
      />
      <text x="16" y="20" textAnchor="middle" fontSize="9" fill="currentColor">
        {pct}
      </text>
    </svg>
  );
}

export function Uploader() {
  const t = useT();
  const qc = useQueryClient();
  const [uploads, setUploads] = useState<UploadState[]>([]);
  const [dragging, setDragging] = useState(false);
  const [slug, setSlug] = useState("");
  // The encrypt toggle starts from the server's encrypt_default; null = the
  // user has not touched it. Until the default is known (and only then) the
  // choice is left to the server by omitting the parameter.
  const { data: defaults } = useUploadDefaults();
  const [encryptChoice, setEncryptChoice] = useState<boolean | null>(null);
  const encrypt: boolean | undefined = encryptChoice ?? defaults?.encrypt_default;
  const folderInput = useRef<HTMLInputElement>(null);
  // Keep the latest slug/encrypt readable inside the stable onDrop callback.
  const slugRef = useRef("");
  slugRef.current = slug;
  const encryptRef = useRef<boolean | undefined>(undefined);
  encryptRef.current = encrypt;
  const patchRow = (id: string, patch: Partial<UploadState>) =>
    setUploads((u) => u.map((s) => (s.id === id ? { ...s, ...patch } : s)));
  // Abort handles of running/queued uploads, by row id (for the cancel button).
  const controllers = useRef(new Map<string, AbortController>());

  const onDrop = useCallback(
    async (files: File[]) => {
      // A custom slug only makes sense for a single file, and only on the simple
      // (non-resumable) path — tus uploads don't carry it.
      const wantSlug = slugRef.current.trim();
      const useSlug = files.length === 1 && wantSlug && files[0].size < TUS_THRESHOLD ? wantSlug : "";
      if (wantSlug && !useSlug) {
        toast(tGlobal("uploader.slugIgnored"), "error");
      }
      const useEncrypt = encryptRef.current;

      await Promise.all(
        files.map(async (file) => {
          // A stable id per upload; deriving the row from uploads.length would be
          // a stale closure and make concurrent drops overwrite the same row.
          const id = uuid();
          const ctrl = new AbortController();
          controllers.current.set(id, ctrl);
          setUploads((u) => [...u, { id, name: file.name, progress: 0 }]);
          const update = (patch: Partial<UploadState>) =>
            setUploads((u) => u.map((s) => (s.id === id ? { ...s, ...patch } : s)));
          const onProgress = (p: Progress) => update({ progress: p.pct, speed: p.speed, eta: p.eta });
          try {
            const res = await runUpload(() => {
              if (ctrl.signal.aborted) throw abortError(); // cancelled while queued
              return file.size >= TUS_THRESHOLD
                ? tusUpload(file, onProgress, { encrypt: useEncrypt, signal: ctrl.signal })
                : uploadFileWithRetry(file, onProgress, useSlug, useEncrypt, ctrl.signal);
            });
            const copied = await copyToClipboard(res.url);
            update({ progress: 100, speed: undefined, eta: undefined, url: res.url, copied });
          } catch (e) {
            if (isAbortError(e)) {
              setUploads((u) => u.filter((s) => s.id !== id)); // cancelled: drop the row
            } else {
              update({ error: e instanceof Error ? e.message : tGlobal("uploader.failed") });
            }
          } finally {
            controllers.current.delete(id);
          }
        }),
      );
      if (useSlug) setSlug(""); // a custom slug is single-use
      const refresh = () => {
        qc.invalidateQueries({ queryKey: ["files"] });
        qc.invalidateQueries({ queryKey: ["stats"] });
        qc.invalidateQueries({ queryKey: ["activity"] });
      };
      refresh();
      for (const ms of POST_PROCESS_REFETCH_MS) {
        setTimeout(() => qc.invalidateQueries({ queryKey: ["files"] }), ms);
      }
    },
    [qc],
  );

  // Full-page drag-&-drop: a window-level overlay so files can be dropped
  // anywhere, not just on the dropzone. react-dropzone handles click-to-select
  // (noDrag) to avoid double handling.
  useEffect(() => {
    let depth = 0;
    const hasFiles = (e: DragEvent) => Array.from(e.dataTransfer?.types ?? []).includes("Files");
    const onEnter = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth++;
      setDragging(true);
    };
    const onOver = (e: DragEvent) => {
      if (hasFiles(e)) e.preventDefault();
    };
    const onLeave = (e: DragEvent) => {
      e.preventDefault();
      depth = Math.max(0, depth - 1);
      if (depth === 0) setDragging(false);
    };
    const onDropWin = (e: DragEvent) => {
      e.preventDefault();
      depth = 0;
      setDragging(false);
      if (!e.dataTransfer) return;
      // Recurse into dropped folders (webkitGetAsEntry) so a whole directory of
      // files uploads at once; falls back to the flat file list otherwise.
      filesFromDataTransfer(e.dataTransfer).then((files) => {
        if (files.length) onDrop(files);
      });
    };
    window.addEventListener("dragenter", onEnter);
    window.addEventListener("dragover", onOver);
    window.addEventListener("dragleave", onLeave);
    window.addEventListener("drop", onDropWin);
    return () => {
      window.removeEventListener("dragenter", onEnter);
      window.removeEventListener("dragover", onOver);
      window.removeEventListener("dragleave", onLeave);
      window.removeEventListener("drop", onDropWin);
    };
  }, [onDrop]);

  // Clipboard paste: Ctrl/Cmd+V pastes images/files straight into the uploader.
  // Ignore pastes while typing in a form field so search/rename keep working.
  useEffect(() => {
    const onPaste = (e: ClipboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
      const files = Array.from(e.clipboardData?.files ?? []);
      if (files.length) {
        e.preventDefault();
        onDrop(files);
      }
    };
    window.addEventListener("paste", onPaste);
    return () => window.removeEventListener("paste", onPaste);
  }, [onDrop]);

  const { getRootProps, getInputProps } = useDropzone({ onDrop, noDrag: true });

  return (
    <div>
      {dragging && (
        <div className="fixed inset-0 z-40 flex items-center justify-center bg-black/90">
          <div className="rounded-2xl border-2 border-dashed border-[var(--color-accent)] bg-[var(--color-bg)] px-12 py-10 text-lg font-medium">
            {t("uploader.dropOverlay")}
          </div>
        </div>
      )}

      <div
        {...getRootProps()}
        className="card-lift cursor-pointer rounded-xl border-2 border-dashed border-[var(--color-border)] p-10 text-center hover:border-[var(--color-accent)]"
      >
        <input {...getInputProps({ "aria-label": t("uploader.uploadFiles") })} />
        <p className="text-[var(--color-muted)]">{t("uploader.dropzone")}</p>
      </div>

      {/* Hidden folder picker — webkitdirectory selects an entire directory. */}
      <input
        ref={folderInput}
        type="file"
        // @ts-expect-error non-standard but widely supported directory attributes
        webkitdirectory=""
        directory=""
        multiple
        className="hidden"
        onChange={(e) => {
          const files = Array.from(e.target.files ?? []);
          if (files.length) onDrop(files);
          e.target.value = "";
        }}
      />

      <div className="mt-3 flex flex-wrap items-center gap-2 text-sm">
        <button
          type="button"
          onClick={() => folderInput.current?.click()}
          className="inline-flex items-center gap-1.5 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-[var(--color-muted)] hover:text-[var(--color-text)]"
        >
          <FolderUp className="h-4 w-4" /> {t("uploader.selectFolder")}
        </button>
        <input
          value={slug}
          onChange={(e) => setSlug(e.target.value)}
          placeholder={t("uploader.customLinkPlaceholder")}
          className="min-w-0 flex-1 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 outline-none focus:border-[var(--color-accent)]"
          aria-label={t("uploader.customSlugLabel")}
        />
        <label
          className="inline-flex cursor-pointer items-center gap-1.5 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-[var(--color-muted)] hover:text-[var(--color-text)]"
          title={t("uploader.encryptTitle")}
        >
          <input
            type="checkbox"
            checked={encrypt ?? false}
            disabled={defaults !== undefined && !defaults.encryption_available}
            onChange={(e) => setEncryptChoice(e.target.checked)}
            className="h-4 w-4 accent-[var(--color-accent)]"
          />
          <Lock className="h-4 w-4" /> {t("uploader.encrypt")}
        </label>
      </div>

      {uploads.length > 0 && (
        <ul className="mt-4 space-y-2 text-sm" aria-live="polite">
          {uploads.map((u) => (
            <li key={u.id} className="flex items-center gap-3">
              {!u.error && !u.url && <ProgressRing pct={u.progress} />}
              <span className="flex-1 truncate">{u.name}</span>
              {u.error ? (
                <span className="text-[var(--color-danger)]">{u.error}</span>
              ) : u.url && u.copied ? (
                <span className="inline-flex items-center gap-1 text-[var(--color-success)]">
                  <Check className="h-4 w-4" aria-hidden="true" /> {t("uploader.copiedLink")}
                </span>
              ) : u.url ? (
                <>
                  <a
                    href={u.url}
                    target="_blank"
                    rel="noreferrer"
                    className="max-w-[50%] truncate text-[var(--color-accent)] underline"
                  >
                    {u.url}
                  </a>
                  <button
                    type="button"
                    onClick={async () => {
                      const ok = await copyToClipboard(u.url!);
                      if (ok) patchRow(u.id, { copied: true });
                      else toast(t("common.copyFailed"), "error");
                    }}
                    aria-label={`${t("common.copyLink")}: ${u.name}`}
                    title={t("common.copyLink")}
                    className="shrink-0 rounded p-0.5 text-[var(--color-muted)] hover:text-[var(--color-text)]"
                  >
                    <Copy className="h-4 w-4" aria-hidden="true" />
                  </button>
                </>
              ) : (
                <>
                  <span className="text-[var(--color-muted)]">
                    {u.speed ? `${formatBytes(u.speed)}/s` : ""}
                    {u.eta ? ` · ${t("uploader.secondsLeft", { n: Math.ceil(u.eta) })}` : ""}
                  </span>
                  <button
                    type="button"
                    onClick={() => controllers.current.get(u.id)?.abort()}
                    aria-label={`${t("common.cancel")}: ${u.name}`}
                    title={t("common.cancel")}
                    className="shrink-0 rounded p-0.5 text-[var(--color-muted)] hover:text-[var(--color-text)]"
                  >
                    <X className="h-4 w-4" aria-hidden="true" />
                  </button>
                </>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
