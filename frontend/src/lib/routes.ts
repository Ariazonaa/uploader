import {
  BookOpen,
  ChartColumn,
  Files,
  KeyRound,
  ScrollText,
  Settings,
  SlidersHorizontal,
  Ticket,
  Trash2,
  Trophy,
  type LucideIcon,
} from "lucide-react";
import type { MessageKey } from "./i18n/types";

// NavRoute is one navigation destination. This table is the single source for
// the desktop header (nav.tsx), the mobile tab bar and its "more" sheet
// (bottomnav.tsx), the protected routes (app.tsx) and the document title
// (useRouteFocus.ts) — add a page here and every one of them picks it up.
//
// slot decides where the mobile bottom nav shows it: "tab" gets its own tab,
// "more" and "admin" land in the overflow sheet (admin ones under a heading,
// for admins only). The desktop header lists them all in table order.
export interface NavRoute {
  path: string;
  labelKey: MessageKey;
  // Shorter label for the mobile tab, when it differs.
  tabLabelKey?: MessageKey;
  // Document title, when it differs from the nav label.
  titleKey?: MessageKey;
  icon: LucideIcon;
  slot: "tab" | "more" | "admin";
  end?: boolean;
}

export const NAV_ROUTES = [
  { path: "/", labelKey: "nav.dashboard", tabLabelKey: "nav.files", icon: Files, slot: "tab", end: true },
  { path: "/trash", labelKey: "nav.trash", icon: Trash2, slot: "more" },
  { path: "/tokens", labelKey: "nav.tokens", titleKey: "tokens.title", icon: KeyRound, slot: "tab" },
  { path: "/profile", labelKey: "nav.profile", icon: Trophy, slot: "tab" },
  { path: "/account", labelKey: "nav.account", icon: Settings, slot: "tab" },
  { path: "/docs", labelKey: "nav.api", icon: BookOpen, slot: "more" },
  { path: "/settings", labelKey: "nav.settings", icon: SlidersHorizontal, slot: "admin" },
  { path: "/traffic", labelKey: "nav.traffic", icon: ChartColumn, slot: "admin" },
  { path: "/invites", labelKey: "nav.invites", icon: Ticket, slot: "admin" },
  { path: "/audit", labelKey: "nav.audit", titleKey: "audit.title", icon: ScrollText, slot: "admin" },
] as const satisfies readonly NavRoute[];

export type NavPath = (typeof NAV_ROUTES)[number]["path"];

// navRoutes is the same table widened to NavRoute, for code that reads the
// optional fields.
export const navRoutes: readonly NavRoute[] = NAV_ROUTES;

// Old paths that still resolve (bookmarks, links in chat history).
export const ROUTE_REDIRECTS: Record<string, NavPath> = {
  "/reports": "/traffic",
};

// Titles of the public (logged-out) pages, which have no nav entry.
const PUBLIC_TITLES: Record<string, MessageKey> = {
  "/login": "login.signIn",
  "/register": "register.title",
  "/legal": "legal.title",
};

// routeTitleKey is the message key for a page's document title, if known.
export function routeTitleKey(pathname: string): MessageKey | undefined {
  const r = navRoutes.find((n) => n.path === pathname);
  if (r) return r.titleKey ?? r.labelKey;
  return PUBLIC_TITLES[pathname];
}
