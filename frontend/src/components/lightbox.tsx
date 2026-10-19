import { useEffect, useRef } from "react";
import { X } from "lucide-react";
import type { FileItem } from "../lib/api/types";
import { useT } from "../lib/i18n/context";

// Selector for the elements that participate in the modal's focus trap.
const FOCUSABLE =
  'a[href], button:not([disabled]), video, img[tabindex="0"], [tabindex]:not([tabindex="-1"])';

// Lightbox renders a fullscreen viewer for an image or video file. Pass null to
// close. It is a real modal dialog: focus is captured on open, trapped inside
// while open, and restored to the opener on close. Closes on Escape or backdrop
// click.
export function Lightbox({ file, onClose }: { file: FileItem | null; onClose: () => void }) {
  const t = useT();
  const dialogRef = useRef<HTMLDivElement>(null);
  const restoreRef = useRef<HTMLElement | null>(null);
  // Callers pass an inline arrow, a new function every render; reading it
  // through a ref keeps the effect below tied to the file alone, so focus is
  // not re-captured (and restored) on every parent render.
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  });

  useEffect(() => {
    if (!file) return;
    // Remember what had focus so we can restore it when the modal closes, then
    // move focus into the dialog.
    restoreRef.current = document.activeElement as HTMLElement | null;
    dialogRef.current?.focus();

    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onCloseRef.current();
        return;
      }
      if (e.key !== "Tab") return;
      const nodes = dialogRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE);
      if (!nodes || nodes.length === 0) return;
      const list = Array.from(nodes);
      const first = list[0];
      const last = list[list.length - 1];
      const active = document.activeElement as HTMLElement | null;
      const inside = dialogRef.current?.contains(active) && active !== dialogRef.current;
      if (e.shiftKey) {
        if (!inside || active === first) {
          e.preventDefault();
          last.focus();
        }
      } else {
        if (!inside || active === last) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      restoreRef.current?.focus?.();
    };
  }, [file]);

  if (!file) return null;
  const isImage = file.mime_type.startsWith("image/");
  const isVideo = file.mime_type.startsWith("video/");
  // Respect reduced-motion: don't auto-play video for users who opted out.
  const reduceMotion =
    typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  return (
    <div
      ref={dialogRef}
      role="dialog"
      aria-modal="true"
      aria-label={file.name}
      tabIndex={-1}
      className="fixed inset-0 z-50 flex flex-col items-center justify-center bg-black/85 p-4 outline-none"
      onClick={onClose}
    >
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          onClose();
        }}
        aria-label={t("lightbox.close")}
        className="absolute right-4 top-4 z-10 inline-flex h-10 w-10 items-center justify-center rounded-full bg-white/10 text-white transition hover:bg-white/20"
      >
        <X className="h-5 w-5" aria-hidden="true" />
      </button>
      <div className="max-h-[90vh] max-w-[92vw]" onClick={(e) => e.stopPropagation()}>
        {isImage ? (
          <img
            src={file.raw}
            alt={file.name}
            tabIndex={0}
            className="max-h-[85vh] max-w-[92vw] rounded-lg object-contain"
          />
        ) : isVideo ? (
          <video
            src={file.raw}
            controls
            autoPlay={!reduceMotion}
            className="max-h-[85vh] max-w-[92vw] rounded-lg"
          />
        ) : (
          <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-8 text-center">
            <p className="mb-2">{file.name}</p>
            <a
              href={file.raw}
              className="text-[var(--color-accent)] underline"
              target="_blank"
              rel="noreferrer"
            >
              {t("lightbox.openFile")}
            </a>
          </div>
        )}
        <div className="mt-2 flex items-center justify-between gap-4 text-sm text-white/80">
          <span className="truncate">{file.name}</span>
          <a href={file.raw} target="_blank" rel="noreferrer" className="underline">
            {t("lightbox.directLink")}
          </a>
        </div>
      </div>
    </div>
  );
}
