import { NavLink, useNavigate } from "react-router-dom";
import { useLogout, useMe, usePublicSettings } from "../lib/api/hooks";
import { safeHttpUrl } from "../lib/url";
import { navRoutes } from "../lib/routes";
import { useT, LanguageSwitcher } from "../lib/i18n/context";
import { Button } from "./ui";

export function Nav() {
  const t = useT();
  const { data: me } = useMe();
  const { data: pub } = usePublicSettings();
  const logout = useLogout();
  const navigate = useNavigate();

  const link = ({ isActive }: { isActive: boolean }) =>
    `rounded-full px-3 py-1.5 text-sm transition ${
      isActive
        ? "control text-[var(--color-text)]"
        : "border border-transparent text-[var(--color-muted)] hover:bg-white/10 hover:text-[var(--color-text)]"
    }`;

  return (
    <header className="sticky top-0 z-40 mx-2 mt-2 flex flex-wrap items-center justify-between gap-2 rounded-[1.6rem] panel px-3 py-2 sm:mx-4 sm:px-4">
      <div className="flex flex-wrap items-center gap-1">
        <span className="mr-3 flex items-center gap-2 font-semibold tracking-tight">
          {safeHttpUrl(pub?.app_logo_url) && (
            <img src={safeHttpUrl(pub?.app_logo_url)} alt="" className="h-6 w-6 object-contain" />
          )}
          {pub?.app_name || "Uploader"}
        </span>
        {/* Primary links — hidden on mobile (bottom nav takes over). */}
        <nav aria-label={t("nav.primary")} className="hidden items-center gap-1 sm:flex">
          {navRoutes
            .filter((r) => me?.is_admin || r.slot !== "admin")
            .map((r) => (
              <NavLink key={r.path} to={r.path} end={r.end} className={link}>
                {t(r.labelKey)}
              </NavLink>
            ))}
        </nav>
      </div>
      <div className="flex items-center gap-3 text-sm text-[var(--color-muted)]">
        {me && <span className="hidden sm:inline">{me.username}</span>}
        <LanguageSwitcher />
        <Button
          variant="ghost"
          onClick={() => logout.mutate(undefined, { onSettled: () => navigate("/login", { replace: true }) })}
        >
          {t("nav.logout")}
        </Button>
      </div>
    </header>
  );
}
