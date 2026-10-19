import { useEffect, type ReactElement } from "react";
import { Navigate, Outlet, Route, Routes } from "react-router-dom";
import { useMe, usePublicSettings } from "./lib/api/hooks";
import { safeHttpUrl } from "./lib/url";
import { useT } from "./lib/i18n/context";
import { useRouteFocus } from "./lib/useRouteFocus";
import { NAV_ROUTES, ROUTE_REDIRECTS, type NavPath } from "./lib/routes";
import { SkipLink } from "./components/skiplink";
import { Nav } from "./components/nav";
import { BottomNav } from "./components/bottomnav";
import { ProfilePage } from "./pages/profile";
import { LegalPage } from "./pages/legal";
import { LoginPage } from "./pages/login";
import { RegisterPage } from "./pages/register";
import { DashboardPage } from "./pages/dashboard";
import { TrashPage } from "./pages/trash";
import { TokensPage } from "./pages/tokens";
import { SettingsPage } from "./pages/settings";
import { AccountPage } from "./pages/account";
import { TrafficPage } from "./pages/traffic";
import { InvitesPage } from "./pages/invites";
import { AuditPage } from "./pages/audit";
import { DocsPage } from "./pages/docs";
import { SetupPage } from "./pages/setup";

// PAGES maps every destination of the shared route table (lib/routes.ts) to its
// page; the Record type makes a table entry without a page a compile error.
const PAGES: Record<NavPath, ReactElement> = {
  "/": <DashboardPage />,
  "/trash": <TrashPage />,
  "/tokens": <TokensPage />,
  "/profile": <ProfilePage />,
  "/account": <AccountPage />,
  "/docs": <DocsPage />,
  "/settings": <SettingsPage />,
  "/traffic": <TrafficPage />,
  "/invites": <InvitesPage />,
  "/audit": <AuditPage />,
};

function ProtectedLayout() {
  const t = useT();
  const { data: me, isLoading, isError, refetch } = useMe();
  if (isLoading) {
    return (
      <div className="flex min-h-full items-center justify-center text-[var(--color-muted)]">
        {t("common.loading")}
      </div>
    );
  }
  // A non-auth failure (network/5xx) must not be treated as "logged out".
  if (isError) {
    return (
      <div className="flex min-h-full flex-col items-center justify-center gap-3 text-[var(--color-muted)]">
        <p>{t("app.unreachable")}</p>
        <button className="underline" onClick={() => refetch()}>
          {t("common.retry")}
        </button>
      </div>
    );
  }
  if (!me) return <Navigate to="/login" replace />;
  return (
    <div className="flex min-h-full flex-col">
      <Nav />
      <main id="main" tabIndex={-1} className="flex-1 pb-20 outline-none sm:pb-0">
        <Outlet />
      </main>
      <BottomNav />
    </div>
  );
}

// PublicLayout wraps the unauthenticated pages (login/register/legal) in the same
// #main landmark the protected pages use, so the skip link and per-route focus
// always have a single, consistent target. h-full preserves the definite-height
// chain the centered auth cards rely on.
function PublicLayout() {
  return (
    <main id="main" tabIndex={-1} className="h-full outline-none">
      <Outlet />
    </main>
  );
}

// AdminRoute guards admin-only pages on the client (the API enforces it too).
function AdminRoute({ children }: { children: React.ReactNode }) {
  const { data: me, isLoading } = useMe();
  if (isLoading) return null;
  if (!me?.is_admin) return <Navigate to="/" replace />;
  return <>{children}</>;
}

// Branding applies the configured app name (document title) and logo (favicon).
function Branding() {
  const { data: pub } = usePublicSettings();
  useEffect(() => {
    if (pub?.app_name) document.title = pub.app_name;
    const logo = safeHttpUrl(pub?.app_logo_url);
    if (logo) {
      let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
      if (!link) {
        link = document.createElement("link");
        link.rel = "icon";
        document.head.appendChild(link);
      }
      link.href = logo;
    }
  }, [pub?.app_name, pub?.app_logo_url]);
  return null;
}

// SetupGate forces the first-run wizard while no account exists, so a fresh
// install can be bootstrapped entirely from the browser.
function SetupGate({ children }: { children: React.ReactNode }) {
  const { data: pub, isLoading } = usePublicSettings();
  if (isLoading) return null;
  if (pub?.needs_setup) return <SetupPage />;
  return <>{children}</>;
}

export function App() {
  const { data: pub } = usePublicSettings();
  useRouteFocus(pub?.app_name || "Uploader");
  return (
    <>
      <SkipLink />
      <Branding />
      <SetupGate>
        <Routes>
          <Route element={<PublicLayout />}>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/register" element={<RegisterPage />} />
            <Route path="/legal" element={<LegalPage />} />
          </Route>
          <Route element={<ProtectedLayout />}>
            {NAV_ROUTES.map((r) => (
              <Route
                key={r.path}
                path={r.path}
                element={r.slot === "admin" ? <AdminRoute>{PAGES[r.path]}</AdminRoute> : PAGES[r.path]}
              />
            ))}
            {Object.entries(ROUTE_REDIRECTS).map(([from, to]) => (
              <Route key={from} path={from} element={<Navigate to={to} replace />} />
            ))}
          </Route>
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </SetupGate>
    </>
  );
}
