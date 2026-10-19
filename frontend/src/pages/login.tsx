import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useLogin, useLoginTotp, usePublicSettings } from "../lib/api/hooks";
import { Button, Card, Input } from "../components/ui";
import { safeHttpUrl } from "../lib/url";
import { useT, LanguageSwitcher } from "../lib/i18n/context";
import { translateApiError } from "../lib/i18n";

export function LoginPage() {
  const t = useT();
  const navigate = useNavigate();
  const login = useLogin();
  const loginTotp = useLoginTotp();
  const { data: pub } = usePublicSettings();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [pending, setPending] = useState<string | null>(null); // 2FA challenge token
  const [code, setCode] = useState("");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    login.mutate(
      { username, password },
      {
        onSuccess: (res) => {
          if ("totp_required" in res) setPending(res.pending);
          else navigate("/", { replace: true });
        },
      },
    );
  };

  const onTotp = (e: React.FormEvent) => {
    e.preventDefault();
    if (!pending) return;
    loginTotp.mutate({ pending, code }, { onSuccess: () => navigate("/", { replace: true }) });
  };

  if (pending) {
    return (
      <div className="flex min-h-full items-center justify-center p-6">
        <Card className="w-full max-w-sm p-6">
          <h1 className="mb-1 text-xl font-semibold">{t("login.totpTitle")}</h1>
          <p className="mb-6 text-sm text-[var(--color-muted)]">{t("login.totpSubtitle")}</p>
          <form onSubmit={onTotp} className="space-y-3">
            <Input
              placeholder={t("login.codePlaceholder")}
              aria-label={t("login.codeLabel")}
              autoComplete="one-time-code"
              autoFocus
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
            {loginTotp.isError && (
              <p role="alert" className="text-sm text-[var(--color-danger)]">
                {translateApiError(loginTotp.error)}
              </p>
            )}
            <Button type="submit" className="w-full" disabled={loginTotp.isPending}>
              {loginTotp.isPending ? t("common.verifying") : t("login.verify")}
            </Button>
            <button
              type="button"
              className="w-full text-center text-sm text-[var(--color-muted)] underline"
              onClick={() => {
                setPending(null);
                setCode("");
              }}
            >
              {t("common.back")}
            </button>
          </form>
        </Card>
      </div>
    );
  }

  const bgUrl = safeHttpUrl(pub?.login_bg_url);
  const logoUrl = safeHttpUrl(pub?.app_logo_url);
  const bgStyle = bgUrl
    ? { backgroundImage: `url("${bgUrl}")`, backgroundSize: "cover", backgroundPosition: "center" }
    : undefined;

  return (
    <div className="flex min-h-full items-center justify-center p-6" style={bgStyle}>
      <Card className="w-full max-w-sm p-6">
        <div className="mb-4 flex items-center justify-between gap-2">
          {logoUrl ? (
            <img src={logoUrl} alt="" className="h-10 object-contain" />
          ) : (
            <span aria-hidden="true" />
          )}
          <LanguageSwitcher />
        </div>
        <h1 className="mb-1 text-xl font-semibold">{pub?.login_heading || t("login.signIn")}</h1>
        <p className="mb-6 text-sm text-[var(--color-muted)]">
          {pub?.login_subtext || pub?.app_name || "Uploader"}
        </p>
        <form onSubmit={onSubmit} className="space-y-3">
          <Input
            placeholder={t("login.username")}
            aria-label={t("login.username")}
            autoComplete="username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
          <Input
            type="password"
            placeholder={t("login.password")}
            aria-label={t("login.password")}
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          {login.isError && (
            <p role="alert" className="text-sm text-[var(--color-danger)]">
              {translateApiError(login.error)}
            </p>
          )}
          <Button type="submit" className="w-full" disabled={login.isPending}>
            {login.isPending ? t("login.signingIn") : t("login.signIn")}
          </Button>
        </form>
        {pub?.registration_enabled && (
          <p className="mt-4 text-center text-sm text-[var(--color-muted)]">
            {t("login.haveInvite")}{" "}
            <Link to="/register" className="text-[var(--color-accent)] underline">
              {t("login.createAccount")}
            </Link>
          </p>
        )}
        {pub?.legal_notice && (
          <p className="mt-3 text-center text-xs text-[var(--color-muted)]">
            <Link to="/legal" className="underline">
              {t("login.legalNotice")}
            </Link>
          </p>
        )}
      </Card>
    </div>
  );
}
