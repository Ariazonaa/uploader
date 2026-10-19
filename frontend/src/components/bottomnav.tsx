import { useEffect, useId, useRef, useState } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { Ellipsis } from "lucide-react";
import { useT } from "../lib/i18n/context";
import { useMe } from "../lib/api/hooks";
import { navRoutes, type NavRoute } from "../lib/routes";

const items = navRoutes.filter((r) => r.slot === "tab");
// Everything the desktop header links to that has no tab of its own.
const moreItems = navRoutes.filter((r) => r.slot === "more");
const adminItems = navRoutes.filter((r) => r.slot === "admin");

const tabClass = (active: boolean) =>
  `flex flex-col items-center gap-1 rounded-[1.35rem] py-2 text-xs transition ${
    active
      ? "control text-[var(--color-text)]"
      : "border border-transparent text-[var(--color-muted)] hover:text-[var(--color-text)]"
  }`;

const sheetLinkClass = ({ isActive }: { isActive: boolean }) =>
  `flex items-center gap-3 rounded-[var(--radius-control)] px-3 py-2.5 text-sm transition ${
    isActive
      ? "control text-[var(--color-text)]"
      : "border border-transparent text-[var(--color-muted)] hover:border-[var(--color-border-strong)] hover:text-[var(--color-text)]"
  }`;

// BottomNav is a floating tab bar shown only on small screens. The fifth tab
// opens a sheet with the remaining destinations (trash, API docs and, for
// admins, the admin pages), which the desktop header shows inline.
export function BottomNav() {
  const t = useT();
  const { data: me } = useMe();
  const { pathname } = useLocation();
  const [open, setOpen] = useState(false);
  const sheetRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const sheetId = useId();
  const headingId = useId();

  const extra = me?.is_admin ? [...moreItems, ...adminItems] : moreItems;
  // The tab reads as active while one of its destinations is the current page.
  const moreActive = extra.some((i) => pathname === i.path);

  // Navigating closes the sheet; useRouteFocus then moves focus to <main>.
  useEffect(() => {
    setOpen(false);
  }, [pathname]);

  useEffect(() => {
    if (!open) return;
    // Move focus into the sheet so keyboard and screen-reader users land on
    // the first destination.
    sheetRef.current?.querySelector<HTMLElement>("a")?.focus();

    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        setOpen(false);
        buttonRef.current?.focus();
      }
    };
    // Any press outside the sheet and its toggle closes it (the toggle handles
    // its own clicks, so it is excluded here to avoid close-then-reopen).
    const onPointer = (e: PointerEvent) => {
      const target = e.target as Node;
      if (sheetRef.current?.contains(target) || buttonRef.current?.contains(target)) return;
      setOpen(false);
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onPointer);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onPointer);
    };
  }, [open]);

  // Tabbing out of the sheet (anywhere but back onto its toggle) closes it, so
  // it never lingers over content the keyboard user has moved on to.
  const onSheetBlur = (e: React.FocusEvent<HTMLDivElement>) => {
    const next = e.relatedTarget as Node | null;
    if (next && (sheetRef.current?.contains(next) || buttonRef.current?.contains(next))) return;
    if (next) setOpen(false);
  };

  const renderLinks = (list: readonly NavRoute[]) => (
    <ul className="grid gap-1">
      {list.map((i) => {
        const Icon = i.icon;
        return (
          <li key={i.path}>
            {/* The click also closes it for the page already open (no path change). */}
            <NavLink to={i.path} className={sheetLinkClass} onClick={() => setOpen(false)}>
              <Icon className="h-5 w-5 shrink-0" aria-hidden="true" />
              {t(i.labelKey)}
            </NavLink>
          </li>
        );
      })}
    </ul>
  );

  return (
    <>
      <nav
        aria-label={t("nav.sections")}
        className="panel fixed inset-x-3 bottom-3 z-30 grid grid-cols-5 gap-1 rounded-[1.75rem] p-1.5 sm:hidden"
      >
        {items.map((i) => {
          const Icon = i.icon;
          return (
            <NavLink key={i.path} to={i.path} end={i.end} className={({ isActive }) => tabClass(isActive)}>
              <Icon className="h-5 w-5" aria-hidden="true" />
              {t(i.tabLabelKey ?? i.labelKey)}
            </NavLink>
          );
        })}
        <button
          ref={buttonRef}
          type="button"
          aria-expanded={open}
          aria-controls={open ? sheetId : undefined}
          onClick={() => setOpen((o) => !o)}
          className={tabClass(open || moreActive)}
        >
          <Ellipsis className="h-5 w-5" aria-hidden="true" />
          {t("nav.more")}
        </button>
      </nav>
      {open && (
        <div
          ref={sheetRef}
          id={sheetId}
          role="region"
          aria-labelledby={headingId}
          onBlur={onSheetBlur}
          className="panel fixed inset-x-3 bottom-[5.25rem] z-40 max-h-[70vh] overflow-y-auto rounded-[var(--radius-card)] p-2 sm:hidden"
        >
          <h2 id={headingId} className="sr-only">
            {t("nav.more")}
          </h2>
          {renderLinks(moreItems)}
          {me?.is_admin && (
            <>
              <div className="mx-3 mt-3 mb-1 border-t border-[var(--color-border)] pt-3 text-[length:var(--text-meta)] tracking-wide text-[var(--color-muted)] uppercase">
                {t("nav.admin")}
              </div>
              {renderLinks(adminItems)}
            </>
          )}
        </div>
      )}
    </>
  );
}
