import { useState } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router-dom";
import { usePublicSettings, useRegister } from "../lib/api/hooks";
import { Button, Card, Input } from "../components/ui";
import { toast } from "../lib/toast";
import { useT } from "../lib/i18n/context";
import { translateApiError } from "../lib/i18n";

export function RegisterPage() {
  const t = useT();
  const navigate = useNavigate();
  const { data: pub, isLoading } = usePublicSettings();
  const register = useRegister();
  // An invite link (/register?code=…) prefills the code.
  const [params] = useSearchParams();
  const [form, setForm] = useState(() => ({
    username: "",
    email: "",
    password: "",
    invite_code: params.get("code")?.trim() ?? "",
  }));

  if (isLoading) {
    return (
      <div className="flex min-h-full items-center justify-center text-[var(--color-muted)]">
        {t("common.loading")}
      </div>
    );
  }
  // Registration is opt-in; if disabled, never expose the form.
  if (!pub?.registration_enabled) return <Navigate to="/login" replace />;

  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm({ ...form, [k]: e.target.value });

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    register.mutate(form, {
      onSuccess: () => {
        toast(t("register.created"), "success");
        navigate("/login", { replace: true });
      },
    });
  };

  return (
    <div className="flex min-h-full items-center justify-center p-6">
      <Card className="w-full max-w-sm p-6">
        <h1 className="mb-1 text-xl font-semibold">{t("register.title")}</h1>
        <p className="mb-6 text-sm text-[var(--color-muted)]">{t("register.inviteRequired")}</p>
        <form onSubmit={onSubmit} className="space-y-3">
          <Input
            placeholder={t("login.username")}
            aria-label={t("login.username")}
            autoComplete="username"
            value={form.username}
            onChange={set("username")}
          />
          <Input
            type="email"
            placeholder={t("register.email")}
            aria-label={t("register.email")}
            autoComplete="email"
            value={form.email}
            onChange={set("email")}
          />
          <Input
            type="password"
            placeholder={t("register.passwordPlaceholder")}
            aria-label={t("login.password")}
            autoComplete="new-password"
            value={form.password}
            onChange={set("password")}
          />
          <Input
            placeholder={t("register.inviteCode")}
            aria-label={t("register.inviteCode")}
            value={form.invite_code}
            onChange={set("invite_code")}
          />
          {register.isError && (
            <p role="alert" className="text-sm text-[var(--color-danger)]">
              {translateApiError(register.error)}
            </p>
          )}
          <Button type="submit" className="w-full" disabled={register.isPending}>
            {register.isPending ? t("register.creating") : t("register.title")}
          </Button>
        </form>
      </Card>
    </div>
  );
}
