import { useEffect, useRef } from "react";
import { useLocation } from "react-router-dom";
import { useT } from "./i18n/context";
import { routeTitleKey } from "./routes";

// useRouteFocus keeps the document title in sync with the current route and moves
// keyboard focus to the main landmark on client navigations. This is the SPA
// equivalent of a full page load: without it, screen-reader / keyboard users stay
// focused on a stale control and never hear that the page changed. The page name
// comes from the shared route table (lib/routes.ts) in the active language; the
// base title falls back to the app name (kept in sync by the Branding component).
export function useRouteFocus(appName: string) {
  const t = useT();
  const { pathname } = useLocation();

  useEffect(() => {
    const key = routeTitleKey(pathname);
    document.title = key ? `${t(key)} — ${appName}` : appName;
  }, [pathname, appName, t]);

  // Only move focus on real navigations, not the initial render, so we don't
  // steal focus (and scroll) away from the top of the page on first load.
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    document.getElementById("main")?.focus();
  }, [pathname]);
}
