import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  useChangePassword,
  useDeleteAccount,
  useMe,
  usePublicSettings,
  usePushTest,
  useRevokeOtherSessions,
  useRevokeSession,
  useSessions,
  useTotpDisable,
  useTotpEnable,
  useTotpSetup,
} from "../lib/api/hooks";
import { isPushSubscribed, pushSupported, subscribePush, unsubscribePush } from "../lib/push";
import { Button, Card, Input } from "../components/ui";
import { formatDate } from "../lib/format";
import { toast } from "../lib/toast";
import { useT } from "../lib/i18n/context";
import { translateApiError } from "../lib/i18n";

function ChangePassword() {
  const t = useT();
  const change = useChangePassword();
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    change.mutate(
      { current_password: cur, new_password: next },
      {
        onSuccess: () => {
          toast(t("account.passwordChanged"), "success");
          setCur("");
          setNext("");
        },
      },
    );
  };

  return (
    <Card className="space-y-3 p-4">
      <h3 className="font-semibold">{t("account.changePassword")}</h3>
      <form onSubmit={onSubmit} className="space-y-3">
        <Input
          type="password"
          placeholder={t("account.currentPassword")}
          aria-label={t("account.currentPassword")}
          autoComplete="current-password"
          value={cur}
          onChange={(e) => setCur(e.target.value)}
        />
        <Input
          type="password"
          placeholder={t("account.newPasswordPlaceholder")}
          aria-label={t("account.newPassword")}
          autoComplete="new-password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
        />
        <Button type="submit" disabled={change.isPending}>
          {change.isPending ? t("common.saving") : t("account.updatePassword")}
        </Button>
      </form>
    </Card>
  );
}

function TwoFactor() {
  const t = useT();
  const { data: me } = useMe();
  const setup = useTotpSetup();
  const enable = useTotpEnable();
  const disable = useTotpDisable();
  const [enrolling, setEnrolling] = useState<{ secret: string; otpauth_url: string } | null>(null);
  const [code, setCode] = useState("");
  // Asked once at setup and sent again with the code: the server re-checks the
  // password for both steps.
  const [password, setPassword] = useState("");
  const [recovery, setRecovery] = useState<string[] | null>(null);
  // Password typed to turn 2FA off; null while that form is closed.
  const [disablePw, setDisablePw] = useState<string | null>(null);

  const resetEnrolling = () => {
    setEnrolling(null);
    setCode("");
    setPassword("");
  };

  // Checked before totp_enabled: enabling 2FA refetches `me`, and the codes are
  // shown exactly once, so the "enabled" card must not replace them.
  if (recovery) {
    return (
      <Card className="space-y-3 p-4">
        <h3 className="font-semibold">{t("account.saveRecovery")}</h3>
        <p className="text-sm text-[var(--color-muted)]">{t("account.recoveryHint")}</p>
        <pre className="rounded border border-[var(--color-border)] p-3 text-sm">{recovery.join("\n")}</pre>
        <Button onClick={() => setRecovery(null)}>{t("common.done")}</Button>
      </Card>
    );
  }

  if (me?.totp_enabled) {
    return (
      <Card className="space-y-3 p-4">
        <h3 className="font-semibold">{t("account.twoFactor")}</h3>
        <p className="text-sm text-[var(--color-success)]">{t("account.twoFactorEnabled")}</p>
        {disablePw === null ? (
          <Button variant="danger" onClick={() => setDisablePw("")}>
            {t("account.disable2fa")}
          </Button>
        ) : (
          // A masked inline form rather than window.prompt(), which shows the
          // password in clear text.
          <form
            className="space-y-2"
            onSubmit={(e) => {
              e.preventDefault();
              disable.mutate(disablePw, {
                onSuccess: () => {
                  setDisablePw(null);
                  toast(t("account.twoFactorDisabled"), "success");
                },
              });
            }}
          >
            <p className="text-sm text-[var(--color-muted)]">{t("account.disablePrompt")}</p>
            <Input
              type="password"
              placeholder={t("account.confirmPassword")}
              aria-label={t("account.confirmPasswordLabel")}
              autoComplete="current-password"
              autoFocus
              value={disablePw}
              onChange={(e) => setDisablePw(e.target.value)}
            />
            <div className="flex gap-2">
              <Button type="submit" variant="danger" disabled={disable.isPending || !disablePw}>
                {t("account.disable2fa")}
              </Button>
              <Button variant="ghost" onClick={() => setDisablePw(null)}>
                {t("common.cancel")}
              </Button>
            </div>
          </form>
        )}
      </Card>
    );
  }

  return (
    <Card className="space-y-3 p-4">
      <h3 className="font-semibold">{t("account.twoFactor")}</h3>
      {!enrolling ? (
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            setup.mutate(password, { onSuccess: (r) => setEnrolling(r) });
          }}
        >
          <p className="text-sm text-[var(--color-muted)]">{t("account.twoFactorIntro")}</p>
          <Input
            type="password"
            placeholder={t("account.confirmPassword")}
            aria-label={t("account.confirmPasswordLabel")}
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          <Button type="submit" disabled={setup.isPending || !password}>
            {t("account.setup2fa")}
          </Button>
        </form>
      ) : (
        <div className="space-y-3">
          <p className="text-sm">{t("account.enrollIntro")}</p>
          <code className="block break-all rounded border border-[var(--color-border)] p-2 text-sm">
            {enrolling.secret}
          </code>
          <a
            className="block break-all text-xs text-[var(--color-accent)] underline"
            href={enrolling.otpauth_url}
          >
            {enrolling.otpauth_url}
          </a>
          <Input
            placeholder={t("account.codePlaceholder")}
            aria-label={t("account.codeLabel")}
            autoComplete="one-time-code"
            value={code}
            onChange={(e) => setCode(e.target.value)}
          />
          {enable.isError && (
            <p className="text-sm text-[var(--color-danger)]">{translateApiError(enable.error)}</p>
          )}
          <div className="flex gap-2">
            <Button
              onClick={() =>
                enable.mutate(
                  { code, password },
                  {
                    onSuccess: (r) => {
                      setRecovery(r.recovery_codes);
                      resetEnrolling();
                    },
                  },
                )
              }
              disabled={enable.isPending}
            >
              {enable.isPending ? t("common.verifying") : t("account.enable")}
            </Button>
            <Button variant="ghost" onClick={resetEnrolling}>
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      )}
    </Card>
  );
}

function PushNotifications() {
  const t = useT();
  const { data: pub } = usePublicSettings();
  const [subscribed, setSubscribed] = useState(false);
  const test = usePushTest();
  // Mutations, so a failure goes through the shared error toast (MutationCache).
  const enable = useMutation({
    mutationFn: (key: string) => subscribePush(key),
    onSuccess: () => {
      setSubscribed(true);
      toast(t("account.notificationsEnabled"), "success");
    },
  });
  const disable = useMutation({
    mutationFn: unsubscribePush,
    onSuccess: () => {
      setSubscribed(false);
      toast(t("account.notificationsDisabled"), "success");
    },
  });
  const busy = enable.isPending || disable.isPending;

  const pushEnabled = !!pub?.push_enabled;
  useEffect(() => {
    if (!pushEnabled) return;
    let live = true;
    isPushSubscribed().then((v) => live && setSubscribed(v));
    return () => {
      live = false;
    };
  }, [pushEnabled]);

  if (!pub?.push_enabled) return null; // server has no VAPID keys configured

  return (
    <Card className="space-y-3 p-4">
      <h3 className="font-semibold">{t("account.browserNotifications")}</h3>
      {!pushSupported() ? (
        <p className="text-sm text-[var(--color-muted)]">{t("account.pushUnsupported")}</p>
      ) : subscribed ? (
        <div className="flex flex-wrap gap-2">
          <Button
            variant="ghost"
            disabled={test.isPending}
            onClick={() =>
              test.mutate(undefined, { onSuccess: () => toast(t("account.testSent"), "success") })
            }
          >
            {t("account.sendTest")}
          </Button>
          <Button variant="danger" onClick={() => disable.mutate()} disabled={busy}>
            {t("account.disableNotifications")}
          </Button>
        </div>
      ) : (
        <Button onClick={() => enable.mutate(pub.vapid_public_key)} disabled={busy}>
          {busy ? t("account.enabling") : t("account.enableNotifications")}
        </Button>
      )}
    </Card>
  );
}

function Sessions() {
  const t = useT();
  const { data: sessions } = useSessions();
  const revoke = useRevokeSession();
  const revokeOthers = useRevokeOtherSessions();

  return (
    <Card className="space-y-3 p-4">
      <div className="flex items-center justify-between">
        <h3 className="font-semibold">{t("account.sessions")}</h3>
        <Button variant="ghost" onClick={() => revokeOthers.mutate()} disabled={revokeOthers.isPending}>
          {t("account.logoutOthers")}
        </Button>
      </div>
      <div className="space-y-2">
        {(sessions ?? []).map((s) => (
          <div key={s.id} className="flex items-center justify-between gap-3 text-sm">
            <div className="min-w-0">
              <div className="truncate">
                {s.ip || t("account.unknownIp")}
                {s.current && (
                  <span className="ml-2 text-xs text-[var(--color-success)]">{t("account.thisDevice")}</span>
                )}
              </div>
              <div className="truncate text-xs text-[var(--color-muted)]" title={s.user_agent}>
                {s.user_agent || t("account.unknownDevice")} · {formatDate(s.created_at)}
              </div>
            </div>
            {!s.current && (
              <Button variant="ghost" onClick={() => revoke.mutate(s.id)} disabled={revoke.isPending}>
                {t("common.revoke")}
              </Button>
            )}
          </div>
        ))}
        {sessions && sessions.length === 0 && (
          <p className="text-sm text-[var(--color-muted)]">{t("account.noSessions")}</p>
        )}
      </div>
    </Card>
  );
}

function DataAndDanger() {
  const t = useT();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const del = useDeleteAccount();
  const [confirm, setConfirm] = useState(false);
  const [pw, setPw] = useState("");

  const onDelete = () => {
    del.mutate(pw, {
      onSuccess: () => {
        qc.clear();
        navigate("/login", { replace: true });
      },
    });
  };

  return (
    <Card className="space-y-3 p-4">
      <h3 className="font-semibold">{t("account.yourData")}</h3>
      <a
        href="/api/v1/auth/export"
        className="inline-block rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-2 text-sm transition hover:border-[var(--color-border-strong)] hover:bg-white/5"
      >
        {t("account.downloadData")}
      </a>

      <div className="mt-4 border-t border-[var(--color-border)] pt-4">
        <h3 className="font-semibold text-[var(--color-danger)]">{t("account.deleteAccount")}</h3>
        <p className="mb-2 text-sm text-[var(--color-muted)]">{t("account.deleteWarning")}</p>
        {!confirm ? (
          <Button variant="danger" onClick={() => setConfirm(true)}>
            {t("account.deleteAccountButton")}
          </Button>
        ) : (
          <div className="space-y-2">
            <Input
              type="password"
              placeholder={t("account.confirmPassword")}
              aria-label={t("account.confirmPasswordLabel")}
              autoComplete="current-password"
              value={pw}
              onChange={(e) => setPw(e.target.value)}
            />
            <div className="flex gap-2">
              <Button variant="danger" onClick={onDelete} disabled={del.isPending || !pw}>
                {del.isPending ? t("account.deleting") : t("account.permanentlyDelete")}
              </Button>
              <Button variant="ghost" onClick={() => setConfirm(false)}>
                {t("common.cancel")}
              </Button>
            </div>
          </div>
        )}
      </div>
    </Card>
  );
}

export function AccountPage() {
  const t = useT();
  return (
    <div className="mx-auto max-w-2xl space-y-6 p-6">
      <h1 className="text-lg font-semibold">{t("account.title")}</h1>
      <ChangePassword />
      <TwoFactor />
      <PushNotifications />
      <Sessions />
      <DataAndDanger />
    </div>
  );
}
