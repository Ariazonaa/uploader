import { useState } from "react";
import { useCreateInvite, useInvites, useRevokeInvite } from "../lib/api/hooks";
import { Button, Card, Input } from "../components/ui";
import { formatDate } from "../lib/format";
import { toast } from "../lib/toast";
import { copyToClipboard } from "../lib/clipboard";
import { useT } from "../lib/i18n/context";

export function InvitesPage() {
  const t = useT();
  const { data: invites } = useInvites();
  const create = useCreateInvite();
  const revoke = useRevokeInvite();
  const [days, setDays] = useState("0");

  const origin = window.location.origin;

  // The link carries the code, so the register form arrives prefilled.
  const copyInviteLink = async (code: string) => {
    const ok = await copyToClipboard(`${origin}/register?code=${encodeURIComponent(code)}`);
    toast(ok ? t("common.linkCopied") : t("common.copyFailed"), ok ? "success" : "error");
  };

  return (
    <div className="mx-auto max-w-2xl space-y-6 p-6">
      <h1 className="text-lg font-semibold">{t("invites.title")}</h1>

      <Card className="space-y-3 p-4">
        <p className="text-sm text-[var(--color-muted)]">{t("invites.intro")}</p>
        <div className="flex gap-2">
          <Input
            type="number"
            min={0}
            aria-label={t("invites.expiryLabel")}
            className="w-32"
            value={days}
            onChange={(e) => setDays(e.target.value)}
          />
          <Button
            onClick={() =>
              // The API takes whole days; a fraction rounds up instead of being refused.
              create.mutate(Math.ceil(Number(days) || 0), {
                onSuccess: async (res) => {
                  const copied = await copyToClipboard(res.code);
                  toast(copied ? t("invites.created") : t("invites.createdNotCopied"), "success");
                },
              })
            }
            disabled={create.isPending}
          >
            {t("invites.create")}
          </Button>
        </div>
      </Card>

      <div className="space-y-2">
        {(invites ?? []).map((inv) => (
          <Card key={inv.code} className="flex items-center justify-between gap-3 p-3 text-sm">
            <div className="min-w-0">
              <code className="block break-all">{inv.code}</code>
              <div className="text-xs text-[var(--color-muted)]">
                {t("invites.createdOn", { date: formatDate(inv.created_at) })}
                {inv.expires_at
                  ? ` · ${t("invites.expiresOn", { date: formatDate(inv.expires_at) })}`
                  : ` · ${t("invites.noExpiry")}`}
                {inv.used ? ` · ${t("invites.used")}` : ` · ${t("invites.unused")}`}
              </div>
            </div>
            <div className="flex shrink-0 gap-2">
              {!inv.used && (
                <Button variant="ghost" onClick={() => copyInviteLink(inv.code)}>
                  {t("common.copyLink")}
                </Button>
              )}
              {!inv.used && (
                <Button variant="danger" onClick={() => revoke.mutate(inv.code)} disabled={revoke.isPending}>
                  {t("common.revoke")}
                </Button>
              )}
            </div>
          </Card>
        ))}
        {invites && invites.length === 0 && (
          <p className="text-sm text-[var(--color-muted)]">{t("invites.empty")}</p>
        )}
      </div>
    </div>
  );
}
