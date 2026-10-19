import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useSetup } from "../lib/api/hooks";
import { Button, Card, Input } from "../components/ui";
import { useT } from "../lib/i18n/context";
import { translateApiError } from "../lib/i18n";

// SetupPage is the first-run wizard: it creates the bootstrap admin account
// through the UI (no CLI needed) and logs them straight in. It is only reachable
// while the server reports needs_setup (no accounts exist yet).
export function SetupPage() {
  const t = useT();
  const navigate = useNavigate();
  const setup = useSetup();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");

  const mismatch = confirm.length > 0 && password !== confirm;
  // The server's limit (bcrypt's): 72 bytes of UTF-8, not 72 characters.
  const tooLong = new TextEncoder().encode(password).length > 72;

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (password !== confirm) return;
    setup.mutate({ username, email, password }, { onSuccess: () => navigate("/", { replace: true }) });
  };

  return (
    <div className="flex min-h-full items-center justify-center p-6">
      <Card className="w-full max-w-sm p-6">
        <h1 className="mb-1 text-xl font-semibold">{t("setup.welcome")}</h1>
        <p className="mb-6 text-sm text-[var(--color-muted)]">{t("setup.subtitle")}</p>
        <form onSubmit={onSubmit} className="space-y-3">
          <Input
            placeholder={t("login.username")}
            aria-label={t("login.username")}
            autoComplete="username"
            autoFocus
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
          <Input
            type="email"
            placeholder={t("register.email")}
            aria-label={t("register.email")}
            autoComplete="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <Input
            type="password"
            placeholder={t("setup.passwordPlaceholder")}
            aria-label={t("login.password")}
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          <Input
            type="password"
            placeholder={t("setup.confirmPassword")}
            aria-label={t("setup.confirmPassword")}
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
          {tooLong && (
            <p role="alert" className="text-sm text-[var(--color-danger)]">
              {t("setup.passwordTooLong")}
            </p>
          )}
          {mismatch && (
            <p role="alert" className="text-sm text-[var(--color-danger)]">
              {t("setup.mismatch")}
            </p>
          )}
          {setup.isError && (
            <p role="alert" className="text-sm text-[var(--color-danger)]">
              {translateApiError(setup.error)}
            </p>
          )}
          <Button
            type="submit"
            className="w-full"
            disabled={setup.isPending || password.length < 8 || tooLong || mismatch}
          >
            {setup.isPending ? t("register.creating") : t("setup.submit")}
          </Button>
        </form>
      </Card>
    </div>
  );
}
